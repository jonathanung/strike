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

func testWorkflow(name string, phases ...config.Phase) config.Workflow {
	w := config.Workflow{
		SchemaVersion: config.WorkflowSchemaVersion,
		Name:          name,
		Phases:        phases,
		Source:        config.WorkflowSourceProject,
	}
	w.Fingerprint = config.MustWorkflowFingerprint(w)
	return w
}

func drainPhaseEvents(e *Engine) {
	for {
		select {
		case <-e.Events():
		default:
			return
		}
	}
}

func TestStartWorkflowGenericAndExactlyOne(t *testing.T) {
	review := testWorkflow("review-fix",
		config.Phase{Name: "review", Agent: "build", Exit: config.ExitGate{Type: config.GateAgent}},
		config.Phase{Name: "fix", Agent: "build", Exit: config.ExitGate{Type: config.GateAgent}},
	)
	custom := testWorkflow("custom-flow",
		config.Phase{
			Name: "step-a",
			Permissions: permission.Ruleset{
				{Permission: "write", Pattern: "*", Action: permission.Deny},
			},
			Exit: config.ExitGate{Type: config.GateAgent},
		},
		config.Phase{Name: "step-b", Exit: config.ExitGate{Type: config.GateAgent}},
	)
	e := New(Options{
		SessionID: "start-one",
		Rules:     []permission.Ruleset{permission.Defaults()},
		Workflows: []config.Workflow{review, custom},
		Agents:    []Agent{{Name: "build"}},
	})

	if err := e.startWorkflow("custom-flow"); err != nil {
		t.Fatal(err)
	}
	ev := <-e.Events()
	pc, ok := ev.(protocol.PhaseChanged)
	if !ok || pc.Workflow != "custom-flow" || pc.Phase != "step-a" || pc.Index != 0 {
		t.Fatalf("start = %#v", ev)
	}
	if pc.Fingerprint == "" || pc.Source != string(config.WorkflowSourceProject) {
		t.Fatalf("identity missing: %#v", pc)
	}
	if pc.Status != "" || pc.Gate == "" {
		t.Fatalf("want healthy gate, got %#v", pc)
	}
	if !e.activeWorkflowHealthy() {
		t.Fatal("expected healthy active workflow")
	}

	// Starting another replaces the first (exactly one active).
	if err := e.startWorkflow("review-fix"); err != nil {
		t.Fatal(err)
	}
	// clear + enter
	var last protocol.PhaseChanged
	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("timeout last=%#v", last)
		case ev := <-e.Events():
			if p, ok := ev.(protocol.PhaseChanged); ok {
				last = p
				if p.Workflow == "review-fix" && p.Phase == "review" {
					goto started
				}
			}
		}
	}
started:
	if last.Fingerprint != review.Fingerprint {
		t.Fatalf("fingerprint = %q want %q", last.Fingerprint, review.Fingerprint)
	}
	if e.workflow.Name != "review-fix" || e.phaseIndex != 0 {
		t.Fatalf("state = %q/%d", e.workflow.Name, e.phaseIndex)
	}
}

func TestStartWorkflowUnknownAndInvalidIndex(t *testing.T) {
	e := New(Options{
		SessionID: "start-bad",
		Rules:     []permission.Ruleset{permission.Defaults()},
		Workflows: []config.Workflow{testWorkflow("ok", config.Phase{Name: "a", Exit: config.ExitGate{Type: config.GateAgent}})},
	})
	if err := e.startWorkflow("nope"); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("unknown = %v", err)
	}
	// Validate-before-mutate: bad index must not clear an active workflow.
	if err := e.startWorkflow("ok"); err != nil {
		t.Fatal(err)
	}
	drainPhaseEvents(e)
	if err := e.enterPhase(e.workflow, 99); err == nil {
		t.Fatal("want out of range")
	}
	if e.workflow.Name != "ok" || e.phaseIndex != 0 {
		t.Fatalf("mutated on failed enter: %q/%d", e.workflow.Name, e.phaseIndex)
	}
}

