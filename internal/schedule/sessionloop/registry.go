// Package sessionloop is the daemon side of /loop (#1588): session-scoped,
// ephemeral recurring jobs that re-inject a prompt into one live session on
// an interval until stopped or the session closes.
//
// It is deliberately not internal/schedule's persisted clock: loops never
// survive a daemon restart or session close and never start new sessions.
// Grammar and limits come from pkg/loopspec, shared with the TUI's /loop.
package sessionloop

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/jonathanung/strike/pkg/loopspec"
)

var (
	// ErrInvalid wraps interval/job grammar errors.
	ErrInvalid = errors.New("invalid loop")
	// ErrLimit reports MaxPerSession concurrent loops.
	ErrLimit = errors.New("too many loops")
	// ErrNotFound reports an unknown loop id.
	ErrNotFound = errors.New("loop not found")
	// ErrClosed reports a registry whose session has ended.
	ErrClosed = errors.New("session closed")
)

// submitTimeout bounds one firing's hand-off to the session.
const submitTimeout = 5 * time.Second

// SubmitFunc injects job into the session as a user prompt. It must queue
// behind a busy turn rather than interleave (the engine's UserInput FIFO).
type SubmitFunc func(ctx context.Context, job string) error

// Loop is a host-safe snapshot of one active loop.
type Loop struct {
	ID         string    `json:"id"`
	Interval   string    `json:"interval"`
	IntervalMs int64     `json:"intervalMs"`
	Job        string    `json:"job"`
	Runs       int       `json:"runs"`
	CreatedAt  time.Time `json:"createdAt"`
	NextAt     time.Time `json:"nextAt"`
	LastRunAt  time.Time `json:"lastRunAt,omitempty"`
	LastError  string    `json:"lastError,omitempty"`
}

type timer interface{ Stop() bool }

type entry struct {
	Loop
	interval time.Duration
	gen      int
	timer    timer
}

// Registry holds one session's loops. The zero value is not usable; call New.
type Registry struct {
	submit SubmitFunc
	now    func() time.Time
	after  func(time.Duration, func()) timer

	mu     sync.Mutex
	seq    int
	loops  map[string]*entry
	closed bool
}

// New returns an empty registry that fires through submit.
func New(submit SubmitFunc) *Registry {
	return &Registry{
		submit: submit,
		now:    time.Now,
		after:  func(d time.Duration, f func()) timer { return time.AfterFunc(d, f) },
		loops:  make(map[string]*entry),
	}
}

// Start parses interval and job with the shared /loop grammar and arms a
// new loop. Ids are l1, l2, … per registry, never reused.
func (r *Registry) Start(interval, job string) (Loop, error) {
	d, err := loopspec.ParseInterval(interval)
	if err != nil {
		return Loop{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	job, err = loopspec.ValidateJob(job)
	if err != nil {
		return Loop{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return Loop{}, ErrClosed
	}
	if len(r.loops) >= loopspec.MaxPerSession {
		return Loop{}, fmt.Errorf("%w: at most %d concurrent loops", ErrLimit, loopspec.MaxPerSession)
	}
	r.seq++
	now := r.now()
	e := &entry{
		Loop: Loop{
			ID:         "l" + strconv.Itoa(r.seq),
			Interval:   loopspec.FormatInterval(d),
			IntervalMs: d.Milliseconds(),
			Job:        job,
			CreatedAt:  now,
		},
		interval: d,
		gen:      1,
	}
	r.loops[e.ID] = e
	r.armLocked(e, now)
	return e.Loop, nil
}

// List returns active loops ordered by id sequence.
func (r *Registry) List() []Loop {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Loop, 0, len(r.loops))
	for _, e := range r.loops {
		out = append(out, e.Loop)
	}
	sort.Slice(out, func(i, j int) bool { return idSeq(out[i].ID) < idSeq(out[j].ID) })
	return out
}

// Len reports the number of active loops.
func (r *Registry) Len() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.loops)
}

// Stop cancels one loop. A firing already handed to the session is not
// recalled; no further firings happen.
func (r *Registry) Stop(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.loops[id]
	if !ok {
		return ErrNotFound
	}
	r.dropLocked(e)
	return nil
}

// StopAll cancels every loop and returns how many were active.
func (r *Registry) StopAll() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := len(r.loops)
	for _, e := range r.loops {
		r.dropLocked(e)
	}
	return n
}

// Close stops every loop and rejects later Starts. Called when the session
// ends; safe to call more than once.
func (r *Registry) Close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	for _, e := range r.loops {
		r.dropLocked(e)
	}
}

func (r *Registry) dropLocked(e *entry) {
	e.gen++
	if e.timer != nil {
		e.timer.Stop()
	}
	delete(r.loops, e.ID)
}

func (r *Registry) armLocked(e *entry, from time.Time) {
	e.NextAt = from.Add(e.interval)
	id, gen := e.ID, e.gen
	e.timer = r.after(e.interval, func() { r.fire(id, gen) })
}

// fire injects the job when (id, gen) is still live, then re-arms. The
// submit call runs outside the lock so a slow session cannot block List.
func (r *Registry) fire(id string, gen int) {
	r.mu.Lock()
	e, ok := r.loops[id]
	if !ok || e.gen != gen || r.closed {
		r.mu.Unlock()
		return
	}
	job := e.Job
	r.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), submitTimeout)
	err := r.submit(ctx, job)
	cancel()

	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok = r.loops[id]
	if !ok || e.gen != gen || r.closed {
		return
	}
	now := r.now()
	e.LastRunAt = now
	if err != nil {
		e.LastError = err.Error()
	} else {
		e.Runs++
		e.LastError = ""
	}
	r.armLocked(e, now)
}

func idSeq(id string) int {
	n, _ := strconv.Atoi(id[min(1, len(id)):])
	return n
}
