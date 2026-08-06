package engine_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/engine"
	"github.com/jonathanung/strike-cli/internal/permission"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/provider"
	"github.com/jonathanung/strike-cli/internal/scheduler"
	"github.com/jonathanung/strike-cli/internal/tool"
)

// holdStreamProvider blocks in Stream until released so model admission can queue.
type holdStreamProvider struct {
	hold <-chan struct{}
	mu   sync.Mutex
	n    int
}

func (p *holdStreamProvider) Name() string { return "hold" }

func (p *holdStreamProvider) Stream(ctx context.Context, _ provider.Request) (<-chan provider.StreamEvent, error) {
	p.mu.Lock()
	p.n++
	p.mu.Unlock()
	out := make(chan provider.StreamEvent)
	go func() {
		defer close(out)
		select {
		case <-p.hold:
		case <-ctx.Done():
			return
		}
		out <- provider.StreamEvent{Type: provider.EventTextDelta, Text: "ok"}
		out <- provider.StreamEvent{Type: provider.EventDone, StopReason: "end_turn"}
	}()
	return out, nil
}

func findQueued(events []protocol.Event, label string) (protocol.SchedulerQueued, bool) {
	for _, ev := range events {
		if q, ok := ev.(protocol.SchedulerQueued); ok {
			if label == "" || q.Label == label {
				return q, true
			}
		}
	}
	return protocol.SchedulerQueued{}, false
}

func findAdmitted(events []protocol.Event, reqID string) (protocol.SchedulerAdmitted, bool) {
	for _, ev := range events {
		if a, ok := ev.(protocol.SchedulerAdmitted); ok && a.RequestID == reqID {
			return a, true
		}
	}
	return protocol.SchedulerAdmitted{}, false
}

func findCanceled(events []protocol.Event, reqID string) (protocol.SchedulerCanceled, bool) {
	for _, ev := range events {
		if c, ok := ev.(protocol.SchedulerCanceled); ok && c.RequestID == reqID {
			return c, true
		}
	}
	return protocol.SchedulerCanceled{}, false
}