func TestAdvanceAndStopLifecycle(t *testing.T) {
	w := testWorkflow("two-step",
		config.Phase{Name: "a", Exit: config.ExitGate{Type: config.GateAgent}},
		config.Phase{Name: "b", Exit: config.ExitGate{Type: config.GateAgent}},
	)
	e := New(Options{
		SessionID: "adv-stop",
		Rules:     []permission.Ruleset{permission.Defaults()},
		Workflows: []config.Workflow{w},
	})
	e.autonomy = protocol.AutonomyAgent
	if err := e.startWorkflow("two-step"); err != nil {
		t.Fatal(err)
	}
	drainPhaseEvents(e)

	if err := e.advancePhase(context.Background()); err != nil {
		t.Fatal(err)
	}
	ev := <-e.Events()
	pc := ev.(protocol.PhaseChanged)
	if pc.Phase != "b" || pc.Index != 1 {
		t.Fatalf("advance = %#v", pc)
	}

	if err := e.advancePhase(context.Background()); err != nil {
		t.Fatal(err)
	}
	ev = <-e.Events()
	cleared := ev.(protocol.PhaseChanged)
	if cleared.Phase != "" || cleared.Workflow != "" {
		t.Fatalf("completion clear = %#v", cleared)
	}

	// Explicit stop is idempotent when inactive.
	e.stopWorkflow()
	select {
	case ev := <-e.Events():
		t.Fatalf("noop stop emitted %#v", ev)
	default:
	}

	// Start then stop.
	if err := e.startWorkflow("two-step"); err != nil {
		t.Fatal(err)
	}
	drainPhaseEvents(e)
	e.stopWorkflow()
	ev = <-e.Events()
	if p := ev.(protocol.PhaseChanged); p.Phase != "" {
		t.Fatalf("stop = %#v", p)
	}
}

func TestResumeFingerprintMatchAndMismatch(t *testing.T) {
	w := testWorkflow("bound",
		config.Phase{
			Name: "only",
			Permissions: permission.Ruleset{
				{Permission: "write", Pattern: "*", Action: permission.Deny},
			},
			Exit: config.ExitGate{Type: config.GateAgent},
		},
	)
	e := New(Options{
		SessionID: "resume-fp",
		Rules:     []permission.Ruleset{permission.Defaults()},
		Workflows: []config.Workflow{w},
	})

	// Match: healthy restore.
	e.restoreWorkflowPhase("bound", 0, "only", w.Fingerprint)
	if !e.activeWorkflowHealthy() || e.phaseRecovery != "" {
		t.Fatalf("match recovery=%q healthy=%v", e.phaseRecovery, e.activeWorkflowHealthy())
	}
	ev := <-e.Events()
	if p := ev.(protocol.PhaseChanged); p.Status != "" || p.Fingerprint != w.Fingerprint {
		t.Fatalf("match event = %#v", p)
	}
	e.stopWorkflow()
	drainPhaseEvents(e)

	// Mismatch fingerprint.
	e.restoreWorkflowPhase("bound", 0, "only", "deadbeef")
	if e.phaseRecovery != protocol.PhaseStatusMismatch {
		t.Fatalf("recovery = %q", e.phaseRecovery)
	}
	if e.activeWorkflowHealthy() {
		t.Fatal("mismatch must not be healthy")
	}
	ev = <-e.Events()
	p := ev.(protocol.PhaseChanged)
	if p.Status != protocol.PhaseStatusMismatch || p.Workflow != "bound" {
		t.Fatalf("mismatch event = %#v", p)
	}
	// Advance fail-closed.
	err := e.advancePhase(context.Background())
	if err == nil || !strings.Contains(err.Error(), "recovery") {
		t.Fatalf("advance recovery = %v", err)
	}
	// Restart clears recovery.
	if err := e.startWorkflow("bound"); err != nil {
		t.Fatal(err)
	}
	if e.phaseRecovery != "" || !e.activeWorkflowHealthy() {
		t.Fatalf("restart state recovery=%q", e.phaseRecovery)
	}
}

func TestResumeMissingWorkflow(t *testing.T) {
	e := New(Options{
		SessionID: "resume-miss",
		Rules:     []permission.Ruleset{permission.Defaults()},
	})
	e.restoreWorkflowPhase("vanished", 1, "mid", "fp1")
	if e.phaseRecovery != protocol.PhaseStatusMissing {
		t.Fatalf("recovery = %q", e.phaseRecovery)
	}
	ev := <-e.Events()
	p := ev.(protocol.PhaseChanged)
	if p.Status != protocol.PhaseStatusMissing || p.Phase != "mid" || p.Index != 1 {
		t.Fatalf("missing event = %#v", p)
	}
	e.stopWorkflow()
	ev = <-e.Events()
	if p := ev.(protocol.PhaseChanged); p.Status != "" || p.Workflow != "" {
		t.Fatalf("stop recovery = %#v", p)
	}
}

func TestResumeLegacyEmptyFingerprintBinds(t *testing.T) {
	w := testWorkflow("legacy",
		config.Phase{Name: "a", Exit: config.ExitGate{Type: config.GateAgent}},
	)
	e := New(Options{
		SessionID: "resume-legacy",
		Rules:     []permission.Ruleset{permission.Defaults()},
		Workflows: []config.Workflow{w},
	})
	e.restoreWorkflowPhase("legacy", 0, "a", "")
	if !e.activeWorkflowHealthy() {
		t.Fatal("legacy empty fp should bind current def")
	}
	drainPhaseEvents(e)
}

