package engine_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/harness/engine"
	"github.com/jonathanung/strike-cli/harness/permission"
	"github.com/jonathanung/strike-cli/harness/provider"
	"github.com/jonathanung/strike-cli/harness/tool"
	"github.com/jonathanung/strike-cli/internal/enginebind"
	"github.com/jonathanung/strike-cli/internal/tools"
	"github.com/jonathanung/strike-cli/pkg/protocol"
)

func TestSelectPlanAgentEntersPlanPhase(t *testing.T) {
	prov := newScriptedProvider(completedStep("ok"))
	eng := engine.New(engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		SessionID:       "phase-select-plan",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Agents: []engine.Agent{
			{Name: "build"},
			{Name: "plan"},
		},
		Workflows: []engine.Workflow{engine.BuiltinPlanImplement()},
		Rules:     []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	waitForEvent(t, eng, func(ev protocol.Event) bool {
		_, ok := ev.(protocol.AgentSelected)
		return ok
	})

	eng.Ops() <- protocol.SelectAgent{Name: "plan"}

	var phase protocol.PhaseChanged
	deadline := time.After(5 * time.Second)
	for phase.Phase != "plan" {
		select {
		case <-deadline:
			t.Fatalf("timeout phase=%#v", phase)
		case ev := <-eng.Events():
			if e, ok := ev.(protocol.PhaseChanged); ok {
				phase = e
			}
		}
	}
	if phase.Workflow != "plan-implement" || phase.Gate != "user" {
		t.Fatalf("phase = %#v", phase)
	}
}

func TestPlanPhaseHardDeniesWrite(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "out.txt")
	if err := os.WriteFile(target, []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"filePath": "out.txt", "content": "pwned\n"})
	call := provider.ToolCall{ID: "w1", Name: "write", Args: args}
	prov := newScriptedProvider(
		toolCallStep(call),
		completedStep("done"),
	)
	baseAllow := permission.Ruleset{
		{Permission: "write", Pattern: "*", Action: permission.Allow},
		{Permission: "edit", Pattern: "*", Action: permission.Allow},
		{Permission: "read", Pattern: "*", Action: permission.Allow},
	}
	eng := engine.New(engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		SessionID:       "phase-deny-write",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tool.NewWrite()),
		WorkDir:         dir,
		Agents: []engine.Agent{
			{Name: "build"},
			{Name: "plan"},
		},
		InitialAgent: "plan",
		Workflows:    []engine.Workflow{engine.BuiltinPlanImplement()},
		Rules:        []permission.Ruleset{permission.Defaults(), baseAllow},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	waitForEvent(t, eng, func(ev protocol.Event) bool {
		p, ok := ev.(protocol.PhaseChanged)
		return ok && p.Phase == "plan"
	})

	eng.Ops() <- protocol.UserInput{Text: "write the file"}
	var end protocol.ToolCallEnd
	deadline := time.After(10 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out")
		case ev := <-eng.Events():
			switch e := ev.(type) {
			case protocol.PermissionAsked:
				t.Fatalf("unexpected PermissionAsked under plan phase deny: %#v", e)
			case protocol.ToolCallEnd:
				if e.CallID == "w1" {
					end = e
					goto done
				}
			}
		}
	}
done:
	if !end.IsError {
		t.Fatalf("write should fail under plan phase: %#v", end)
	}
	if !strings.Contains(strings.ToLower(end.Output), "den") {
		t.Fatalf("output should mention deny: %q", end.Output)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "original\n" {
		t.Fatalf("file mutated: %q", got)
	}
}

