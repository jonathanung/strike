package engine_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/harness/engine"
	"github.com/jonathanung/strike-cli/harness/permission"
	"github.com/jonathanung/strike-cli/harness/provider"
	"github.com/jonathanung/strike-cli/harness/tool"
	"github.com/jonathanung/strike-cli/pkg/protocol"
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

func TestSetPermissionModeLockedByManaged(t *testing.T) {
	// Lock default (not plan) so startup does not enter the plan workflow.
	eng, _, cancel := newRecordingEngine(t, engine.Options{
		InitialPermissionMode: protocol.PermissionModeDefault,
		LockPermissionMode:    true,
	})
	defer cancel()
	waitForEvent(t, eng, func(ev protocol.Event) bool {
		sel, ok := ev.(protocol.PermissionModeSelected)
		return ok && sel.Mode == protocol.PermissionModeDefault
	})

	eng.Ops() <- protocol.SetPermissionMode{Mode: protocol.PermissionModeYolo}
	event := waitForEvent(t, eng, func(ev protocol.Event) bool {
		_, ok := ev.(protocol.EngineError)
		return ok
	})
	msg := event.(protocol.EngineError).Message
	if !strings.Contains(msg, "managed") || !strings.Contains(msg, "default") {
		t.Fatalf("error = %q, want managed lock mention", msg)
	}
}

func TestSetPermissionModeYoloRejectedWhenSandboxOff(t *testing.T) {
	eng, _, cancel := newRecordingEngine(t, engine.Options{
		SandboxMode: "off",
	})
	defer cancel()
	waitForEvent(t, eng, func(ev protocol.Event) bool {
		_, ok := ev.(protocol.PermissionModeSelected)
		return ok
	})

	eng.Ops() <- protocol.SetPermissionMode{Mode: protocol.PermissionModeYolo}
	event := waitForEvent(t, eng, func(ev protocol.Event) bool {
		_, ok := ev.(protocol.EngineError)
		return ok
	})
	msg := event.(protocol.EngineError).Message
	if !strings.Contains(msg, "--i-know") {
		t.Fatalf("error = %q, want --i-know mention", msg)
	}
}

func TestSetPermissionModeYoloAllowedWithIKnowWhenSandboxOff(t *testing.T) {
	eng, _, cancel := newRecordingEngine(t, engine.Options{
		SandboxMode:             "off",
		AllowYoloWithoutSandbox: true,
	})
	defer cancel()
	waitForEvent(t, eng, func(ev protocol.Event) bool {
		_, ok := ev.(protocol.PermissionModeSelected)
		return ok
	})

	eng.Ops() <- protocol.SetPermissionMode{Mode: protocol.PermissionModeYolo}
	event := waitForEvent(t, eng, func(ev protocol.Event) bool {
		sel, ok := ev.(protocol.PermissionModeSelected)
		return ok && sel.Mode == protocol.PermissionModeYolo
	})
	if event.(protocol.PermissionModeSelected).Mode != protocol.PermissionModeYolo {
		t.Fatalf("got %#v", event)
	}
}

func TestSetPermissionModeAcceptedWhileTurnRunning(t *testing.T) {
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
		sel, ok := ev.(protocol.PermissionModeSelected)
		return ok && sel.Mode == protocol.PermissionModeYolo
	})
	selected := event.(protocol.PermissionModeSelected)
	if selected.Correlation != (protocol.Correlation{SessionID: sessionID}) {
		t.Errorf("PermissionModeSelected correlation = %#v, want session only", selected.Correlation)
	}
}

func TestSetPermissionModePlanMidTurn(t *testing.T) {
	prov := &blockingFastProvider{requests: make(chan provider.Request, 1)}
	eng := engine.New(engine.Options{
		SessionID:       "session-perm-mode-plan-mid",
		InitialProvider: "blocking",
		Select: func(string) (provider.Provider, string, error) {
			return prov, "model", nil
		},
		Registry: tool.NewRegistry(),
		WorkDir:  t.TempDir(),
		Agents: []engine.Agent{
			{Name: "build", Description: "build"},
			{Name: "plan", Description: "plan"},
		},
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

	eng.Ops() <- protocol.SetPermissionMode{Mode: protocol.PermissionModePlan}
	var sawMode, sawPhase bool
	deadline := time.After(10 * time.Second)
	for !sawMode || !sawPhase {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for plan mode+phase mid-turn (mode=%v phase=%v)", sawMode, sawPhase)
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
			case protocol.EngineError:
				t.Fatalf("unexpected EngineError mid-turn plan mode: %s", e.Message)
			}
		}
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
