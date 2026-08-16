package engine_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/harness/engine"
	"github.com/jonathanung/strike-cli/harness/permission"
	"github.com/jonathanung/strike-cli/harness/provider"
	"github.com/jonathanung/strike-cli/harness/scheduler"
	"github.com/jonathanung/strike-cli/harness/tool"
	"github.com/jonathanung/strike-cli/pkg/protocol"
)

// concurrencyProbeProvider tracks aggregate in-flight Stream calls and can
// block until released so admission limits are observable.
type concurrencyProbeProvider struct {
	mu        sync.Mutex
	active    int
	maxActive int
	calls     int
	// entered is closed once on the first Stream entry (optional signal).
	enteredOnce sync.Once
	entered     chan struct{}
	// hold, when non-nil, blocks the stream until closed or ctx done.
	hold <-chan struct{}
	// err is returned from Stream without opening a channel when non-nil.
	err error
	// incomplete closes the channel with no terminal event.
	incomplete bool
	// events are emitted when hold is nil/closed and err is nil.
	events []provider.StreamEvent
}

func newConcurrencyProbeProvider() *concurrencyProbeProvider {
	return &concurrencyProbeProvider{
		entered: make(chan struct{}),
		events: []provider.StreamEvent{
			{Type: provider.EventTextDelta, Text: "ok"},
			{Type: provider.EventDone, StopReason: "end_turn"},
		},
	}
}

func (p *concurrencyProbeProvider) Name() string { return "probe" }

