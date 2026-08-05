// Package scheduler provides a fair, cancellable named-pool admission controller
// for in-process Strike work (shell processes, builds, tests, model streams,
// eval containers).
//
// Limits apply only inside one Strike OS process; separate Strike programs do
// not coordinate leases or share capacity. Layered global/project limits and
// ordered command classification rules are compiled via Compile into an
// Effective policy (see limits.go, classify.go, effective.go).
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
)

// Well-known pool names used by Strike admission sites.
const (
	PoolProcess   = "process"
	PoolBuild     = "build"
	PoolTest      = "test"
	PoolModel     = "model"
	PoolContainer = "container"
)

// DefaultPoolNames is the initial set of named pools.
var DefaultPoolNames = []string{
	PoolProcess,
	PoolBuild,
	PoolTest,
	PoolModel,
	PoolContainer,
}

// ErrClosed is returned when the scheduler has been shut down.
var ErrClosed = errors.New("scheduler closed")

// ErrUnknownPool is returned when Acquire names a pool that was not configured.
var ErrUnknownPool = errors.New("unknown scheduler pool")

// Config configures named pool capacities.
//
// Capacity <= 0 means unlimited (no admission wait for that pool alone).
// Empty pool names are rejected at construction. Acquire only accepts pools
// present in Pools (or the default name set when Pools is empty).
type Config struct {
	// Pools maps pool name → capacity. Nil or empty yields DefaultPoolNames
	// each with unlimited capacity. A non-empty map is the full pool set
	// (defaults are not merged in).
	Pools map[string]int
}

// Scheduler is a process-local fair named-pool admission controller.
//
// All methods are safe for concurrent use. Acquire is context-cancellable;
// canceled waiters never consume capacity. Multi-pool Acquire is atomic
// (all-or-nothing) and deadlock-free: waiters never hold partial grants.
type Scheduler struct {
	mu     sync.Mutex
	pools  map[string]*pool
	order  []string // stable snapshot order
	closed bool
	seq    uint64 // monotonic waiter / lease ids
}

type pool struct {
	name     string
	capacity int // <=0 unlimited
	inUse    int
	waiters  []*waiter // FIFO
}

// waiter is a pending multi-pool acquisition.
type waiter struct {
	id    uint64
	pools []string // sorted unique names
	// ready is closed once the waiter leaves the queues (granted, canceled, or closed).
	ready chan struct{}
	// outcome is set before ready is closed; guarded by Scheduler.mu until then.
	err   error
	lease *Lease
}

// Lease is an acquired hold on one or more pool slots.
// Release is idempotent and safe to call multiple times or after Close.
type Lease struct {
	s        *Scheduler
	id       uint64
	pools    []string
	released atomic.Bool
}

// Pools returns the pool names held by this lease (sorted, unique).
func (l *Lease) Pools() []string {
	if l == nil {
		return nil
	}
	out := make([]string, len(l.pools))
	copy(out, l.pools)
	return out
}

// Release returns held capacity. Idempotent: the first call frees slots;
// subsequent calls are no-ops.
func (l *Lease) Release() {
	if l == nil || l.s == nil {
		return
	}
	if !l.released.CompareAndSwap(false, true) {
		return
	}
	l.s.release(l)
}

// PoolSnapshot is a point-in-time view of one pool for observers / protocol.
type PoolSnapshot struct {
	Name     string `json:"name"`
	Capacity int    `json:"capacity"` // <=0 means unlimited
	InUse    int    `json:"inUse"`
	Waiting  int    `json:"waiting"`
	// Unlimited is true when Capacity <= 0.
	Unlimited bool `json:"unlimited"`
}

// Snapshot is an observer view of all pools.
type Snapshot struct {
	Pools  []PoolSnapshot `json:"pools"`
	Closed bool           `json:"closed"`
}

