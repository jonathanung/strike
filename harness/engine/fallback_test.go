package engine_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/jonathanung/strike/harness/engine"
	"github.com/jonathanung/strike/harness/provider"
	"github.com/jonathanung/strike/harness/tool"
	"github.com/jonathanung/strike/pkg/protocol"
)

// failingProvider rejects every request with a non-retryable error.
type failingProvider struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (p *failingProvider) Name() string { return "failing" }

func (p *failingProvider) Stream(context.Context, provider.Request) (<-chan provider.StreamEvent, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	return nil, p.err
}

func (p *failingProvider) Calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func newFallbackEngine(t *testing.T, providers map[string]provider.Provider) *engine.Engine {
	t.Helper()
	eng := engine.New(engine.Options{
		SessionID:       "fallback-session",
		InitialProvider: "primary",
		Select: func(name string) (provider.Provider, string, error) {
			p, ok := providers[name]
			if !ok {
				return nil, "", errors.New("unknown provider " + name)
			}
			return p, name + "-default", nil
		},
		Registry: tool.NewRegistry(),
		WorkDir:  t.TempDir(),
	})
	return eng
}

func setFallback(t *testing.T, eng *engine.Engine, prov, model string) protocol.FallbackModelSelected {
	t.Helper()
	eng.Ops() <- protocol.SetFallbackModel{Provider: prov, Model: model}
	ev := waitForEvent(t, eng, func(ev protocol.Event) bool {
		_, ok := ev.(protocol.FallbackModelSelected)
		return ok
	})
	return ev.(protocol.FallbackModelSelected)
}

// runUntilDone sends one prompt and collects events until TurnCompleted.
func runUntilDone(t *testing.T, eng *engine.Engine, text string) []protocol.Event {
	t.Helper()
	eng.Ops() <- protocol.UserInput{Text: text}
	var got []protocol.Event
	waitForEvent(t, eng, func(ev protocol.Event) bool {
		got = append(got, ev)
		_, ok := ev.(protocol.TurnCompleted)
		return ok
	})
	return got
}

func fallbackEvents(evs []protocol.Event) []protocol.ModelFallback {
	var out []protocol.ModelFallback
	for _, ev := range evs {
		if fb, ok := ev.(protocol.ModelFallback); ok {
			out = append(out, fb)
		}
	}
	return out
}

func lastCompleted(t *testing.T, evs []protocol.Event) protocol.TurnCompleted {
	t.Helper()
	tc, ok := evs[len(evs)-1].(protocol.TurnCompleted)
	if !ok {
		t.Fatalf("last event = %T, want TurnCompleted", evs[len(evs)-1])
	}
	return tc
}

func TestFallbackAnswersTheTurnAndPrimaryIsRetriedNextTurn(t *testing.T) {
	primary := &failingProvider{err: errors.New("primary rejected the request")}
	backup := &fastRecordingProvider{}
	eng := newFallbackEngine(t, map[string]provider.Provider{"primary": primary, "backup": backup})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	sel := setFallback(t, eng, "backup", "b1")
	if sel.Provider != "backup" || sel.Model != "b1" {
		t.Fatalf("FallbackModelSelected = %+v", sel)
	}

	for turn := 1; turn <= 2; turn++ {
		evs := runUntilDone(t, eng, "hello")
		fbs := fallbackEvents(evs)
		if len(fbs) != 1 {
			t.Fatalf("turn %d: ModelFallback events = %d, want 1", turn, len(fbs))
		}
		fb := fbs[0]
		want := protocol.ModelFallback{
			Correlation:      fb.Correlation,
			PrimaryProvider:  "primary",
			PrimaryModel:     "primary-default",
			FallbackProvider: "backup",
			FallbackModel:    "b1",
			Reason:           protocol.ModelFallbackReasonUnavailable,
			Error:            fb.Error,
		}
		if fb != want {
			t.Errorf("turn %d: ModelFallback = %+v, want %+v", turn, fb, want)
		}
		if !strings.Contains(fb.Error, "primary rejected the request") {
			t.Errorf("turn %d: ModelFallback.Error = %q", turn, fb.Error)
		}
		if fb.TurnID == "" {
			t.Errorf("turn %d: ModelFallback has no turn correlation", turn)
		}
		if tc := lastCompleted(t, evs); tc.StopReason != "end_turn" {
			t.Errorf("turn %d: stop reason = %q, want end_turn", turn, tc.StopReason)
		}
		for _, ev := range evs {
			if ms, ok := ev.(protocol.ModelSelected); ok {
				t.Errorf("turn %d: fallback emitted ModelSelected %+v (not a user switch)", turn, ms)
			}
		}
		// The primary is tried first on every turn.
		if primary.Calls() != turn {
			t.Errorf("turn %d: primary calls = %d, want %d", turn, primary.Calls(), turn)
		}
	}
	reqs := backup.Requests()
	if len(reqs) != 2 {
		t.Fatalf("fallback requests = %d, want 2", len(reqs))
	}
	if reqs[0].Model != "b1" {
		t.Errorf("fallback request model = %q, want b1", reqs[0].Model)
	}
}

