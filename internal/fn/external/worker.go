package external

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jonathanung/strike-cli/internal/fn"
	"github.com/jonathanung/strike-cli/internal/provider"
)

// Default persistent-worker policy (overridable via WorkerOptions / config).
const (
	defaultMaxConcurrent = 1
	defaultIdleTimeout   = 60 * time.Second
	defaultMaxRestarts   = 3
	readyWait            = 2 * time.Second
)

// WorkerOptions configures a persistent external harness worker.
// Zero values select defaults; negative IdleTimeout disables idle eviction;
// negative MaxRestarts allows unlimited crash restarts.
type WorkerOptions struct {
	MaxConcurrent int
	IdleTimeout   time.Duration
	MaxRestarts   int
}

// Normalize applies defaults used by NewPersistent and config wiring.
func (o WorkerOptions) Normalize() WorkerOptions {
	if o.MaxConcurrent <= 0 {
		o.MaxConcurrent = defaultMaxConcurrent
	}
	if o.IdleTimeout == 0 {
		o.IdleTimeout = defaultIdleTimeout
	}
	if o.MaxRestarts == 0 {
		o.MaxRestarts = defaultMaxRestarts
	}
	return o
}

// pool owns at most one live subprocess and multiplexes invocations onto it.
type pool struct {
	name    string
	adapter Adapter
	opts    WorkerOptions

	slots chan struct{}

	mu       sync.Mutex
	closed   bool
	disabled error
	restarts int
	live     *liveWorker
}

// NewPersistent returns a fn.Func that reuses one subprocess across
// invocations (when the worker implements the persistent protocol), plus a
// Close that shuts the worker down. One-shot New remains the default path.
func NewPersistent(name string, adapter Adapter, opts WorkerOptions) (fn.Func, func() error, error) {
	if strings.TrimSpace(name) == "" || adapter == nil {
		return nil, nil, errors.New("external harness: name and adapter are required")
	}
	opts = opts.Normalize()
	p := &pool{
		name:    name,
		adapter: adapter,
		opts:    opts,
		slots:   make(chan struct{}, opts.MaxConcurrent),
	}
	for i := 0; i < opts.MaxConcurrent; i++ {
		p.slots <- struct{}{}
	}
	return p.run, p.Close, nil
}

// Close shuts down the live worker (best-effort graceful shutdown) and
// rejects further invocations.
func (p *pool) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	live := p.live
	// Leave p.live set until shutdown retires it so concurrent failAll can see stopping.
	p.mu.Unlock()
	if live != nil {
		live.shutdown()
	}
	return nil
}

func (p *pool) run(input fn.Input, prov fn.Provider, emit fn.Emit) (fn.Result, error) {
	ctx := input.Context
	if ctx == nil {
		ctx = context.Background()
	}

	select {
	case <-p.slots:
	case <-ctx.Done():
		return fn.Result{}, ctx.Err()
	}
	defer func() { p.slots <- struct{}{} }()

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return fn.Result{}, errors.New("external harness: worker pool closed")
	}
	if p.disabled != nil {
		err := p.disabled
		p.mu.Unlock()
		return fn.Result{}, err
	}
	p.mu.Unlock()

	live, err := p.ensureLive(ctx)
	if err != nil {
		return fn.Result{}, err
	}
	return live.invoke(ctx, input, prov, emit)
}

func (p *pool) ensureLive(ctx context.Context) (*liveWorker, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, errors.New("external harness: worker pool closed")
	}
	if p.disabled != nil {
		return nil, p.disabled
	}
	if p.live != nil {
		if p.live.healthy() {
			return p.live, nil
		}
		// Reader may still be finalizing; retire exactly once as a crash.
		p.retireLocked(p.live, true, p.live.deadErr)
	}
	if p.disabled != nil {
		return nil, p.disabled
	}
	if p.opts.MaxRestarts >= 0 && p.restarts > p.opts.MaxRestarts {
		p.disabled = fmt.Errorf("external harness %q: disabled after %d restarts", p.name, p.opts.MaxRestarts)
		return nil, p.disabled
	}

	lw, err := startLiveWorker(ctx, p)
	if err != nil {
		p.restarts++
		if p.opts.MaxRestarts >= 0 && p.restarts > p.opts.MaxRestarts {
			p.disabled = fmt.Errorf("external harness %q: disabled after start failures: %w", p.name, err)
			return nil, p.disabled
		}
		return nil, fmt.Errorf("start external harness %q: %w", p.name, err)
	}
	p.live = lw
	return lw, nil
}

