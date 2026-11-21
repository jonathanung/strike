package engine_test

import (
	"context"
	"sync"
	"testing"

	"github.com/jonathanung/strike/harness/provider"
	"github.com/jonathanung/strike/pkg/protocol"
)

// refusingProvider really refuses (#1922): the first refuse calls stream
// refusal prose and end with provider.StopReasonRefusal, later calls answer.
type refusingProvider struct {
	mu     sync.Mutex
	calls  int
	refuse int
}

func (p *refusingProvider) Name() string { return "refusing" }

func (p *refusingProvider) Stream(context.Context, provider.Request) (<-chan provider.StreamEvent, error) {
	p.mu.Lock()
	p.calls++
	refuse := p.calls <= p.refuse
	p.mu.Unlock()
	stream := make(chan provider.StreamEvent, 2)
	if refuse {
		stream <- provider.StreamEvent{Type: provider.EventTextDelta, Text: "I can't help with that."}
		stream <- provider.StreamEvent{Type: provider.EventDone, StopReason: provider.StopReasonRefusal}
	} else {
		stream <- provider.StreamEvent{Type: provider.EventDone, StopReason: "end_turn"}
	}
	close(stream)
	return stream, nil
}

func (p *refusingProvider) Calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func TestRefusalMovesSessionToFallbackUntilReturn(t *testing.T) {
	primary := &refusingProvider{refuse: 1}
	backup := &fastRecordingProvider{}
	eng := newFallbackEngine(t, map[string]provider.Provider{"primary": primary, "backup": backup})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	setFallback(t, eng, "backup", "b1")

	// Turn 1: the primary refuses; the fallback answers and the session
	// stays on it.
	evs := runUntilDone(t, eng, "hello")
	fbs := fallbackEvents(evs)
	if len(fbs) != 1 {
		t.Fatalf("turn 1: ModelFallback events = %d, want 1", len(fbs))
	}
	fb := fbs[0]
	want := protocol.ModelFallback{
		Correlation:      fb.Correlation,
		PrimaryProvider:  "primary",
		PrimaryModel:     "primary-default",
		FallbackProvider: "backup",
		FallbackModel:    "b1",
		Reason:           protocol.ModelFallbackReasonRefused,
		Sticky:           true,
	}
	if fb != want {
		t.Errorf("turn 1: ModelFallback = %+v, want %+v", fb, want)
	}
	if tc := lastCompleted(t, evs); tc.StopReason != "end_turn" {
		t.Errorf("turn 1: stop reason = %q, want end_turn", tc.StopReason)
	}
	if primary.Calls() != 1 || len(backup.Requests()) != 1 {
		t.Fatalf("turn 1: calls primary=%d backup=%d, want 1/1", primary.Calls(), len(backup.Requests()))
	}

	// Turn 2: sticky — the fallback answers without trying the primary and
	// without a second notice.
	evs = runUntilDone(t, eng, "again")
	if n := len(fallbackEvents(evs)); n != 0 {
		t.Errorf("turn 2: ModelFallback events = %d, want 0 (already on fallback)", n)
	}
	if primary.Calls() != 1 || len(backup.Requests()) != 2 {
		t.Fatalf("turn 2: calls primary=%d backup=%d, want 1/2", primary.Calls(), len(backup.Requests()))
	}
	if got := backup.Requests()[1].Model; got != "b1" {
		t.Errorf("turn 2: fallback model = %q, want b1", got)
	}

	// Return to primary: confirmed with Active false; the next turn uses the
	// primary again.
	eng.Ops() <- protocol.ReturnToPrimary{}
	ev := waitForEvent(t, eng, func(ev protocol.Event) bool {
		_, ok := ev.(protocol.FallbackModelSelected)
		return ok
	})
	sel := ev.(protocol.FallbackModelSelected)
	if sel.Provider != "backup" || sel.Model != "b1" || sel.Active {
		t.Errorf("ReturnToPrimary confirmation = %+v, want backup/b1 inactive", sel)
	}
	evs = runUntilDone(t, eng, "third")
	if n := len(fallbackEvents(evs)); n != 0 {
		t.Errorf("turn 3: ModelFallback events = %d, want 0", n)
	}
	if primary.Calls() != 2 || len(backup.Requests()) != 2 {
		t.Errorf("turn 3: calls primary=%d backup=%d, want 2/2", primary.Calls(), len(backup.Requests()))
	}
}

func TestRefusalWithoutFallbackEndsTheTurn(t *testing.T) {
	primary := &refusingProvider{refuse: 5}
	eng := newFallbackEngine(t, map[string]provider.Provider{"primary": primary})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	evs := runUntilDone(t, eng, "hello")
	if n := len(fallbackEvents(evs)); n != 0 {
		t.Errorf("ModelFallback events = %d, want 0", n)
	}
	if tc := lastCompleted(t, evs); tc.StopReason != provider.StopReasonRefusal {
		t.Errorf("stop reason = %q, want %q", tc.StopReason, provider.StopReasonRefusal)
	}
	if primary.Calls() != 1 {
		t.Errorf("primary calls = %d, want 1", primary.Calls())
	}
}

func TestNewFallbackPickEndsStickyFallback(t *testing.T) {
	primary := &refusingProvider{refuse: 1}
	backup := &fastRecordingProvider{}
	other := &fastRecordingProvider{}
	eng := newFallbackEngine(t, map[string]provider.Provider{"primary": primary, "backup": backup, "other": other})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	setFallback(t, eng, "backup", "b1")
	runUntilDone(t, eng, "hello") // primary refuses; sticky on backup

	if sel := setFallback(t, eng, "other", "o1"); sel.Active {
		t.Errorf("new pick confirmation = %+v, want inactive", sel)
	}
	runUntilDone(t, eng, "again")
	if primary.Calls() != 2 {
		t.Errorf("primary calls = %d, want 2 (primary back after a new pick)", primary.Calls())
	}
	if n := len(other.Requests()); n != 0 {
		t.Errorf("new fallback requests = %d, want 0 (primary answered)", n)
	}
}
