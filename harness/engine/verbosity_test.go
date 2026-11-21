package engine_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike/harness/engine"
	"github.com/jonathanung/strike/harness/provider"
	"github.com/jonathanung/strike/harness/tool"
	"github.com/jonathanung/strike/pkg/protocol"
)

const verbosityMarker = "Response style for this session:"

func runVerbosityTurn(t *testing.T, eng *engine.Engine, text string) {
	t.Helper()
	eng.Ops() <- protocol.UserInput{Text: text}
	waitForEvent(t, eng, func(ev protocol.Event) bool {
		_, ok := ev.(protocol.TurnCompleted)
		return ok
	})
}

func waitVerbosity(t *testing.T, eng *engine.Engine, level protocol.Verbosity) protocol.VerbositySelected {
	t.Helper()
	ev := waitForEvent(t, eng, func(ev protocol.Event) bool {
		sel, ok := ev.(protocol.VerbositySelected)
		return ok && sel.Level == level
	})
	return ev.(protocol.VerbositySelected)
}

func TestSetVerbosityReachesNextRequestAsInstruction(t *testing.T) {
	const sessionID = "verbosity-session"
	prov := &fastRecordingProvider{}
	eng := engine.New(engine.Options{
		SessionID:       sessionID,
		InitialProvider: "recording",
		Select: func(string) (provider.Provider, string, error) {
			return prov, "model", nil
		},
		Registry: tool.NewRegistry(),
		WorkDir:  t.TempDir(),
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	runVerbosityTurn(t, eng, "before any style")

	eng.Ops() <- protocol.SetVerbosity{Level: protocol.VerbosityConcise}
	sel := waitVerbosity(t, eng, protocol.VerbosityConcise)
	if sel.Delivery != protocol.VerbosityDeliveryInstruction {
		t.Errorf("concise delivery = %q, want instruction", sel.Delivery)
	}
	if sel.Correlation != (protocol.Correlation{SessionID: sessionID}) {
		t.Errorf("VerbositySelected correlation = %#v, want session only", sel.Correlation)
	}
	runVerbosityTurn(t, eng, "concise turn")

	eng.Ops() <- protocol.SetVerbosity{Level: protocol.VerbosityDetailed}
	waitVerbosity(t, eng, protocol.VerbosityDetailed)
	runVerbosityTurn(t, eng, "detailed turn")

	eng.Ops() <- protocol.SetVerbosity{Level: "default"}
	sel = waitVerbosity(t, eng, protocol.VerbosityDefault)
	if sel.Delivery != protocol.VerbosityDeliveryNone {
		t.Errorf("default delivery = %q, want none", sel.Delivery)
	}
	runVerbosityTurn(t, eng, "default turn")

	reqs := prov.Requests()
	if len(reqs) != 4 {
		t.Fatalf("requests = %d, want 4", len(reqs))
	}
	tests := []struct {
		name string
		want string // "" means no style layer
	}{
		{"before any style", ""},
		{"concise", verbosityMarker + " concise"},
		{"detailed", verbosityMarker + " detailed"},
		{"back to default", ""},
	}
	for i, tt := range tests {
		sys := reqs[i].System
		if tt.want == "" {
			if strings.Contains(sys, verbosityMarker) {
				t.Errorf("%s: system carries a style layer", tt.name)
			}
			continue
		}
		if !strings.Contains(sys, tt.want) {
			t.Errorf("%s: system missing %q", tt.name, tt.want)
		}
		if strings.Count(sys, verbosityMarker) != 1 {
			t.Errorf("%s: want exactly one style layer", tt.name)
		}
	}
}

func TestSetVerbosityRejectsUnknownLevel(t *testing.T) {
	eng := engine.New(engine.Options{
		SessionID:       "verbosity-bad",
		InitialProvider: "recording",
		Select: func(string) (provider.Provider, string, error) {
			return &fastRecordingProvider{}, "model", nil
		},
		Registry: tool.NewRegistry(),
		WorkDir:  t.TempDir(),
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.SetVerbosity{Level: "loud"}
	waitForEvent(t, eng, func(ev protocol.Event) bool {
		err, ok := ev.(protocol.EngineError)
		return ok && strings.Contains(err.Message, "unknown verbosity")
	})
}

func TestSetVerbosityDuringActiveTurnIsRejected(t *testing.T) {
	prov := &blockingFastProvider{requests: make(chan provider.Request, 1)}
	eng := engine.New(engine.Options{
		SessionID:       "verbosity-active",
		InitialProvider: "blocking",
		Select: func(string) (provider.Provider, string, error) {
			return prov, "model", nil
		},
		Registry: tool.NewRegistry(),
		WorkDir:  t.TempDir(),
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "keep the turn active"}
	select {
	case <-prov.requests:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for active provider request")
	}
	eng.Ops() <- protocol.SetVerbosity{Level: protocol.VerbosityConcise}
	waitForEvent(t, eng, func(ev protocol.Event) bool {
		err, ok := ev.(protocol.EngineError)
		return ok && strings.Contains(err.Message, "cannot change verbosity")
	})
	select {
	case ev := <-eng.Events():
		if _, ok := ev.(protocol.VerbositySelected); ok {
			t.Fatalf("active-turn SetVerbosity emitted VerbositySelected: %#v", ev)
		}
	case <-time.After(100 * time.Millisecond):
	}
}

func TestInitialVerbosityAppliesFromFirstTurn(t *testing.T) {
	prov := &fastRecordingProvider{}
	eng := engine.New(engine.Options{
		SessionID:        "verbosity-initial",
		InitialProvider:  "recording",
		InitialVerbosity: protocol.VerbosityDetailed,
		Select: func(string) (provider.Provider, string, error) {
			return prov, "model", nil
		},
		Registry: tool.NewRegistry(),
		WorkDir:  t.TempDir(),
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	waitVerbosity(t, eng, protocol.VerbosityDetailed)
	runVerbosityTurn(t, eng, "first")
	reqs := prov.Requests()
	if len(reqs) != 1 || !strings.Contains(reqs[0].System, verbosityMarker+" detailed") {
		t.Fatalf("first request system missing the detailed style layer")
	}
}

func TestRestoreKeepsLastVerbosity(t *testing.T) {
	corr := protocol.Correlation{SessionID: "s1"}
	tests := []struct {
		name   string
		events []protocol.Event
		want   protocol.Verbosity
	}{
		{"none", nil, protocol.VerbosityDefault},
		{"last wins", []protocol.Event{
			protocol.VerbositySelected{Correlation: corr, Level: protocol.VerbosityConcise, Delivery: protocol.VerbosityDeliveryInstruction},
			protocol.VerbositySelected{Correlation: corr, Level: protocol.VerbosityDetailed, Delivery: protocol.VerbosityDeliveryInstruction},
		}, protocol.VerbosityDetailed},
		{"reset to default", []protocol.Event{
			protocol.VerbositySelected{Correlation: corr, Level: protocol.VerbosityConcise, Delivery: protocol.VerbosityDeliveryInstruction},
			protocol.VerbositySelected{Correlation: corr, Level: protocol.VerbosityDefault, Delivery: protocol.VerbosityDeliveryNone},
		}, protocol.VerbosityDefault},
	}
	for _, tt := range tests {
		if got := engine.Restore(tt.events).Verbosity; got != tt.want {
			t.Errorf("%s: Restore().Verbosity = %q, want %q", tt.name, got, tt.want)
		}
	}
}