// retireLocked detaches lw from the pool at most once.
// crash=true counts toward MaxRestarts / disable policy.
// Caller must hold p.mu.
func (p *pool) retireLocked(lw *liveWorker, crash bool, cause error) {
	if lw == nil || lw.retired {
		return
	}
	lw.retired = true
	if p.live == lw {
		p.live = nil
	}
	if !crash {
		return
	}
	p.restarts++
	if p.opts.MaxRestarts >= 0 && p.restarts > p.opts.MaxRestarts {
		if cause == nil {
			cause = errors.New("worker crashed")
		}
		p.disabled = fmt.Errorf("external harness %q: disabled after %d restarts: %w", p.name, p.opts.MaxRestarts, cause)
	}
}

// noteCrash clears the live worker and applies restart/disable policy.
// caller must not hold p.mu.
func (p *pool) noteCrash(lw *liveWorker, cause error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.retireLocked(lw, true, cause)
}

type liveWorker struct {
	pool *pool
	pipe Pipe

	writeMu sync.Mutex

	mu          sync.Mutex
	invocations map[string]*invocation
	dead        bool
	deadErr     error
	processDone chan error
	idleTimer   *time.Timer
	stopping    bool
	// retired is set under pool.mu when detached from the pool (once).
	retired bool
}

type invocation struct {
	id     string
	ctx    context.Context
	cancel context.CancelFunc
	p      fn.Provider
	tools  fn.Tools
	emit   fn.Emit
	ids    map[string]string
	total  int
	done   chan terminal
}

func startLiveWorker(ctx context.Context, p *pool) (*liveWorker, error) {
	// Detach process lifetime from any single invocation context.
	startCtx := context.Background()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	pipe, err := p.adapter.Start(startCtx)
	if err != nil {
		return nil, err
	}
	lw := &liveWorker{
		pool:        p,
		pipe:        pipe,
		invocations: make(map[string]*invocation),
		processDone: make(chan error, 1),
	}
	go func() { lw.processDone <- pipe.Wait() }()
	go lw.readLoop()
	lw.armIdleLocked() // safe: no concurrent access yet
	// Optional readiness: workers may emit harness.ready; we do not block on it.
	// Process start success is the readiness gate; health is observed via I/O.
	_ = readyWait
	return lw, nil
}

func (lw *liveWorker) healthy() bool {
	lw.mu.Lock()
	defer lw.mu.Unlock()
	return !lw.dead && !lw.stopping
}

func (lw *liveWorker) write(v any) error {
	lw.writeMu.Lock()
	defer lw.writeMu.Unlock()
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(b) > maxLineBytes {
		return errors.New("external harness: outbound line exceeds limit")
	}
	_, err = lw.pipe.Write(append(b, '\n'))
	return err
}

func (lw *liveWorker) invoke(parent context.Context, input fn.Input, prov fn.Provider, emit fn.Emit) (fn.Result, error) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	invocationID := rand.Text()
	inv := &invocation{
		id:     invocationID,
		ctx:    ctx,
		cancel: cancel,
		p:      prov,
		tools:  input.Tools,
		emit:   emit,
		ids:    make(map[string]string),
		done:   make(chan terminal, 1),
	}

	lw.mu.Lock()
	if lw.dead {
		err := lw.deadErr
		lw.mu.Unlock()
		if err == nil {
			err = errors.New("external harness: worker is dead")
		}
		return fn.Result{}, err
	}
	if lw.stopping {
		lw.mu.Unlock()
		return fn.Result{}, errors.New("external harness: worker is shutting down")
	}
	lw.disarmIdleLocked()
	lw.invocations[invocationID] = inv
	lw.mu.Unlock()

	defer func() {
		lw.mu.Lock()
		delete(lw.invocations, invocationID)
		if len(lw.invocations) == 0 && !lw.dead && !lw.stopping {
			lw.armIdleLocked()
		}
		lw.mu.Unlock()
	}()

	start := struct {
		Version      int         `json:"version"`
		Type         string      `json:"type"`
		InvocationID string      `json:"invocationId"`
		Request      wireRequest `json:"request"`
		Capabilities []string    `json:"capabilities"`
	}{
		1, "harness.start", invocationID, toWire(input.Request),
		[]string{"provider.call", "tool.execute", "progress.emit", "harness.cancel", "persistent"},
	}
	if err := lw.write(start); err != nil {
		lw.failAll(fmt.Errorf("external harness: write start: %w", err))
		return fn.Result{}, err
	}

	select {
	case t := <-inv.done:
		return t.result, t.err
	case <-ctx.Done():
		ctxErr := ctx.Err()
		_ = lw.write(struct {
			Version      int    `json:"version"`
			Type         string `json:"type"`
			InvocationID string `json:"invocationId"`
			Reason       string `json:"reason"`
		}{1, "harness.cancel", invocationID, ctxErr.Error()})
		// Isolated cancel: wait for this invocation only; kill process if stuck.
		select {
		case t := <-inv.done:
			if t.err != nil {
				return fn.Result{}, ctxErr
			}
			return t.result, ctxErr
		case <-time.After(cancelGrace):
			lw.failAll(fmt.Errorf("external harness: cancel grace exceeded for %s", invocationID))
			return fn.Result{}, ctxErr
		}
	}
}