func TestResumeQuietStartupStillEmitsRecovery(t *testing.T) {
	e := New(Options{
		SessionID:    "resume-quiet",
		Rules:        []permission.Ruleset{permission.Defaults()},
		QuietStartup: true,
	})
	e.quietStartup = true
	e.restoreWorkflowPhase("gone", 0, "x", "fp")
	select {
	case ev := <-e.Events():
		p, ok := ev.(protocol.PhaseChanged)
		if !ok || p.Status != protocol.PhaseStatusMissing {
			t.Fatalf("want recovery emit, got %#v", ev)
		}
	default:
		t.Fatal("recovery must emit during quiet startup")
	}
}

func TestGenericAgentSwitchStopsPinnedWorkflow(t *testing.T) {
	w := testWorkflow("pinned",
		config.Phase{Name: "review", Agent: "reviewer", Exit: config.ExitGate{Type: config.GateAgent}},
	)
	e := New(Options{
		SessionID: "agent-sync",
		Rules:     []permission.Ruleset{permission.Defaults()},
		Workflows: []config.Workflow{w},
		Agents:    []Agent{{Name: "reviewer"}, {Name: "build"}},
	})
	e.agent = Agent{Name: "reviewer"}
	if err := e.startWorkflow("pinned"); err != nil {
		t.Fatal(err)
	}
	drainPhaseEvents(e)

	e.syncPhaseWithAgent("build")
	ev := <-e.Events()
	if p := ev.(protocol.PhaseChanged); p.Phase != "" {
		t.Fatalf("want stop on agent mismatch, got %#v", p)
	}
}

func waitPhaseChanged(t *testing.T, e *Engine, wantPhase string) protocol.PhaseChanged {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("timeout waiting for phase %q", wantPhase)
		case ev := <-e.Events():
			if p, ok := ev.(protocol.PhaseChanged); ok {
				if wantPhase == "" || p.Phase == wantPhase {
					return p
				}
			}
		}
	}
}

func TestPlanConvenienceAgentSyncPreserved(t *testing.T) {
	e := New(Options{
		SessionID: "plan-sync",
		Rules:     []permission.Ruleset{permission.Defaults()},
		Workflows: []config.Workflow{config.BuiltinPlanImplement()},
		Agents:    []Agent{{Name: "plan"}, {Name: "build"}, {Name: "orchestrator"}},
	})
	e.agent = Agent{Name: "build"}
	e.syncPhaseWithAgent("plan")
	p := waitPhaseChanged(t, e, "plan")
	if p.Workflow != "plan-implement" {
		t.Fatalf("plan enter = %#v", p)
	}
	// build leaves plan → implement without clearing.
	e.syncPhaseWithAgent("build")
	p = waitPhaseChanged(t, e, "implement")
	if p.Phase != "implement" {
		t.Fatalf("plan leave = %#v", p)
	}
}

func TestStartWorkflowOpAndStopOp(t *testing.T) {
	w := testWorkflow("via-op",
		config.Phase{Name: "a", Exit: config.ExitGate{Type: config.GateAgent}},
	)
	e := New(Options{
		SessionID: "ops-wf",
		Rules:     []permission.Ruleset{permission.Defaults()},
		Workflows: []config.Workflow{w},
	})
	e.handleOp(context.Background(), protocol.StartWorkflow{Name: "via-op"})
	ev := <-e.Events()
	if p, ok := ev.(protocol.PhaseChanged); !ok || p.Workflow != "via-op" {
		t.Fatalf("start op = %#v", ev)
	}
	e.handleOp(context.Background(), protocol.StopWorkflow{})
	ev = <-e.Events()
	if p, ok := ev.(protocol.PhaseChanged); !ok || p.Phase != "" {
		t.Fatalf("stop op = %#v", ev)
	}
	// Unknown emits EngineError.
	e.handleOp(context.Background(), protocol.StartWorkflow{Name: "nope"})
	ev = <-e.Events()
	if _, ok := ev.(protocol.EngineError); !ok {
		t.Fatalf("want EngineError, got %#v", ev)
	}
}

func TestEnterPhaseRejectsBeforeMutate(t *testing.T) {
	e := New(Options{
		SessionID: "validate-first",
		Rules:     []permission.Ruleset{permission.Defaults()},
	})
	bad := config.Workflow{Name: "x"} // no phases
	if err := e.enterPhase(bad, 0); err == nil {
		t.Fatal("want error")
	}
	if e.phaseIndex != -1 || e.workflow.Name != "" {
		t.Fatalf("mutated on bad target: %#v / %d", e.workflow, e.phaseIndex)
	}
}
