package engine_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/harness/engine"
	"github.com/jonathanung/strike-cli/harness/provider"
	"github.com/jonathanung/strike-cli/harness/tool"
	"github.com/jonathanung/strike-cli/pkg/protocol"
)

func TestAutonomySelectedEmittedAtStartupAsSupervised(t *testing.T) {
	const sessionID = "session-autonomy-default"
	eng, _, cancel := newRecordingEngine(t, engine.Options{SessionID: sessionID})
	defer cancel()

	event := waitForEvent(t, eng, func(ev protocol.Event) bool {
		_, ok := ev.(protocol.AutonomySelected)
		return ok
	})
	selected := event.(protocol.AutonomySelected)
	if selected.Mode != protocol.AutonomySupervised {
		t.Errorf("startup autonomy = %q, want supervised", selected.Mode)
	}
	if selected.Correlation != (protocol.Correlation{SessionID: sessionID}) {
		t.Errorf("AutonomySelected correlation = %#v, want session only", selected.Correlation)
	}
}

func TestInitialAutonomyAppliesAtStartup(t *testing.T) {
	eng, _, cancel := newRecordingEngine(t, engine.Options{InitialAutonomy: protocol.AutonomyChecks})
	defer cancel()

	event := waitForEvent(t, eng, func(ev protocol.Event) bool {
		sel, ok := ev.(protocol.AutonomySelected)
		return ok && sel.Mode == protocol.AutonomyChecks
	})
	if event.(protocol.AutonomySelected).Mode != protocol.AutonomyChecks {
		t.Fatalf("startup mode = %#v", event)
	}
}

func TestSetAutonomyConfirmsMode(t *testing.T) {
	const sessionID = "session-autonomy-set"
	eng, _, cancel := newRecordingEngine(t, engine.Options{SessionID: sessionID})
	defer cancel()
	// Drain startup confirmation.
	waitForEvent(t, eng, func(ev protocol.Event) bool {
		_, ok := ev.(protocol.AutonomySelected)
		return ok
	})

	eng.Ops() <- protocol.SetAutonomy{Mode: protocol.AutonomyAgent}
	event := waitForEvent(t, eng, func(ev protocol.Event) bool {
		sel, ok := ev.(protocol.AutonomySelected)
		return ok && sel.Mode == protocol.AutonomyAgent
	})
	selected := event.(protocol.AutonomySelected)
	if selected.Correlation != (protocol.Correlation{SessionID: sessionID}) {
		t.Errorf("AutonomySelected correlation = %#v, want session only", selected.Correlation)
	}
}

func TestSetAutonomyRejectsUnknownMode(t *testing.T) {
	eng, _, cancel := newRecordingEngine(t, engine.Options{})
	defer cancel()
	waitForEvent(t, eng, func(ev protocol.Event) bool {
		_, ok := ev.(protocol.AutonomySelected)
		return ok
	})

	eng.Ops() <- protocol.SetAutonomy{Mode: "yolo"}
	event := waitForEvent(t, eng, func(ev protocol.Event) bool {
		_, ok := ev.(protocol.EngineError)
		return ok
	})
	msg := event.(protocol.EngineError).Message
	for _, want := range []string{"yolo", "supervised", "agent", "checks", "skip-all"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error = %q, want it to mention %q", msg, want)
		}
	}
}

func TestSetAutonomyRejectedWhileTurnRunning(t *testing.T) {
	const sessionID = "session-autonomy-active"
	prov := &blockingFastProvider{requests: make(chan provider.Request, 1)}
	eng := engine.New(engine.Options{
		SessionID:       sessionID,
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

	waitForEvent(t, eng, func(ev protocol.Event) bool {
		_, ok := ev.(protocol.AutonomySelected)
		return ok
	})

	eng.Ops() <- protocol.UserInput{Text: "keep the turn active"}
	select {
	case <-prov.requests:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for active provider request")
	}

	eng.Ops() <- protocol.SetAutonomy{Mode: protocol.AutonomyAgent}
	event := waitForEvent(t, eng, func(ev protocol.Event) bool {
		err, ok := ev.(protocol.EngineError)
		return ok && strings.Contains(err.Message, "cannot change autonomy")
	})
	if event.(protocol.EngineError).Correlation != (protocol.Correlation{SessionID: sessionID}) {
		t.Errorf("EngineError correlation = %#v, want session only", event.(protocol.EngineError).Correlation)
	}
}