func TestExitPlanModeAdvancesToImplement(t *testing.T) {
	exitArgs, _ := json.Marshal(map[string]any{
		"legacy_text": "1. implement the feature",
	})
	call := provider.ToolCall{ID: "ex1", Name: "exit_plan_mode", Args: exitArgs}
	prov := newScriptedProvider(
		toolCallStep(call),
		completedStep("implementing"),
	)
	eng := engine.New(engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		SessionID:       "phase-exit-implement",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tools.NewExitPlanMode()),
		Agents: []engine.Agent{
			{Name: "build"},
			{Name: "plan"},
		},
		InitialAgent: "plan",
		Workflows:    []engine.Workflow{engine.BuiltinPlanImplement()},
		Rules:        []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	waitForEvent(t, eng, func(ev protocol.Event) bool {
		p, ok := ev.(protocol.PhaseChanged)
		return ok && p.Phase == "plan"
	})

	eng.Ops() <- protocol.UserInput{Text: "exit plan"}
	var sawImplement bool
	var sawBuild bool
	var endOK bool
	deadline := time.After(10 * time.Second)
	for !sawImplement || !endOK || !sawBuild {
		select {
		case <-deadline:
			t.Fatalf("timeout implement=%v build=%v end=%v", sawImplement, sawBuild, endOK)
		case ev := <-eng.Events():
			switch e := ev.(type) {
			case protocol.QuestionAsked:
				eng.Ops() <- protocol.QuestionReply{RequestID: e.RequestID, Answers: []string{"Yes"}}
			case protocol.PhaseChanged:
				if e.Phase == "implement" {
					sawImplement = true
				}
			case protocol.AgentSelected:
				if e.Name == "build" {
					sawBuild = true
				}
			case protocol.ToolCallEnd:
				if e.CallID == "ex1" && !e.IsError {
					endOK = true
				}
			}
		}
	}
}

func TestExitPlanModeRoutesToOrchestrator(t *testing.T) {
	exitArgs, _ := json.Marshal(map[string]any{
		"agent":       "orchestrator",
		"legacy_text": "multi-area plan",
	})
	call := provider.ToolCall{ID: "ex-orch", Name: "exit_plan_mode", Args: exitArgs}
	prov := newScriptedProvider(
		toolCallStep(call),
		completedStep("delegating"),
	)
	eng := engine.New(engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		SessionID:       "phase-exit-orchestrator",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tools.NewExitPlanMode()),
		Agents: []engine.Agent{
			{Name: "build"},
			{Name: "plan"},
			{Name: "orchestrator"},
		},
		InitialAgent: "plan",
		Workflows:    []engine.Workflow{engine.BuiltinPlanImplement()},
		Rules:        []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	waitForEvent(t, eng, func(ev protocol.Event) bool {
		p, ok := ev.(protocol.PhaseChanged)
		return ok && p.Phase == "plan"
	})

	eng.Ops() <- protocol.UserInput{Text: "exit to orchestrator"}
	var sawImplement bool
	var sawOrch bool
	var endOK bool
	deadline := time.After(10 * time.Second)
	for !sawImplement || !sawOrch || !endOK {
		select {
		case <-deadline:
			t.Fatalf("timeout implement=%v orch=%v end=%v", sawImplement, sawOrch, endOK)
		case ev := <-eng.Events():
			switch e := ev.(type) {
			case protocol.QuestionAsked:
				eng.Ops() <- protocol.QuestionReply{RequestID: e.RequestID, Answers: []string{"Yes"}}
			case protocol.PhaseChanged:
				if e.Phase == "implement" {
					sawImplement = true
				}
			case protocol.AgentSelected:
				if e.Name == "orchestrator" {
					sawOrch = true
				}
			case protocol.ToolCallEnd:
				if e.CallID == "ex-orch" {
					if e.IsError {
						t.Fatalf("exit_plan_mode error: %s", e.Output)
					}
					endOK = true
					if !strings.Contains(e.Output, "orchestrator") {
						t.Fatalf("tool output missing orchestrator: %q", e.Output)
					}
				}
			}
		}
	}
}