func (p *concurrencyProbeProvider) Stream(ctx context.Context, _ provider.Request) (<-chan provider.StreamEvent, error) {
	p.mu.Lock()
	p.calls++
	p.active++
	if p.active > p.maxActive {
		p.maxActive = p.active
	}
	hold := p.hold
	err := p.err
	incomplete := p.incomplete
	events := append([]provider.StreamEvent(nil), p.events...)
	p.mu.Unlock()
	p.enteredOnce.Do(func() { close(p.entered) })

	if err != nil {
		p.leave()
		return nil, err
	}

	ch := make(chan provider.StreamEvent)
	go func() {
		defer close(ch)
		defer p.leave()
		if hold != nil {
			select {
			case <-hold:
			case <-ctx.Done():
				return
			}
		}
		if incomplete {
			return
		}
		for _, ev := range events {
			select {
			case ch <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}

func (p *concurrencyProbeProvider) leave() {
	p.mu.Lock()
	p.active--
	p.mu.Unlock()
}

func (p *concurrencyProbeProvider) snapshot() (calls, active, maxActive int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls, p.active, p.maxActive
}

func (p *concurrencyProbeProvider) setHold(hold <-chan struct{}) {
	p.mu.Lock()
	p.hold = hold
	p.mu.Unlock()
}

func (p *concurrencyProbeProvider) setErr(err error) {
	p.mu.Lock()
	p.err = err
	p.mu.Unlock()
}

func modelSched(t *testing.T, modelCap int) *scheduler.Scheduler {
	t.Helper()
	s, err := scheduler.New(scheduler.Config{
		Pools: map[string]int{
			scheduler.PoolProcess:   0,
			scheduler.PoolBuild:     0,
			scheduler.PoolTest:      0,
			scheduler.PoolModel:     modelCap,
			scheduler.PoolContainer: 0,
		},
	})
	if err != nil {
		t.Fatalf("scheduler.New: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

func modelPoolSnap(t *testing.T, s *scheduler.Scheduler) scheduler.PoolSnapshot {
	t.Helper()
	for _, p := range s.Snapshot().Pools {
		if p.Name == scheduler.PoolModel {
			return p
		}
	}
	t.Fatal("model pool missing from snapshot")
	return scheduler.PoolSnapshot{}
}

func waitModelWaiting(t *testing.T, s *scheduler.Scheduler, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if modelPoolSnap(t, s).Waiting >= n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for model waiting=%d; snap=%+v", n, modelPoolSnap(t, s))
}

func waitModelInUse(t *testing.T, s *scheduler.Scheduler, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if modelPoolSnap(t, s).InUse == n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for model inUse=%d; snap=%+v", n, modelPoolSnap(t, s))
}

func startProbeEngine(t *testing.T, id string, prov provider.Provider, sched *scheduler.Scheduler, opts ...func(*engine.Options)) *engine.Engine {
	t.Helper()
	o := engine.Options{
		SessionID:       id,
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "probe",
		Registry:        tool.NewRegistry(),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
		Scheduler:       sched,
	}
	for _, fn := range opts {
		fn(&o)
	}
	eng := engine.New(o)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go eng.Run(ctx)
	return eng
}

func waitTurnDone(t *testing.T, eng *engine.Engine, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case ev := <-eng.Events():
			switch ev := ev.(type) {
			case protocol.TurnCompleted:
				return
			case protocol.EngineError:
				// Some failure paths still complete the turn; keep draining
				// until TurnCompleted or timeout.
				_ = ev
			}
		case <-deadline:
			t.Fatal("timed out waiting for TurnCompleted")
		}
	}
}

func TestModelPoolCapsConcurrentStreams(t *testing.T) {
	sched := modelSched(t, 1)
	prov := newConcurrencyProbeProvider()
	hold := make(chan struct{})
	prov.setHold(hold)

	engA := startProbeEngine(t, "root-a", prov, sched)
	engB := startProbeEngine(t, "root-b", prov, sched)

	engA.Ops() <- protocol.UserInput{Text: "a"}
	select {
	case <-prov.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("first stream never entered provider")
	}
	waitModelInUse(t, sched, 1)

	engB.Ops() <- protocol.UserInput{Text: "b"}
	waitModelWaiting(t, sched, 1)

	// Second must still be queued: only one provider call so far.
	if calls, _, max := prov.snapshot(); calls != 1 || max != 1 {
		t.Fatalf("while queued: calls=%d maxActive=%d, want 1/1", calls, max)
	}

	close(hold)
	waitTurnDone(t, engA, 5*time.Second)
	waitTurnDone(t, engB, 5*time.Second)

	calls, active, max := prov.snapshot()
	if active != 0 {
		t.Fatalf("active=%d after both turns, want 0", active)
	}
	if calls != 2 {
		t.Fatalf("calls=%d, want 2", calls)
	}
	if max != 1 {
		t.Fatalf("maxActive=%d, want 1 (model limit)", max)
	}
	if snap := modelPoolSnap(t, sched); snap.InUse != 0 || snap.Waiting != 0 {
		t.Fatalf("pool not idle after turns: %+v", snap)
	}
}

func TestModelPoolCanceledQueueNeverCallsProvider(t *testing.T) {
	sched := modelSched(t, 1)
	prov := newConcurrencyProbeProvider()
	hold := make(chan struct{})
	prov.setHold(hold)

	engA := startProbeEngine(t, "hold-root", prov, sched)
	engB := startProbeEngine(t, "cancel-root", prov, sched)

	engA.Ops() <- protocol.UserInput{Text: "hold"}
	select {
	case <-prov.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("holder never entered provider")
	}
	waitModelInUse(t, sched, 1)

	// engB's turn context is the engine turn ctx; Interrupt cancels it while queued.
	engB.Ops() <- protocol.UserInput{Text: "should-not-run"}
	waitModelWaiting(t, sched, 1)

	engB.Ops() <- protocol.Interrupt{}
	// Wait until the waiter is gone (canceled) while A still holds the slot.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		snap := modelPoolSnap(t, sched)
		if snap.Waiting == 0 && snap.InUse == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if snap := modelPoolSnap(t, sched); snap.Waiting != 0 || snap.InUse != 1 {
		t.Fatalf("after cancel: %+v, want waiting=0 inUse=1", snap)
	}
	if calls, _, _ := prov.snapshot(); calls != 1 {
		t.Fatalf("provider calls=%d, want 1 (canceled waiter must not Stream)", calls)
	}

	close(hold)
	waitTurnDone(t, engA, 5*time.Second)
	// engB may emit EngineError/TurnCompleted from interrupt; drain briefly.
	drainDeadline := time.After(500 * time.Millisecond)
drainB:
	for {
		select {
		case <-engB.Events():
		case <-drainDeadline:
			break drainB
		}
	}
	if calls, _, max := prov.snapshot(); calls != 1 || max != 1 {
		t.Fatalf("final calls=%d maxActive=%d, want 1/1", calls, max)
	}
}

func TestModelPoolStreamErrorReleasesLease(t *testing.T) {
	sched := modelSched(t, 1)
	prov := newConcurrencyProbeProvider()
	prov.setErr(errors.New("boom"))

	eng := startProbeEngine(t, "err-root", prov, sched,
		func(o *engine.Options) {
			o.MaxStreamAttempts = 1
			o.StreamRetryBackoff = func(int) time.Duration { return 0 }
		},
	)
	eng.Ops() <- protocol.UserInput{Text: "fail"}

	// Turn should fail; lease must be free for a subsequent acquire.
	deadline := time.After(5 * time.Second)
	sawErr := false
	for !sawErr {
		select {
		case ev := <-eng.Events():
			if _, ok := ev.(protocol.EngineError); ok {
				sawErr = true
			}
			if _, ok := ev.(protocol.TurnCompleted); ok {
				sawErr = true
			}
		case <-deadline:
			t.Fatal("timed out waiting for failed turn")
		}
	}
	waitModelInUse(t, sched, 0)

	// Immediate acquire proves the slot was released.
	lease, err := sched.Acquire(context.Background(), scheduler.PoolModel)
	if err != nil {
		t.Fatalf("Acquire after error: %v", err)
	}
	lease.Release()
}

func TestModelPoolIncompleteStreamReleasesLease(t *testing.T) {
	sched := modelSched(t, 1)
	prov := newConcurrencyProbeProvider()
	prov.mu.Lock()
	prov.incomplete = true
	prov.mu.Unlock()

	// First attempt incomplete (retryable), second succeeds — lease free between.
	var sawZeroDuringRetry atomic.Bool
	eng := startProbeEngine(t, "incomplete-root", prov, sched,
		func(o *engine.Options) {
			o.MaxStreamAttempts = 2
			o.StreamRetryBackoff = func(int) time.Duration {
				// Observe pool while backing off before attempt 2.
				if modelPoolSnap(t, sched).InUse == 0 {
					sawZeroDuringRetry.Store(true)
				}
				// Flip to a complete stream for the next attempt before it starts.
				prov.mu.Lock()
				prov.incomplete = false
				prov.events = []provider.StreamEvent{
					{Type: provider.EventTextDelta, Text: "recovered"},
					{Type: provider.EventDone, StopReason: "end_turn"},
				}
				prov.mu.Unlock()
				return 0
			}
		},
	)

	eng.Ops() <- protocol.UserInput{Text: "retry-incomplete"}
	waitTurnDone(t, eng, 5*time.Second)

	if !sawZeroDuringRetry.Load() {
		t.Fatal("model lease was held during retry backoff after incomplete stream")
	}
	if calls, _, _ := prov.snapshot(); calls != 2 {
		t.Fatalf("calls=%d, want 2", calls)
	}
	if snap := modelPoolSnap(t, sched); snap.InUse != 0 {
		t.Fatalf("lease leaked: %+v", snap)
	}
}

func TestModelPoolRetryReleasesDuringBackoff(t *testing.T) {
	sched := modelSched(t, 1)
	var inUseDuringBackoff atomic.Int32
	inUseDuringBackoff.Store(-1)

	prov := &scriptedProvider{steps: []streamStep{
		{err: errors.New("unexpected status 429: rate limited")},
		{events: []provider.StreamEvent{
			{Type: provider.EventTextDelta, Text: "ok"},
			{Type: provider.EventDone, StopReason: "end_turn"},
		}},
	}}

	eng := startProbeEngine(t, "retry-root", prov, sched,
		func(o *engine.Options) {
			o.StreamRetryBackoff = func(int) time.Duration {
				inUseDuringBackoff.Store(int32(modelPoolSnap(t, sched).InUse))
				return 20 * time.Millisecond
			}
		},
	)
	eng.Ops() <- protocol.UserInput{Text: "hi"}
	waitTurnDone(t, eng, 5*time.Second)

	if got := inUseDuringBackoff.Load(); got != 0 {
		t.Fatalf("inUse during backoff = %d, want 0", got)
	}
	if prov.callCount() != 2 {
		t.Fatalf("calls=%d, want 2", prov.callCount())
	}
	if snap := modelPoolSnap(t, sched); snap.InUse != 0 {
		t.Fatalf("lease leaked: %+v", snap)
	}
}

func TestModelPoolCancelMidStreamReleasesLease(t *testing.T) {
	sched := modelSched(t, 1)
	prov := newConcurrencyProbeProvider()
	hold := make(chan struct{})
	prov.setHold(hold)

	eng := startProbeEngine(t, "mid-cancel", prov, sched)
	eng.Ops() <- protocol.UserInput{Text: "stream"}
	select {
	case <-prov.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("stream never started")
	}
	waitModelInUse(t, sched, 1)

	eng.Ops() <- protocol.Interrupt{}
	// Provider unblocks on ctx cancel; drain releases lease.
	waitModelInUse(t, sched, 0)

	lease, err := sched.Acquire(context.Background(), scheduler.PoolModel)
	if err != nil {
		t.Fatalf("Acquire after mid-stream cancel: %v", err)
	}
	lease.Release()
	close(hold) // unblock if still waiting
}

func TestModelPoolNilSchedulerUnlimited(t *testing.T) {
	prov := newConcurrencyProbeProvider()
	hold := make(chan struct{})
	prov.setHold(hold)

	// No Scheduler: both streams may enter the provider concurrently.
	engA := startProbeEngine(t, "u-a", prov, nil)
	engB := startProbeEngine(t, "u-b", prov, nil)

	engA.Ops() <- protocol.UserInput{Text: "a"}
	engB.Ops() <- protocol.UserInput{Text: "b"}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, active, max := prov.snapshot()
		if active >= 2 || max >= 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	_, _, max := prov.snapshot()
	if max < 2 {
		t.Fatalf("maxActive=%d, want >=2 without scheduler", max)
	}
	close(hold)
	waitTurnDone(t, engA, 5*time.Second)
	waitTurnDone(t, engB, 5*time.Second)
}

func TestModelPoolSharedWithChildTurn(t *testing.T) {
	sched := modelSched(t, 1)
	// Parent holds the model slot; child spawn that needs a stream must wait.
	prov := newConcurrencyProbeProvider()
	hold := make(chan struct{})
	prov.setHold(hold)

	// Parent turn that blocks in Stream; we only need the shared scheduler
	// visible on a child Options path — spawnChild copies Scheduler.
	// Use two root engines as a stand-in for parent+child sharing (child
	// inheritance is covered by Options copy + this shared-cap test).
	engA := startProbeEngine(t, "share-a", prov, sched)
	engB := startProbeEngine(t, "share-b", prov, sched)

	engA.Ops() <- protocol.UserInput{Text: "a"}
	select {
	case <-prov.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("first stream missing")
	}
	engB.Ops() <- protocol.UserInput{Text: "b"}
	waitModelWaiting(t, sched, 1)
	if calls, _, max := prov.snapshot(); calls != 1 || max != 1 {
		t.Fatalf("shared pool broken: calls=%d max=%d", calls, max)
	}
	close(hold)
	waitTurnDone(t, engA, 5*time.Second)
	waitTurnDone(t, engB, 5*time.Second)
}

func TestModelPoolUnlimitedCapacityZero(t *testing.T) {
	// Capacity 0 means unlimited in scheduler.Config.
	s, err := scheduler.New(scheduler.Config{
		Pools: map[string]int{scheduler.PoolModel: 0},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	// Only model pool configured — Acquire model works unlimited.
	var leases []*scheduler.Lease
	for i := 0; i < 4; i++ {
		l, err := s.Acquire(context.Background(), scheduler.PoolModel)
		if err != nil {
			t.Fatalf("Acquire %d: %v", i, err)
		}
		leases = append(leases, l)
	}
	for _, l := range leases {
		l.Release()
	}
}