func (lw *liveWorker) readLoop() {
	s := bufio.NewScanner(lw.pipe)
	s.Buffer(make([]byte, 64*1024), maxLineBytes)
	for s.Scan() {
		lineLen := len(s.Bytes()) + 1
		var m envelope
		if err := json.Unmarshal(s.Bytes(), &m); err != nil {
			lw.failAll(fmt.Errorf("external harness: malformed JSON: %w", err))
			return
		}
		if m.Version != 1 {
			lw.failAll(fmt.Errorf("external harness: invalid version or invocationId"))
			return
		}
		// Process-level control messages (no invocation).
		switch m.Type {
		case "harness.ready":
			continue
		}
		if m.InvocationID == "" {
			lw.failAll(fmt.Errorf("external harness: invalid version or invocationId"))
			return
		}
		lw.mu.Lock()
		inv := lw.invocations[m.InvocationID]
		if inv != nil {
			inv.total += lineLen
			if inv.total > maxOutputBytes {
				lw.mu.Unlock()
				lw.failAll(errors.New("external harness: output exceeds limit"))
				return
			}
		}
		lw.mu.Unlock()
		if inv == nil {
			// Stale or unknown invocation — protocol violation for persistent workers.
			lw.failAll(fmt.Errorf("external harness: unknown invocationId %q", m.InvocationID))
			return
		}
		if !lw.dispatch(inv, m) {
			return
		}
	}
	if err := s.Err(); err != nil {
		lw.failAll(fmt.Errorf("external harness: read: %w", err))
		return
	}
	// EOF
	select {
	case waitErr := <-lw.processDone:
		if waitErr != nil {
			lw.failAll(fmt.Errorf("external harness exit: %w", waitErr))
			return
		}
	default:
	}
	lw.failAll(errors.New("external harness exited without harness.complete or harness.error"))
}

// dispatch handles one inbound message for inv. Returns false when the worker
// should stop reading (fatal protocol error already reported via failAll).
func (lw *liveWorker) dispatch(inv *invocation, m envelope) bool {
	switch m.Type {
	case "provider.call":
		if m.CallID == "" || m.Request == nil {
			lw.finishInv(inv, terminal{err: errors.New("external harness: provider.call requires callId and request")})
			return true
		}
		if previous := inv.ids[m.CallID]; previous != "" {
			lw.failAll(fmt.Errorf("external harness: duplicate request ID %q (already used by %s)", m.CallID, previous))
			return false
		}
		inv.ids[m.CallID] = m.Type
		go func() {
			relayProvider(inv.ctx, inv.p.Call, inv.id, m.CallID, m.Request.providerRequest(), lw.write)
		}()
		return true
	case "tool.execute":
		if m.CallID == "" {
			lw.finishInv(inv, terminal{err: errors.New("external harness: tool.execute requires callId")})
			return true
		}
		if previous := inv.ids[m.CallID]; previous != "" {
			lw.failAll(fmt.Errorf("external harness: duplicate request ID %q (already used by %s)", m.CallID, previous))
			return false
		}
		inv.ids[m.CallID] = m.Type
		call := provider.ToolCall{
			ID:   m.ToolCallID,
			Name: m.Name,
			Args: m.Arguments,
		}
		if call.ID == "" {
			call.ID = m.CallID
		}
		go func() {
			relayTool(inv.ctx, inv.tools.Execute, inv.id, m.CallID, call, lw.write)
		}()
		return true
	case "progress.emit":
		payload := m.Payload
		if len(payload) == 0 {
			payload = m.Message
		}
		if len(payload) == 0 {
			payload = json.RawMessage(`{}`)
		}
		if inv.emit != nil {
			inv.emit(payload)
		}
		return true
	case "harness.complete":
		result := fn.Result{Text: m.Text, Reasoning: m.Reasoning, StopReason: m.StopReason}
		for _, c := range m.ToolCalls {
			result.Calls = append(result.Calls, provider.ToolCall{ID: c.ID, Name: c.Name, Args: c.Args})
		}
		lw.finishInv(inv, terminal{result: result})
		return true
	case "harness.error":
		msg := m.ErrorText
		if msg == "" {
			msg = string(m.Message)
		}
		lw.finishInv(inv, terminal{err: fmt.Errorf("external harness %q: %s: %s", lw.pool.name, m.Code, msg)})
		return true
	default:
		lw.failAll(fmt.Errorf("external harness: unknown message type %q", m.Type))
		return false
	}
}