// TestPlanRejectInterruptsTurn: declining the plan exit gate settles the tool
// as an error and ends the turn with stopReason interrupted.
func TestPlanRejectInterruptsTurn(t *testing.T) {
	exitArgs, _ := json.Marshal(map[string]any{
		"legacy_text": "do not implement yet",
	})
	call := provider.ToolCall{ID: "ex-no", Name: "exit_plan_mode", Args: exitArgs}
	// Single stream only — a follow-up stream would mean the turn continued.
	prov := newScriptedProvider(toolCallStep(call))
	eng := engine.New(engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		SessionID:       "phase-exit-reject",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tools.NewExitPlanMode()),
		Agents: []engine.Agent{
			{Name: "build"},
			{Name: "plan"},
		},
		InitialAgent: "plan",
		Workflows:    []engine.Workflow{engine.BuiltinPlanImplement()},
		Rules:        []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	waitForEvent(t, eng, func(ev protocol.Event) bool {
		p, ok := ev.(protocol.PhaseChanged)
		return ok && p.Phase == "plan"
	})

	eng.Ops() <- protocol.UserInput{Text: "exit plan"}
	var (
		sawToolEnd bool
		toolOut    string
		stopReason string
		stillPlan  = true
	)
	deadline := time.After(10 * time.Second)
	for stopReason == "" {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for interrupted turn after plan reject")
		case ev := <-eng.Events():
			switch e := ev.(type) {
			case protocol.QuestionAsked:
				eng.Ops() <- protocol.QuestionReply{RequestID: e.RequestID, Answers: []string{"No"}}
			case protocol.PhaseChanged:
				if e.Phase == "implement" {
					stillPlan = false
				}
			case protocol.ToolCallEnd:
				if e.CallID != "ex-no" {
					continue
				}
				sawToolEnd = true
				toolOut = e.Output
				if !e.IsError {
					t.Error("plan reject should be an error tool result")
				}
			case protocol.TurnCompleted:
				stopReason = e.StopReason
			case protocol.EngineError:
				t.Fatalf("engine error: %s", e.Message)
			}
		}
	}
	if !sawToolEnd {
		t.Error("missing ToolCallEnd for rejected plan exit")
	}
	if !stillPlan {
		t.Error("phase advanced to implement after plan reject")
	}
	if !strings.Contains(strings.ToLower(toolOut), "declined") &&
		!strings.Contains(strings.ToLower(toolOut), "rejected") {
		t.Errorf("tool output = %q, want decline/reject feedback", toolOut)
	}
	if stopReason != "interrupted" {
		t.Errorf("stop reason = %q, want interrupted", stopReason)
	}
}