func TestModelQueueEmitsQueuedAdmitted(t *testing.T) {
	s, err := scheduler.New(scheduler.Config{Pools: map[string]int{
		scheduler.PoolModel:   1,
		scheduler.PoolProcess: 4,
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	holder, err := s.Acquire(context.Background(), scheduler.PoolModel)
	if err != nil {
		t.Fatal(err)
	}

	hold := make(chan struct{})
	prov := &holdStreamProvider{hold: hold}
	eng := engine.New(engine.Options{
		SessionID: "root-a",
		Select: func(string) (provider.Provider, string, error) {
			return prov, "hold", nil
		},
		InitialProvider: "hold",
		Scheduler:       s,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "hi"}

	events := drainUntil(t, eng, 5*time.Second, func(evs []protocol.Event) bool {
		_, ok := findQueued(evs, "model")
		return ok
	})
	queued, ok := findQueued(events, "model")
	if !ok || queued.RequestID == "" {
		t.Fatalf("queued=%+v", queued)
	}
	if len(queued.Pools) != 1 || queued.Pools[0] != scheduler.PoolModel {
		t.Fatalf("pools=%v", queued.Pools)
	}
	if queued.SessionID != "root-a" {
		t.Fatalf("session=%q", queued.SessionID)
	}

	holder.Release()

	more := drainUntil(t, eng, 5*time.Second, func(evs []protocol.Event) bool {
		_, ok := findAdmitted(evs, queued.RequestID)
		return ok
	})
	events = append(events, more...)
	admitted, ok := findAdmitted(events, queued.RequestID)
	if !ok {
		t.Fatal("missing admitted")
	}
	if admitted.RequestID != queued.RequestID {
		t.Fatalf("admitted id=%q want %q", admitted.RequestID, queued.RequestID)
	}
	close(hold)

	if _, ok := findCanceled(events, queued.RequestID); ok {
		t.Fatal("unexpected cancel for successful admit")
	}
}

func TestModelQueueCancelNoLaterAdmitted(t *testing.T) {
	s, err := scheduler.New(scheduler.Config{Pools: map[string]int{
		scheduler.PoolModel: 1,
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	holder, err := s.Acquire(context.Background(), scheduler.PoolModel)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Release()

	hold := make(chan struct{})
	defer close(hold)
	eng := engine.New(engine.Options{
		SessionID: "root-b",
		Select: func(string) (provider.Provider, string, error) {
			return &holdStreamProvider{hold: hold}, "hold", nil
		},
		InitialProvider: "hold",
		Scheduler:       s,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "wait"}

	events := drainUntil(t, eng, 5*time.Second, func(evs []protocol.Event) bool {
		_, ok := findQueued(evs, "model")
		return ok
	})
	queued, _ := findQueued(events, "model")

	eng.Ops() <- protocol.Interrupt{}

	more := drainUntil(t, eng, 5*time.Second, func(evs []protocol.Event) bool {
		_, ok := findCanceled(evs, queued.RequestID)
		return ok
	})
	events = append(events, more...)
	canceled, ok := findCanceled(events, queued.RequestID)
	if !ok {
		t.Fatal("missing canceled")
	}
	if canceled.Reason != protocol.SchedulerReasonCanceled {
		t.Fatalf("reason=%q", canceled.Reason)
	}
	if _, ok := findAdmitted(events, queued.RequestID); ok {
		t.Fatal("admitted after cancel")
	}
	// Brief extra drain — admitted must never appear for this request.
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		select {
		case ev := <-eng.Events():
			if a, ok := ev.(protocol.SchedulerAdmitted); ok && a.RequestID == queued.RequestID {
				t.Fatalf("later admitted after cancel: %+v", a)
			}
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestBashQueueEmitsQueuedAndCancelClears(t *testing.T) {
	s, err := scheduler.New(scheduler.Config{Pools: map[string]int{
		scheduler.PoolProcess: 1,
		scheduler.PoolModel:   4,
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	holder, err := s.Acquire(context.Background(), scheduler.PoolProcess)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Release()

	eng := engine.New(engine.Options{
		SessionID:       "root-bash",
		Select:          selectEcho,
		InitialProvider: "echo",
		Registry:        tool.NewRegistry(tool.NewBash()),
		WorkDir:         t.TempDir(),
		SandboxMode:     "off",
		Scheduler:       s,
		Rules: []permission.Ruleset{{
			{Permission: "bash", Action: permission.Allow, Pattern: "*"},
		}},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "run sleep 30"}

	events := drainUntil(t, eng, 5*time.Second, func(evs []protocol.Event) bool {
		for _, ev := range evs {
			if _, ok := ev.(protocol.ProcessStarted); ok {
				t.Fatal("ProcessStarted before admission")
			}
		}
		_, ok := findQueued(evs, "bash")
		return ok
	})
	queued, ok := findQueued(events, "bash")
	if !ok || len(queued.Pools) == 0 || queued.Pools[0] != scheduler.PoolProcess {
		t.Fatalf("queued=%+v", queued)
	}

	eng.Ops() <- protocol.Interrupt{}

	more := drainUntil(t, eng, 5*time.Second, func(evs []protocol.Event) bool {
		for _, ev := range evs {
			if a, ok := ev.(protocol.SchedulerAdmitted); ok && a.RequestID == queued.RequestID {
				t.Fatalf("admitted after bash cancel: %+v", a)
			}
			if _, ok := ev.(protocol.ProcessStarted); ok {
				t.Fatal("ProcessStarted after cancel")
			}
		}
		_, ok := findCanceled(evs, queued.RequestID)
		return ok
	})
	events = append(events, more...)
	if _, ok := findCanceled(events, queued.RequestID); !ok {
		t.Fatal("missing bash canceled")
	}
}

func TestMultiRootQueueCorrelation(t *testing.T) {
	s, err := scheduler.New(scheduler.Config{Pools: map[string]int{
		scheduler.PoolModel: 1,
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	holder, err := s.Acquire(context.Background(), scheduler.PoolModel)
	if err != nil {
		t.Fatal(err)
	}

	hold := make(chan struct{})
	makeEng := func(id string) *engine.Engine {
		return engine.New(engine.Options{
			SessionID: id,
			Select: func(string) (provider.Provider, string, error) {
				return &holdStreamProvider{hold: hold}, "hold", nil
			},
			InitialProvider: "hold",
			Scheduler:       s,
		})
	}
	a := makeEng("root-1")
	b := makeEng("root-2")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx)
	go b.Run(ctx)

	a.Ops() <- protocol.UserInput{Text: "a"}
	b.Ops() <- protocol.UserInput{Text: "b"}

	bySession := map[string]protocol.SchedulerQueued{}
	deadline := time.Now().Add(5 * time.Second)
	for len(bySession) < 2 && time.Now().Before(deadline) {
		for _, eng := range []*engine.Engine{a, b} {
			select {
			case ev := <-eng.Events():
				if q, ok := ev.(protocol.SchedulerQueued); ok {
					bySession[q.SessionID] = q
				}
			default:
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	if len(bySession) < 2 {
		t.Fatalf("queued sessions=%v want root-1 and root-2", bySession)
	}
	if bySession["root-1"].RequestID == "" || bySession["root-2"].RequestID == "" {
		t.Fatalf("missing request ids: %+v", bySession)
	}
	if bySession["root-1"].RequestID == bySession["root-2"].RequestID {
		t.Fatal("request ids must differ across roots")
	}

	holder.Release()
	close(hold)
}