func TestFallbackFailureFailsTheTurnNamingBothModels(t *testing.T) {
	primary := &failingProvider{err: errors.New("primary down")}
	backup := &failingProvider{err: errors.New("backup down")}
	eng := newFallbackEngine(t, map[string]provider.Provider{"primary": primary, "backup": backup})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	setFallback(t, eng, "backup", "b1")
	evs := runUntilDone(t, eng, "hello")
	if tc := lastCompleted(t, evs); tc.StopReason != "error" {
		t.Errorf("stop reason = %q, want error", tc.StopReason)
	}
	var msg string
	for _, ev := range evs {
		if ee, ok := ev.(protocol.EngineError); ok {
			msg = ee.Message
		}
		if td, ok := ev.(protocol.TextDelta); ok {
			t.Errorf("failed turn produced text %q", td.Text)
		}
	}
	for _, want := range []string{"primary primary/primary-default failed: primary down", "fallback backup/b1 failed: backup down"} {
		if !strings.Contains(msg, want) {
			t.Errorf("EngineError = %q, want it to contain %q", msg, want)
		}
	}
	if primary.Calls() != 1 || backup.Calls() != 1 {
		t.Errorf("calls primary=%d backup=%d, want 1 each (no third hop)", primary.Calls(), backup.Calls())
	}
}

func TestNoFallbackMeansPlainFailure(t *testing.T) {
	primary := &failingProvider{err: errors.New("primary down")}
	backup := &fastRecordingProvider{}
	eng := newFallbackEngine(t, map[string]provider.Provider{"primary": primary, "backup": backup})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	tests := []struct {
		name  string
		setup func()
	}{
		{"never set", func() {}},
		{"cleared", func() {
			setFallback(t, eng, "backup", "b1")
			if sel := setFallback(t, eng, "", ""); sel.Provider != "" || sel.Model != "" {
				t.Fatalf("clear FallbackModelSelected = %+v", sel)
			}
		}},
	}
	for _, tt := range tests {
		tt.setup()
		evs := runUntilDone(t, eng, "hello")
		if n := len(fallbackEvents(evs)); n != 0 {
			t.Errorf("%s: ModelFallback events = %d, want 0", tt.name, n)
		}
		if tc := lastCompleted(t, evs); tc.StopReason != "error" {
			t.Errorf("%s: stop reason = %q, want error", tt.name, tc.StopReason)
		}
	}
	if n := len(backup.Requests()); n != 0 {
		t.Errorf("backup requests = %d, want 0", n)
	}
}

func TestSetFallbackRejectsUnknownProvider(t *testing.T) {
	eng := newFallbackEngine(t, map[string]provider.Provider{"primary": &fastRecordingProvider{}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.SetFallbackModel{Provider: "nope"}
	waitForEvent(t, eng, func(ev protocol.Event) bool {
		ee, ok := ev.(protocol.EngineError)
		return ok && strings.Contains(ee.Message, "fallback model") && strings.Contains(ee.Message, "unknown provider nope")
	})
}

func TestRestoreKeepsLastFallbackModel(t *testing.T) {
	corr := protocol.Correlation{SessionID: "s1"}
	tests := []struct {
		name   string
		events []protocol.Event
		prov   string
		model  string
	}{
		{"none", nil, "", ""},
		{"set", []protocol.Event{protocol.FallbackModelSelected{Correlation: corr, Provider: "openai", Model: "gpt"}}, "openai", "gpt"},
		{"cleared", []protocol.Event{
			protocol.FallbackModelSelected{Correlation: corr, Provider: "openai", Model: "gpt"},
			protocol.FallbackModelSelected{Correlation: corr},
		}, "", ""},
	}
	for _, tt := range tests {
		r := engine.Restore(tt.events)
		if r.FallbackProvider != tt.prov || r.FallbackModel != tt.model {
			t.Errorf("%s: Restore fallback = %q/%q, want %q/%q", tt.name, r.FallbackProvider, r.FallbackModel, tt.prov, tt.model)
		}
	}
}

func TestInitialFallbackIsRestoredAtStartup(t *testing.T) {
	primary := &failingProvider{err: errors.New("primary down")}
	backup := &fastRecordingProvider{}
	eng := engine.New(engine.Options{
		SessionID:               "fallback-initial",
		InitialProvider:         "primary",
		InitialFallbackProvider: "backup",
		InitialFallbackModel:    "b1",
		Select: func(name string) (provider.Provider, string, error) {
			if name == "backup" {
				return backup, "backup-default", nil
			}
			return primary, "primary-default", nil
		},
		Registry: tool.NewRegistry(),
		WorkDir:  t.TempDir(),
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	evs := runUntilDone(t, eng, "hello")
	if n := len(fallbackEvents(evs)); n != 1 {
		t.Fatalf("ModelFallback events = %d, want 1", n)
	}
	if n := len(backup.Requests()); n != 1 {
		t.Errorf("backup requests = %d, want 1", n)
	}
}