// TestSelectOrchestratorFromPlanAbandonsWithoutHandoff: manual agent selection
// cannot enter implement without unified plan handoff — it clears the plan
// workflow instead.
func TestSelectOrchestratorFromPlanAbandonsWithoutHandoff(t *testing.T) {
	prov := newScriptedProvider(completedStep("ok"))
	eng := engine.New(engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		SessionID:       "phase-select-orch",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Agents: []engine.Agent{
			{Name: "build"},
			{Name: "plan"},
			{Name: "orchestrator"},
		},
		InitialAgent: "plan",
		Workflows:    []engine.Workflow{engine.BuiltinPlanImplement()},
		Rules:        []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	waitForEvent(t, eng, func(ev protocol.Event) bool {
		p, ok := ev.(protocol.PhaseChanged)
		return ok && p.Phase == "plan"
	})

	eng.Ops() <- protocol.SelectAgent{Name: "orchestrator"}

	var cleared bool
	var agent string
	deadline := time.After(5 * time.Second)
	for !cleared || agent != "orchestrator" {
		select {
		case <-deadline:
			t.Fatalf("timeout cleared=%v agent=%q", cleared, agent)
		case ev := <-eng.Events():
			switch e := ev.(type) {
			case protocol.PhaseChanged:
				if e.Phase == "" {
					cleared = true
				}
				if e.Phase == "implement" {
					t.Fatal("manual agent select must not enter implement without handoff")
				}
			case protocol.AgentSelected:
				agent = e.Name
			}
		}
	}
}

func TestCheckGateCommand(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ok")
	wf := engine.Workflow{
		Name: "checked",
		Phases: []engine.Phase{
			{
				Name:  "prep",
				Agent: "build",
				// Authored type is ignored at runtime; autonomy=checks drives the gate.
				Exit: engine.ExitGate{Type: engine.GateAgent, Command: "touch ok"},
			},
			{
				Name:  "done",
				Agent: "build",
				Exit:  engine.ExitGate{Type: engine.GateAgent},
			},
		},
	}
	args, _ := json.Marshal(map[string]any{})
	enterCall := provider.ToolCall{ID: "en1", Name: "enter_plan_mode", Args: args}
	doneCall := provider.ToolCall{ID: "pd1", Name: "phase_done", Args: args}
	// enter_plan_mode uses DefaultWorkflow ("checked") then phase_done advances.
	// After enter, one Stream returns phase_done; after advance, one more Stream completes.
	prov := newScriptedProvider(
		toolCallStep(enterCall),
		toolCallStep(doneCall),
		completedStep("done"),
	)
	allowCheck := permission.Ruleset{
		{Permission: "phase_check", Pattern: "*", Action: permission.Allow},
	}
	eng := engine.New(engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		SessionID:       "phase-check",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		InitialAutonomy: protocol.AutonomyChecks,
		Registry:        tool.NewRegistry(tools.NewPhaseDone(), tools.NewEnterPlanMode()),
		WorkDir:         dir,
		Agents:          []engine.Agent{{Name: "build"}, {Name: "plan"}},
		Workflows:       []engine.Workflow{wf, engine.BuiltinPlanImplement()},
		DefaultWorkflow: "checked",
		Rules:           []permission.Ruleset{permission.Defaults(), allowCheck},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	waitForEvent(t, eng, func(ev protocol.Event) bool {
		_, ok := ev.(protocol.AgentSelected)
		return ok
	})

	eng.Ops() <- protocol.UserInput{Text: "run gates"}
	var sawDone bool
	var prepGate string
	deadline := time.After(10 * time.Second)
	for !sawDone {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for done phase")
		case ev := <-eng.Events():
			if p, ok := ev.(protocol.PhaseChanged); ok {
				if p.Phase == "prep" {
					prepGate = p.Gate
				}
				if p.Phase == "done" {
					sawDone = true
				}
			}
		}
	}
	if prepGate != "check" {
		t.Fatalf("prep gate = %q, want check (autonomy authoritative)", prepGate)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("check command did not create marker: %v", err)
	}
}

// TestAutonomyAgentAdvancesWithoutUserPrompt: agent mode self-affirms via
// phase_done even when the workflow authors a user exit gate.
func TestAutonomyAgentAdvancesWithoutUserPrompt(t *testing.T) {
	wf := engine.Workflow{
		Name: "user-authored",
		Phases: []engine.Phase{
			{Name: "a", Agent: "build", Exit: engine.ExitGate{Type: engine.GateUser}},
			{Name: "b", Agent: "build", Exit: engine.ExitGate{Type: engine.GateUser}},
		},
	}
	args, _ := json.Marshal(map[string]any{})
	enterCall := provider.ToolCall{ID: "en-agent", Name: "enter_plan_mode", Args: args}
	doneCall := provider.ToolCall{ID: "pd-agent", Name: "phase_done", Args: args}
	prov := newScriptedProvider(
		toolCallStep(enterCall),
		toolCallStep(doneCall),
		completedStep("on b"),
	)
	eng := engine.New(engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		SessionID:       "phase-autonomy-agent",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		InitialAutonomy: protocol.AutonomyAgent,
		Registry:        tool.NewRegistry(tools.NewPhaseDone(), tools.NewEnterPlanMode()),
		Agents:          []engine.Agent{{Name: "build"}, {Name: "plan"}},
		Workflows:       []engine.Workflow{wf, engine.BuiltinPlanImplement()},
		DefaultWorkflow: "user-authored",
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	waitForEvent(t, eng, func(ev protocol.Event) bool {
		_, ok := ev.(protocol.AgentSelected)
		return ok
	})

	eng.Ops() <- protocol.UserInput{Text: "advance under agent autonomy"}
	var sawB bool
	deadline := time.After(10 * time.Second)
	for !sawB {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for phase b")
		case ev := <-eng.Events():
			switch e := ev.(type) {
			case protocol.QuestionAsked:
				t.Fatalf("agent autonomy must not prompt user: %#v", e)
			case protocol.PhaseChanged:
				if e.Phase == "a" && e.Gate != "agent" {
					t.Fatalf("phase a gate = %q, want agent", e.Gate)
				}
				if e.Phase == "b" {
					sawB = true
				}
			case protocol.EngineError:
				t.Fatalf("engine error: %s", e.Message)
			}
		}
	}
}

// TestAutonomySkipAllAdvancesWithoutApproval: skip-all bypasses workflow gates
// without touching tool permissions (write still denied under plan phase).
func TestAutonomySkipAllAdvancesWithoutApproval(t *testing.T) {
	args, _ := json.Marshal(map[string]any{})
	exitCall := provider.ToolCall{ID: "ex-skip", Name: "exit_plan_mode", Args: args}
	prov := newScriptedProvider(
		toolCallStep(exitCall),
		completedStep("implementing"),
	)
	eng := engine.New(engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		SessionID:       "phase-autonomy-skip",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		InitialAutonomy: protocol.AutonomySkipAll,
		Registry:        tool.NewRegistry(tools.NewExitPlanMode()),
		Agents: []engine.Agent{
			{Name: "build"},
			{Name: "plan"},
		},
		InitialAgent: "plan",
		Workflows:    []engine.Workflow{engine.BuiltinPlanImplement()},
		Rules:        []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	waitForEvent(t, eng, func(ev protocol.Event) bool {
		p, ok := ev.(protocol.PhaseChanged)
		return ok && p.Phase == "plan" && p.Gate == "skip"
	})

	eng.Ops() <- protocol.UserInput{Text: "exit plan under skip-all"}
	var sawImplement bool
	deadline := time.After(10 * time.Second)
	for !sawImplement {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for implement under skip-all")
		case ev := <-eng.Events():
			switch e := ev.(type) {
			case protocol.QuestionAsked:
				t.Fatalf("skip-all must not prompt: %#v", e)
			case protocol.PhaseChanged:
				if e.Phase == "implement" {
					sawImplement = true
				}
			case protocol.ToolCallEnd:
				if e.CallID == "ex-skip" && e.IsError {
					t.Fatalf("exit_plan_mode error under skip-all: %s", e.Output)
				}
			case protocol.EngineError:
				t.Fatalf("engine error: %s", e.Message)
			}
		}
	}
}

// TestAutonomyChecksEmptyCommandFailsClosed: checks mode without a command
// refuses to advance.
func TestAutonomyChecksEmptyCommandFailsClosed(t *testing.T) {
	wf := engine.Workflow{
		Name: "no-cmd",
		Phases: []engine.Phase{
			{Name: "a", Agent: "build", Exit: engine.ExitGate{Type: engine.GateAgent}},
			{Name: "b", Agent: "build", Exit: engine.ExitGate{Type: engine.GateAgent}},
		},
	}
	args, _ := json.Marshal(map[string]any{})
	enterCall := provider.ToolCall{ID: "en-empty", Name: "enter_plan_mode", Args: args}
	doneCall := provider.ToolCall{ID: "pd-empty", Name: "phase_done", Args: args}
	prov := newScriptedProvider(
		toolCallStep(enterCall),
		toolCallStep(doneCall),
		completedStep("still a"),
	)
	eng := engine.New(engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		SessionID:       "phase-checks-empty",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		InitialAutonomy: protocol.AutonomyChecks,
		Registry:        tool.NewRegistry(tools.NewPhaseDone(), tools.NewEnterPlanMode()),
		Agents:          []engine.Agent{{Name: "build"}, {Name: "plan"}},
		Workflows:       []engine.Workflow{wf, engine.BuiltinPlanImplement()},
		DefaultWorkflow: "no-cmd",
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	waitForEvent(t, eng, func(ev protocol.Event) bool {
		_, ok := ev.(protocol.AgentSelected)
		return ok
	})

	eng.Ops() <- protocol.UserInput{Text: "try advance"}
	var (
		toolErr  string
		advanced bool
		turnDone bool
	)
	deadline := time.After(10 * time.Second)
	for !turnDone {
		select {
		case <-deadline:
			t.Fatal("timeout")
		case ev := <-eng.Events():
			switch e := ev.(type) {
			case protocol.PhaseChanged:
				if e.Phase == "b" {
					advanced = true
				}
			case protocol.ToolCallEnd:
				if e.CallID == "pd-empty" {
					if !e.IsError {
						t.Fatal("phase_done should error on empty check command")
					}
					toolErr = e.Output
				}
			case protocol.TurnCompleted:
				turnDone = true
			}
		}
	}
	if advanced {
		t.Fatal("phase advanced despite empty check command")
	}
	if !strings.Contains(strings.ToLower(toolErr), "empty") {
		t.Fatalf("tool error = %q, want empty command mention", toolErr)
	}
}

// TestAutonomyChecksFailingCommandReportsFailure.
func TestAutonomyChecksFailingCommandReportsFailure(t *testing.T) {
	wf := engine.Workflow{
		Name: "fail-cmd",
		Phases: []engine.Phase{
			{Name: "a", Agent: "build", Exit: engine.ExitGate{Command: "echo boom >&2; exit 7"}},
			{Name: "b", Agent: "build", Exit: engine.ExitGate{Type: engine.GateAgent}},
		},
	}
	args, _ := json.Marshal(map[string]any{})
	enterCall := provider.ToolCall{ID: "en-fail", Name: "enter_plan_mode", Args: args}
	doneCall := provider.ToolCall{ID: "pd-fail", Name: "phase_done", Args: args}
	prov := newScriptedProvider(
		toolCallStep(enterCall),
		toolCallStep(doneCall),
		completedStep("still a"),
	)
	allowCheck := permission.Ruleset{
		{Permission: "phase_check", Pattern: "*", Action: permission.Allow},
	}
	eng := engine.New(engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		SessionID:       "phase-checks-fail",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		InitialAutonomy: protocol.AutonomyChecks,
		Registry:        tool.NewRegistry(tools.NewPhaseDone(), tools.NewEnterPlanMode()),
		Agents:          []engine.Agent{{Name: "build"}, {Name: "plan"}},
		Workflows:       []engine.Workflow{wf, engine.BuiltinPlanImplement()},
		DefaultWorkflow: "fail-cmd",
		Rules:           []permission.Ruleset{permission.Defaults(), allowCheck},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	waitForEvent(t, eng, func(ev protocol.Event) bool {
		_, ok := ev.(protocol.AgentSelected)
		return ok
	})

	eng.Ops() <- protocol.UserInput{Text: "try advance"}
	var toolErr string
	var advanced bool
	deadline := time.After(10 * time.Second)
	for toolErr == "" && !advanced {
		select {
		case <-deadline:
			t.Fatal("timeout")
		case ev := <-eng.Events():
			switch e := ev.(type) {
			case protocol.PhaseChanged:
				if e.Phase == "b" {
					advanced = true
				}
			case protocol.ToolCallEnd:
				if e.CallID == "pd-fail" {
					if !e.IsError {
						t.Fatal("phase_done should error on failing check")
					}
					toolErr = e.Output
				}
			}
		}
	}
	if advanced {
		t.Fatal("phase advanced despite failing check")
	}
	if !strings.Contains(strings.ToLower(toolErr), "check failed") &&
		!strings.Contains(toolErr, "boom") {
		t.Fatalf("tool error = %q, want check failure detail", toolErr)
	}
}

func TestPlanSystemPromptIncludesPhaseOverlay(t *testing.T) {
	sys := captureSystemPrompt(t, engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		WorkDir:         t.TempDir(),
		Agents:          []engine.Agent{{Name: "plan"}, {Name: "build"}},
		InitialAgent:    "plan",
		Workflows:       []engine.Workflow{engine.BuiltinPlanImplement()},
	}, "echo", "echo")
	if !strings.Contains(sys, "Plan mode (read-only)") {
		t.Fatalf("missing plan overlay:\n%s", sys)
	}
	if !strings.Contains(sys, "Interview first") {
		t.Fatalf("missing interview duties:\n%s", sys)
	}
}

// TestPhaseAgentTransitionKeepsSessionModel: workflow phase agent pins must
// not thrash provider/model. Explicit SelectAgent still applies pins.
func TestPhaseAgentTransitionKeepsSessionModel(t *testing.T) {
	const sessionModel = "session-model"
	// Custom workflow so phase agents carry pins while startup stays on an
	// unpinned build (startup SelectAgent would otherwise apply build pins).
	wf := engine.Workflow{
		Name: "sticky-plan-implement",
		Phases: []engine.Phase{
			{
				Name:  "plan",
				Agent: "planner",
				Permissions: permission.Ruleset{
					{Permission: "write", Pattern: "*", Action: permission.Deny},
					{Permission: "edit", Pattern: "*", Action: permission.Deny},
				},
				Exit: engine.ExitGate{Type: engine.GateUser},
			},
			{
				Name:  "implement",
				Agent: "coder",
				Exit:  engine.ExitGate{Type: engine.GateAgent},
			},
		},
	}
	enterArgs, _ := json.Marshal(map[string]any{})
	exitArgs, _ := json.Marshal(map[string]any{
		"legacy_text": "sticky model handoff plan",
	})
	enterCall := provider.ToolCall{ID: "en-sticky", Name: "enter_plan_mode", Args: enterArgs}
	// exit_plan_mode is required to leave plan convenience; routes implementer
	// while phase agent pin must not thrash the session model.
	exitCall := provider.ToolCall{ID: "ex-sticky", Name: "exit_plan_mode", Args: exitArgs}

	sessionProv := newScriptedProvider(
		toolCallStep(enterCall),
		toolCallStep(exitCall),
		completedStep("implementing"),
	)
	planProv := newScriptedProvider(completedStep("plan-pin-should-not-run"))
	coderProv := newScriptedProvider(completedStep("coder-pin-should-not-run"))
	providers := map[string]*scriptedProvider{
		"session":  sessionProv,
		"planpin":  planProv,
		"coderpin": coderProv,
	}
	defaults := map[string]string{
		"session":  sessionModel,
		"planpin":  "plan-pinned-model",
		"coderpin": "coder-pinned-model",
	}

	eng := engine.New(engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		SessionID:       "phase-sticky-model",
		Select:          multiProviderSelect(providers, defaults),
		InitialProvider: "session",
		InitialModel:    sessionModel,
		Registry:        tool.NewRegistry(tools.NewEnterPlanMode(), tools.NewExitPlanMode()),
		Agents: []engine.Agent{
			{Name: "build"},
			{Name: "plan"},
			{Name: "planner", Provider: "planpin", Model: "plan-pinned-model"},
			{Name: "coder", Provider: "coderpin", Model: "coder-pinned-model"},
		},
		InitialAgent:    "build",
		Workflows:       []engine.Workflow{wf, engine.BuiltinPlanImplement()},
		DefaultWorkflow: "sticky-plan-implement",
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	// Startup model.
	waitForEvent(t, eng, func(ev protocol.Event) bool {
		ms, ok := ev.(protocol.ModelSelected)
		return ok && ms.Provider == "session" && ms.Model == sessionModel
	})

	eng.Ops() <- protocol.UserInput{Text: "plan then implement"}

	var sawPlan, sawImplement, sawPlanner, sawBuild, turnDone bool
	var modelAfterPhase []protocol.ModelSelected
	deadline := time.After(15 * time.Second)
	for !sawImplement || !sawBuild || !turnDone {
		select {
		case <-deadline:
			t.Fatalf("timeout plan=%v implement=%v planner=%v build=%v turn=%v models=%v",
				sawPlan, sawImplement, sawPlanner, sawBuild, turnDone, modelAfterPhase)
		case ev := <-eng.Events():
			switch e := ev.(type) {
			case protocol.QuestionAsked:
				eng.Ops() <- protocol.QuestionReply{RequestID: e.RequestID, Answers: []string{"Yes"}}
			case protocol.PhaseChanged:
				switch e.Phase {
				case "plan":
					sawPlan = true
				case "implement":
					sawImplement = true
				}
			case protocol.AgentSelected:
				switch e.Name {
				case "planner":
					sawPlanner = true
				case "build":
					// exit_plan_mode routes to build (simple legacy plan).
					if sawPlan {
						sawBuild = true
					}
				}
			case protocol.ModelSelected:
				// Ignore startup; collect any model changes after phase work starts.
				if sawPlan || sawPlanner {
					modelAfterPhase = append(modelAfterPhase, e)
				}
			case protocol.TurnCompleted:
				turnDone = true
			case protocol.EngineError:
				t.Fatalf("engine error: %s", e.Message)
			}
		}
	}

	if !sawPlan || !sawPlanner {
		t.Fatalf("expected plan phase+agent; plan=%v planner=%v", sawPlan, sawPlanner)
	}
	for _, ms := range modelAfterPhase {
		if ms.Provider != "session" || ms.Model != sessionModel {
			t.Fatalf("phase agent transition changed model: %+v (want session/%s)", ms, sessionModel)
		}
	}
	// Pinned providers must not have been selected for streaming.
	select {
	case req := <-planProv.requests:
		t.Fatalf("plan-pinned provider received Stream: model=%q", req.Model)
	default:
	}
	select {
	case req := <-coderProv.requests:
		t.Fatalf("coder-pinned provider received Stream: model=%q", req.Model)
	default:
	}

	// Explicit user SelectAgent still applies model pins.
	eng.Ops() <- protocol.SelectAgent{Name: "planner"}
	ms := waitForEvent(t, eng, func(ev protocol.Event) bool {
		m, ok := ev.(protocol.ModelSelected)
		return ok && m.Provider == "planpin" && m.Model == "plan-pinned-model"
	}).(protocol.ModelSelected)
	if ms.Provider != "planpin" || ms.Model != "plan-pinned-model" {
		t.Fatalf("explicit SelectAgent model = %+v, want planpin/plan-pinned-model", ms)
	}
}

// TestPhaseDoneAdvanceKeepsSessionModel: phase_done advancing to a phase with
// a different agent pin must not emit a model change.
func TestPhaseDoneAdvanceKeepsSessionModel(t *testing.T) {
	const sessionModel = "sticky-session"
	wf := engine.Workflow{
		Name: "two-agent",
		Phases: []engine.Phase{
			{Name: "first", Agent: "alpha", Exit: engine.ExitGate{Type: engine.GateAgent}},
			{Name: "second", Agent: "beta", Exit: engine.ExitGate{Type: engine.GateAgent}},
		},
	}
	args, _ := json.Marshal(map[string]any{})
	enterCall := provider.ToolCall{ID: "en-pd", Name: "enter_plan_mode", Args: args}
	doneCall := provider.ToolCall{ID: "pd-sticky", Name: "phase_done", Args: args}

	sessionProv := newScriptedProvider(
		toolCallStep(enterCall),
		toolCallStep(doneCall),
		completedStep("on second"),
	)
	alphaProv := newScriptedProvider(completedStep("alpha-pin"))
	betaProv := newScriptedProvider(completedStep("beta-pin"))
	providers := map[string]*scriptedProvider{
		"session": sessionProv,
		"alpha":   alphaProv,
		"beta":    betaProv,
	}
	defaults := map[string]string{
		"session": sessionModel,
		"alpha":   "alpha-model",
		"beta":    "beta-model",
	}

	eng := engine.New(engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		SessionID:       "phase-done-sticky",
		Select:          multiProviderSelect(providers, defaults),
		InitialProvider: "session",
		InitialModel:    sessionModel,
		// Agent autonomy: phase_done self-affirms without a user prompt.
		InitialAutonomy: protocol.AutonomyAgent,
		Registry:        tool.NewRegistry(tools.NewEnterPlanMode(), tools.NewPhaseDone()),
		Agents: []engine.Agent{
			{Name: "build"},
			{Name: "plan"},
			{Name: "alpha", Provider: "alpha", Model: "alpha-model"},
			{Name: "beta", Provider: "beta", Model: "beta-model"},
		},
		Workflows:       []engine.Workflow{wf, engine.BuiltinPlanImplement()},
		DefaultWorkflow: "two-agent",
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	waitForEvent(t, eng, func(ev protocol.Event) bool {
		ms, ok := ev.(protocol.ModelSelected)
		return ok && ms.Provider == "session" && ms.Model == sessionModel
	})

	eng.Ops() <- protocol.UserInput{Text: "advance"}

	var sawSecond, sawBeta bool
	var badModels []protocol.ModelSelected
	deadline := time.After(15 * time.Second)
	for !sawSecond || !sawBeta {
		select {
		case <-deadline:
			t.Fatalf("timeout second=%v beta=%v badModels=%v", sawSecond, sawBeta, badModels)
		case ev := <-eng.Events():
			switch e := ev.(type) {
			case protocol.PhaseChanged:
				if e.Phase == "second" {
					sawSecond = true
				}
			case protocol.AgentSelected:
				if e.Name == "beta" {
					sawBeta = true
				}
			case protocol.ModelSelected:
				if e.Provider != "session" || e.Model != sessionModel {
					badModels = append(badModels, e)
				}
			case protocol.EngineError:
				t.Fatalf("engine error: %s", e.Message)
			}
		}
	}
	if len(badModels) > 0 {
		t.Fatalf("phase_done agent transition changed model: %v", badModels)
	}
	select {
	case req := <-alphaProv.requests:
		t.Fatalf("alpha-pinned provider streamed: %q", req.Model)
	default:
	}
	select {
	case req := <-betaProv.requests:
		t.Fatalf("beta-pinned provider streamed: %q", req.Model)
	default:
	}
}
