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

func TestCheckGateCanceledContext(t *testing.T) {
	e := New(Options{
		SessionID: "check-cancel",
		WorkDir:   t.TempDir(),
		Rules: []permission.Ruleset{
			permission.Defaults(),
			{{Permission: "phase_check", Pattern: "*", Action: permission.Allow}},
		},
	})
	e.autonomy = protocol.AutonomyChecks
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := e.runCheckGate(ctx, config.Phase{
		Name: "a",
		Exit: config.ExitGate{Command: "true"},
	})
	if err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("error = %v, want canceled", err)
	}
}

func TestCheckGateTimeout(t *testing.T) {
	e := New(Options{
		SessionID: "check-timeout",
		WorkDir:   t.TempDir(),
		Rules: []permission.Ruleset{
			permission.Defaults(),
			{{Permission: "phase_check", Pattern: "*", Action: permission.Allow}},
		},
	})
	e.autonomy = protocol.AutonomyChecks
	// Short parent deadline forces RunProcess timeout/cancel path without
	// waiting the full phaseCheckTimeout.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := e.runCheckGate(ctx, config.Phase{
		Name: "slow",
		Exit: config.ExitGate{Command: "sleep 5"},
	})
	if err == nil {
		t.Fatal("expected timeout or cancel error")
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "timed out") && !strings.Contains(msg, "canceled") {
		t.Fatalf("error = %q, want timed out or canceled", err.Error())
	}
}

func TestCheckGateTrustDenied(t *testing.T) {
	e := New(Options{
		SessionID: "check-deny",
		WorkDir:   t.TempDir(),
		Rules: []permission.Ruleset{
			{{Permission: "phase_check", Pattern: "*", Action: permission.Deny}},
		},
	})
	e.autonomy = protocol.AutonomyChecks
	err := e.runCheckGate(context.Background(), config.Phase{
		Name: "a",
		Exit: config.ExitGate{Command: "touch pwned"},
	})
	if err == nil || !strings.Contains(err.Error(), "trust denied") {
		t.Fatalf("error = %v, want trust denied", err)
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
