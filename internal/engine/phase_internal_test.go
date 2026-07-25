package engine

import (
	"testing"

	"github.com/jonathanung/strike-cli/internal/config"
	"github.com/jonathanung/strike-cli/internal/permission"
	"github.com/jonathanung/strike-cli/internal/protocol"
)

func TestClearPhaseNoopWhenInactive(t *testing.T) {
	e := New(Options{SessionID: "clear-noop"})
	e.clearPhase()
	select {
	case ev := <-e.Events():
		t.Fatalf("noop clearPhase emitted %#v", ev)
	default:
	}
}

func TestClearPhaseDropsWorkflowAndEmits(t *testing.T) {
	e := New(Options{
		SessionID: "clear-active",
		Rules:     []permission.Ruleset{permission.Defaults()},
	})
	w := config.Workflow{
		Name: "solo",
		Phases: []config.Phase{{
			Name: "only",
			Permissions: permission.Ruleset{
				{Permission: "write", Pattern: "*", Action: permission.Deny},
			},
			Exit: config.ExitGate{Type: config.GateAgent},
		}},
	}
	if err := e.enterPhase(w, 0); err != nil {
		t.Fatal(err)
	}
	ev := <-e.Events()
	entered, ok := ev.(protocol.PhaseChanged)
	if !ok || entered.Workflow != "solo" || entered.Phase != "only" {
		t.Fatalf("enter event = %#v", ev)
	}

	e.clearPhase()
	ev = <-e.Events()
	cleared, ok := ev.(protocol.PhaseChanged)
	if !ok {
		t.Fatalf("clear event type = %T", ev)
	}
	if cleared.Workflow != "" || cleared.Phase != "" || cleared.Gate != "" {
		t.Fatalf("want empty PhaseChanged, got %#v", cleared)
	}
	if e.phaseIndex != -1 || e.workflow.Name != "" {
		t.Fatalf("state index=%d name=%q", e.phaseIndex, e.workflow.Name)
	}

	// Idempotent: second clear emits nothing.
	e.clearPhase()
	select {
	case ev := <-e.Events():
		t.Fatalf("second clearPhase emitted %#v", ev)
	default:
	}
}