func (lw *liveWorker) finishInv(inv *invocation, t terminal) {
	select {
	case inv.done <- t:
	default:
	}
	inv.cancel()
}

func (lw *liveWorker) failAll(err error) {
	lw.mu.Lock()
	if lw.dead {
		lw.mu.Unlock()
		return
	}
	lw.dead = true
	lw.deadErr = err
	stopping := lw.stopping
	invs := make([]*invocation, 0, len(lw.invocations))
	for _, inv := range lw.invocations {
		invs = append(invs, inv)
	}
	if lw.idleTimer != nil {
		lw.idleTimer.Stop()
		lw.idleTimer = nil
	}
	lw.mu.Unlock()

	for _, inv := range invs {
		lw.finishInv(inv, terminal{err: err})
	}
	_ = lw.pipe.Kill()
	select {
	case <-lw.processDone:
	case <-time.After(cancelGrace):
		_ = lw.pipe.Kill()
		<-lw.processDone
	}
	// Intentional shutdown/idle eviction must not burn restart budget when the
	// reader observes EOF during teardown.
	if stopping {
		lw.pool.mu.Lock()
		lw.pool.retireLocked(lw, false, err)
		lw.pool.mu.Unlock()
		return
	}
	lw.pool.noteCrash(lw, err)
}

func (lw *liveWorker) armIdleLocked() {
	if lw.pool.opts.IdleTimeout < 0 {
		return
	}
	if lw.idleTimer != nil {
		lw.idleTimer.Stop()
	}
	timeout := lw.pool.opts.IdleTimeout
	lw.idleTimer = time.AfterFunc(timeout, func() {
		lw.idleEvict()
	})
}

func (lw *liveWorker) disarmIdleLocked() {
	if lw.idleTimer != nil {
		lw.idleTimer.Stop()
		lw.idleTimer = nil
	}
}

func (lw *liveWorker) idleEvict() {
	lw.mu.Lock()
	if lw.dead || lw.stopping || len(lw.invocations) > 0 {
		lw.mu.Unlock()
		return
	}
	lw.stopping = true
	lw.mu.Unlock()
	lw.shutdown()
}

func (lw *liveWorker) shutdown() {
	lw.mu.Lock()
	if lw.dead {
		lw.mu.Unlock()
		return
	}
	lw.stopping = true
	lw.disarmIdleLocked()
	// Cancel in-flight so callers unblock; then kill.
	invs := make([]*invocation, 0, len(lw.invocations))
	for _, inv := range lw.invocations {
		invs = append(invs, inv)
	}
	lw.mu.Unlock()

	_ = lw.write(struct {
		Version int    `json:"version"`
		Type    string `json:"type"`
	}{1, "harness.shutdown"})
	_ = lw.pipe.CloseWrite()

	deadline := time.After(cancelGrace)
	select {
	case <-lw.processDone:
	case <-deadline:
		_ = lw.pipe.Kill()
		<-lw.processDone
	}

	lw.mu.Lock()
	lw.dead = true
	if lw.deadErr == nil {
		lw.deadErr = errors.New("external harness: worker shut down")
	}
	lw.mu.Unlock()

	for _, inv := range invs {
		lw.finishInv(inv, terminal{err: errors.New("external harness: worker shut down")})
	}

	// Idle/graceful shutdown does not count as a crash restart.
	lw.pool.mu.Lock()
	lw.pool.retireLocked(lw, false, nil)
	lw.pool.mu.Unlock()
}