// New builds a Scheduler from cfg. Empty Pools installs DefaultPoolNames
// with unlimited capacity each.
func New(cfg Config) (*Scheduler, error) {
	pools := cfg.Pools
	if len(pools) == 0 {
		pools = make(map[string]int, len(DefaultPoolNames))
		for _, n := range DefaultPoolNames {
			pools[n] = 0 // unlimited
		}
	}
	order := make([]string, 0, len(pools))
	for name := range pools {
		if name == "" {
			return nil, fmt.Errorf("scheduler: empty pool name")
		}
		order = append(order, name)
	}
	slices.Sort(order)

	s := &Scheduler{
		pools: make(map[string]*pool, len(order)),
		order: order,
	}
	for _, name := range order {
		s.pools[name] = &pool{
			name:     name,
			capacity: pools[name],
		}
	}
	return s, nil
}

// NewDefault is New with unlimited default pools.
func NewDefault() *Scheduler {
	s, err := New(Config{})
	if err != nil {
		// DefaultPoolNames are non-empty constants; construction cannot fail.
		panic(err)
	}
	return s
}

// Acquire blocks until one slot is held in every named pool, ctx is done,
// or the scheduler is closed. names may contain duplicates; they are
// deduplicated. Empty names is an error. Multi-pool grants are atomic.
//
// FIFO: within each pool, waiters are admitted in arrival order. A multi-pool
// waiter is enqueued on every requested pool and is granted only when it is
// the head of each of those queues and every pool has free capacity (or is
// unlimited). Waiters never hold partial capacity, so inconsistent lock
// ordering cannot deadlock.
func (s *Scheduler) Acquire(ctx context.Context, names ...string) (*Lease, error) {
	if s == nil {
		return nil, errors.New("scheduler: nil receiver")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	need, err := normalizeNames(names)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, ErrClosed
	}
	for _, n := range need {
		if _, ok := s.pools[n]; !ok {
			s.mu.Unlock()
			return nil, fmt.Errorf("%w: %q", ErrUnknownPool, n)
		}
	}

	// Fast path: grant immediately when no one is waiting ahead and capacity allows.
	// Honor an already-canceled ctx so we never consume capacity for a dead caller.
	if err := ctx.Err(); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if s.canGrantLocked(need, nil) {
		lease := s.grantLocked(need)
		s.mu.Unlock()
		// ctx may have canceled between the check and grant; free the slot.
		if err := ctx.Err(); err != nil {
			lease.Release()
			return nil, err
		}
		return lease, nil
	}

	s.seq++
	w := &waiter{
		id:    s.seq,
		pools: need,
		ready: make(chan struct{}),
	}
	for _, n := range need {
		p := s.pools[n]
		p.waiters = append(p.waiters, w)
	}
	s.mu.Unlock()

	select {
	case <-w.ready:
		if w.err != nil {
			return nil, w.err
		}
		// Both ready and ctx.Done may be selectable; never hand out a lease
		// when the caller already canceled.
		if err := ctx.Err(); err != nil {
			w.lease.Release()
			return nil, err
		}
		return w.lease, nil
	case <-ctx.Done():
		s.cancelWaiter(w, ctx.Err())
		// cancelWaiter always settles ready (cancel, prior grant, or Close).
		<-w.ready
		if w.lease != nil {
			// Grant won the race: free capacity so a canceled caller never holds a slot.
			w.lease.Release()
			return nil, ctx.Err()
		}
		if w.err != nil {
			// Prefer Close over a racy cancel when the scheduler is gone.
			if errors.Is(w.err, ErrClosed) {
				return nil, ErrClosed
			}
			return nil, w.err
		}
		return nil, ctx.Err()
	}
}

