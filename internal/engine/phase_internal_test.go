package engine

import (
	"context"
	"strings"
	"testing"
	"time"

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

func TestEffectiveGateLabelFollowsAutonomy(t *testing.T) {
	e := New(Options{SessionID: "gate-label"})
	cases := []struct {
		mode protocol.Autonomy
		want string
	}{
		{protocol.AutonomySupervised, "user"},
		{protocol.AutonomyAgent, "agent"},
		{protocol.AutonomyChecks, "check"},
		{protocol.AutonomySkipAll, "skip"},
		{"", "user"},
	}
	for _, tc := range cases {
		e.autonomy = tc.mode
		if got := e.effectiveGateLabel(); got != tc.want {
			t.Errorf("autonomy %q gate = %q, want %q", tc.mode, got, tc.want)
		}
	}
}

func TestSupervisedFailsClosedWithoutQuestionService(t *testing.T) {
	e := New(Options{
		SessionID: "sup-no-q",
		Rules:     []permission.Ruleset{permission.Defaults()},
	})
	e.autonomy = protocol.AutonomySupervised
	e.questions = nil
	err := e.runExitGate(context.Background(), config.Phase{Name: "plan"})
	if err == nil {
		t.Fatal("supervised without questions should fail closed")
	}
	if !strings.Contains(err.Error(), "question service") {
		t.Fatalf("error = %q, want question service", err.Error())
	}
}

func TestSetAutonomyReemitsPhaseGate(t *testing.T) {
	e := New(Options{
		SessionID: "auto-reemit",
		Rules:     []permission.Ruleset{permission.Defaults()},
	})
	// Default autonomy is empty until setAutonomy; treat as supervised label.
	w := config.BuiltinPlanImplement()
	if err := e.enterPhase(w, 0); err != nil {
		t.Fatal(err)
	}
	ev := <-e.Events()
	entered, ok := ev.(protocol.PhaseChanged)
	if !ok || entered.Gate != "user" {
		t.Fatalf("enter = %#v, want gate user", ev)
	}

	e.setAutonomy(protocol.AutonomySkipAll)
	var sawAuto, sawPhase bool
	deadline := time.After(2 * time.Second)
	for !sawAuto || !sawPhase {
		select {
		case <-deadline:
			t.Fatalf("timeout auto=%v phase=%v", sawAuto, sawPhase)
		case ev := <-e.Events():
			switch e := ev.(type) {
			case protocol.AutonomySelected:
				if e.Mode == protocol.AutonomySkipAll {
					sawAuto = true
				}
			case protocol.PhaseChanged:
				if e.Phase == "plan" && e.Gate == "skip" {
					sawPhase = true
				}
			}
		}
	}
}
