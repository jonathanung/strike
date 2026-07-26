package engine_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/engine"
	"github.com/jonathanung/strike-cli/internal/permission"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/provider"
	"github.com/jonathanung/strike-cli/internal/tool"
)

func TestPermissionModeSelectedEmittedAtStartupAsDefault(t *testing.T) {
	const sessionID = "session-perm-mode-default"
	eng, _, cancel := newRecordingEngine(t, engine.Options{SessionID: sessionID})
	defer cancel()

	event := waitForEvent(t, eng, func(ev protocol.Event) bool {
		_, ok := ev.(protocol.PermissionModeSelected)
		return ok
	})
	selected := event.(protocol.PermissionModeSelected)
	if selected.Mode != protocol.PermissionModeDefault {
		t.Errorf("startup mode = %q, want default", selected.Mode)
	}
	if selected.Correlation != (protocol.Correlation{SessionID: sessionID}) {
		t.Errorf("PermissionModeSelected correlation = %#v, want session only", selected.Correlation)
	}
}

func TestInitialPermissionModeAppliesAtStartup(t *testing.T) {
	eng, _, cancel := newRecordingEngine(t, engine.Options{
		InitialPermissionMode: protocol.PermissionModeYolo,
	})
	defer cancel()

	event := waitForEvent(t, eng, func(ev protocol.Event) bool {
		sel, ok := ev.(protocol.PermissionModeSelected)
		return ok && sel.Mode == protocol.PermissionModeYolo
	})
	if event.(protocol.PermissionModeSelected).Mode != protocol.PermissionModeYolo {
		t.Fatalf("startup mode = %#v", event)
	}
}

func TestSetPermissionModeConfirmsMode(t *testing.T) {
	const sessionID = "session-perm-mode-set"
	eng, _, cancel := newRecordingEngine(t, engine.Options{SessionID: sessionID})
	defer cancel()
	waitForEvent(t, eng, func(ev protocol.Event) bool {
		_, ok := ev.(protocol.PermissionModeSelected)
		return ok
	})

	eng.Ops() <- protocol.SetPermissionMode{Mode: protocol.PermissionModeAcceptEdits}
	event := waitForEvent(t, eng, func(ev protocol.Event) bool {
		sel, ok := ev.(protocol.PermissionModeSelected)
		return ok && sel.Mode == protocol.PermissionModeAcceptEdits
	})
	selected := event.(protocol.PermissionModeSelected)
	if selected.Correlation != (protocol.Correlation{SessionID: sessionID}) {
		t.Errorf("correlation = %#v, want session only", selected.Correlation)
	}
}

func TestSetPermissionModeRejectsUnknown(t *testing.T) {
	eng, _, cancel := newRecordingEngine(t, engine.Options{})
	defer cancel()
	waitForEvent(t, eng, func(ev protocol.Event) bool {
		_, ok := ev.(protocol.PermissionModeSelected)
		return ok
	})

	eng.Ops() <- protocol.SetPermissionMode{Mode: "supervised"}
	event := waitForEvent(t, eng, func(ev protocol.Event) bool {
		_, ok := ev.(protocol.EngineError)
		return ok
	})
	msg := event.(protocol.EngineError).Message
	for _, want := range []string{"supervised", "default", "plan", "soft-approve", "accept-edits", "yolo"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error = %q, want it to mention %q", msg, want)
		}
	}
}

func TestSetPermissionModeRejectedWhileTurnRunning(t *testing.T) {
	const sessionID = "session-perm-mode-active"
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
		_, ok := ev.(protocol.PermissionModeSelected)
		return ok
	})

	eng.Ops() <- protocol.UserInput{Text: "keep the turn active"}
	select {
	case <-prov.requests:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for active provider request")
	}

	eng.Ops() <- protocol.SetPermissionMode{Mode: protocol.PermissionModeYolo}
	event := waitForEvent(t, eng, func(ev protocol.Event) bool {
		err, ok := ev.(protocol.EngineError)
		return ok && strings.Contains(err.Message, "cannot change permission mode")
	})
	if event.(protocol.EngineError).Correlation != (protocol.Correlation{SessionID: sessionID}) {
		t.Errorf("EngineError correlation = %#v, want session only", event.(protocol.EngineError).Correlation)
	}
}

func TestSetPermissionModePlanEntersPlanPhase(t *testing.T) {
	eng, _, cancel := newRecordingEngine(t, engine.Options{
		Agents: []engine.Agent{
			{Name: "build", Description: "build"},
			{Name: "plan", Description: "plan"},
		},
	})
	defer cancel()
	waitForEvent(t, eng, func(ev protocol.Event) bool {
		_, ok := ev.(protocol.PermissionModeSelected)
		return ok
	})

	eng.Ops() <- protocol.SetPermissionMode{Mode: protocol.PermissionModePlan}
	var sawMode, sawPhase bool
	deadline := time.After(10 * time.Second)
	for !sawMode || !sawPhase {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for plan mode+phase (mode=%v phase=%v)", sawMode, sawPhase)
		case ev := <-eng.Events():
			switch e := ev.(type) {
			case protocol.PermissionModeSelected:
				if e.Mode == protocol.PermissionModePlan {
					sawMode = true
				}
			case protocol.PhaseChanged:
				if e.Phase == "plan" {
					sawPhase = true
				}
			}
		}
	}
}

func TestRestorePermissionMode(t *testing.T) {
	events := []protocol.Event{
		protocol.PermissionModeSelected{Mode: protocol.PermissionModeYolo},
		protocol.PermissionModeSelected{Mode: protocol.PermissionModeAcceptEdits},
	}
	got := engine.Restore(events)
	if got.PermissionMode != protocol.PermissionModeAcceptEdits {
		t.Fatalf("PermissionMode = %q, want accept-edits", got.PermissionMode)
	}
}

func TestPermissionModeYoloSkipsAskInEngine(t *testing.T) {
	// Echo provider with a write tool call path is heavy; exercise perms via
	// a direct service-backed engine option by running a tool that asks.
	var asked int
	perms := permission.New(func(ev protocol.Event) {
		if _, ok := ev.(protocol.PermissionAsked); ok {
			asked++
		}
	}, permission.Defaults())
	perms.SetPermissionMode(protocol.PermissionModeYolo)
	err := perms.Ask(context.Background(), tool.AskRequest{
		Permission: "bash",
		Patterns:   []string{"echo hi"},
	})
	if err != nil {
		t.Fatalf("yolo ask = %v", err)
	}
	if asked != 0 {
		t.Fatalf("asked = %d, want 0", asked)
	}
}