// Snapshot returns a consistent observer view of pool state.
func (s *Scheduler) Snapshot() Snapshot {
	if s == nil {
		return Snapshot{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := Snapshot{
		Pools:  make([]PoolSnapshot, 0, len(s.order)),
		Closed: s.closed,
	}
	for _, name := range s.order {
		p := s.pools[name]
		out.Pools = append(out.Pools, PoolSnapshot{
			Name:      p.name,
			Capacity:  p.capacity,
			InUse:     p.inUse,
			Waiting:   len(p.waiters),
			Unlimited: p.capacity <= 0,
		})
	}
	return out
}

// Close shuts down the scheduler. In-flight leases remain valid until
// Release; pending waiters fail with ErrClosed; new Acquire calls fail
// with ErrClosed. Close is idempotent.
func (s *Scheduler) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	// Collect unique waiters (multi-pool waiters appear on multiple queues).
	seen := make(map[uint64]*waiter)
	for _, p := range s.pools {
		for _, w := range p.waiters {
			seen[w.id] = w
		}
		p.waiters = nil
	}
	for _, w := range seen {
		w.err = ErrClosed
		close(w.ready)
	}
}

func (s *Scheduler) release(l *Lease) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, n := range l.pools {
		p := s.pools[n]
		if p == nil {
			continue
		}
		if p.inUse > 0 {
			p.inUse--
		}
	}
	s.pumpLocked()
}

// cancelWaiter removes w from all queues if it has not been settled.
func (s *Scheduler) cancelWaiter(w *waiter, cause error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Already settled (granted or closed).
	select {
	case <-w.ready:
		return
	default:
	}
	for _, n := range w.pools {
		p := s.pools[n]
		if p == nil {
			continue
		}
		p.waiters = removeWaiter(p.waiters, w.id)
	}
	w.err = cause
	close(w.ready)
	// Removing a head waiter may unblock others.
	s.pumpLocked()
}

// canGrantLocked reports whether need can be granted now.
// If w is non-nil, w must be the head of every requested pool's waiter queue.
// If w is nil (immediate acquire), every requested pool must have an empty waiter queue.
func (s *Scheduler) canGrantLocked(need []string, w *waiter) bool {
	for _, n := range need {
		p := s.pools[n]
		if !hasCapacity(p) {
			return false
		}
		if w == nil {
			if len(p.waiters) > 0 {
				return false
			}
			continue
		}
		if len(p.waiters) == 0 || p.waiters[0].id != w.id {
			return false
		}
	}
	return true
}

func hasCapacity(p *pool) bool {
	if p.capacity <= 0 {
		return true
	}
	return p.inUse < p.capacity
}

func (s *Scheduler) grantLocked(need []string) *Lease {
	s.seq++
	lease := &Lease{
		s:     s,
		id:    s.seq,
		pools: need,
	}
	for _, n := range need {
		s.pools[n].inUse++
	}
	return lease
}

// pumpLocked grants any waiters that are runnable. Called with s.mu held.
func (s *Scheduler) pumpLocked() {
	if s.closed {
		return
	}
	// Repeat until a full pass grants nothing (granting may unblock others).
	for {
		granted := false
		// Deterministic scan order by pool name, then queue position.
		// A multi-pool waiter is only granted when head of every needed queue.
		seen := make(map[uint64]bool)
		for _, name := range s.order {
			p := s.pools[name]
			if len(p.waiters) == 0 {
				continue
			}
			w := p.waiters[0]
			if seen[w.id] {
				continue
			}
			seen[w.id] = true
			if !s.canGrantLocked(w.pools, w) {
				continue
			}
			// Remove from all queues before granting.
			for _, n := range w.pools {
				s.pools[n].waiters = removeWaiter(s.pools[n].waiters, w.id)
			}
			lease := s.grantLocked(w.pools)
			w.lease = lease
			close(w.ready)
			granted = true
		}
		if !granted {
			return
		}
	}
}

func removeWaiter(q []*waiter, id uint64) []*waiter {
	for i, w := range q {
		if w.id == id {
			return append(q[:i], q[i+1:]...)
		}
	}
	return q
}

func normalizeNames(names []string) ([]string, error) {
	if len(names) == 0 {
		return nil, errors.New("scheduler: at least one pool name required")
	}
	seen := make(map[string]struct{}, len(names))
	out := make([]string, 0, len(names))
	for _, n := range names {
		if n == "" {
			return nil, errors.New("scheduler: empty pool name")
		}
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		out = append(out, n)
	}
	slices.Sort(out)
	return out, nil
}
