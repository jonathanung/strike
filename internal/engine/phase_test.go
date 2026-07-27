package engine_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/config"
	"github.com/jonathanung/strike-cli/internal/engine"
	"github.com/jonathanung/strike-cli/internal/permission"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/provider"
	"github.com/jonathanung/strike-cli/internal/tool"
)

func TestSelectPlanAgentEntersPlanPhase(t *testing.T) {
	prov := newScriptedProvider(completedStep("ok"))
	eng := engine.New(engine.Options{
		SessionID:       "phase-select-plan",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Agents: []engine.Agent{
			{Name: "build"},
			{Name: "plan"},
		},
		Workflows: []config.Workflow{config.BuiltinPlanImplement()},
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
		Workflows:    []config.Workflow{config.BuiltinPlanImplement()},
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
	exitArgs, _ := json.Marshal(map[string]any{})
	call := provider.ToolCall{ID: "ex1", Name: "exit_plan_mode", Args: exitArgs}
	prov := newScriptedProvider(
		toolCallStep(call),
		completedStep("implementing"),
	)
	eng := engine.New(engine.Options{
		SessionID:       "phase-exit-implement",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tool.NewExitPlanMode()),
		Agents: []engine.Agent{
			{Name: "build"},
			{Name: "plan"},
		},
		InitialAgent: "plan",
		Workflows:    []config.Workflow{config.BuiltinPlanImplement()},
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
		"agent": "orchestrator",
	})
	call := provider.ToolCall{ID: "ex-orch", Name: "exit_plan_mode", Args: exitArgs}
	prov := newScriptedProvider(
		toolCallStep(call),
		completedStep("delegating"),
	)
	eng := engine.New(engine.Options{
		SessionID:       "phase-exit-orchestrator",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tool.NewExitPlanMode()),
		Agents: []engine.Agent{
			{Name: "build"},
			{Name: "plan"},
			{Name: "orchestrator"},
		},
		InitialAgent: "plan",
		Workflows:    []config.Workflow{config.BuiltinPlanImplement()},
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
	exitArgs, _ := json.Marshal(map[string]any{})
	call := provider.ToolCall{ID: "ex-no", Name: "exit_plan_mode", Args: exitArgs}
	// Single stream only — a follow-up stream would mean the turn continued.
	prov := newScriptedProvider(toolCallStep(call))
	eng := engine.New(engine.Options{
		SessionID:       "phase-exit-reject",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tool.NewExitPlanMode()),
		Agents: []engine.Agent{
			{Name: "build"},
			{Name: "plan"},
		},
		InitialAgent: "plan",
		Workflows:    []config.Workflow{config.BuiltinPlanImplement()},
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

func TestSelectOrchestratorFromPlanJumpsImplement(t *testing.T) {
	prov := newScriptedProvider(completedStep("ok"))
	eng := engine.New(engine.Options{
		SessionID:       "phase-select-orch",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Agents: []engine.Agent{
			{Name: "build"},
			{Name: "plan"},
			{Name: "orchestrator"},
		},
		InitialAgent: "plan",
		Workflows:    []config.Workflow{config.BuiltinPlanImplement()},
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

	var phase protocol.PhaseChanged
	var agent string
	deadline := time.After(5 * time.Second)
	for phase.Phase != "implement" || agent != "orchestrator" {
		select {
		case <-deadline:
			t.Fatalf("timeout phase=%#v agent=%q", phase, agent)
		case ev := <-eng.Events():
			switch e := ev.(type) {
			case protocol.PhaseChanged:
				phase = e
			case protocol.AgentSelected:
				agent = e.Name
			}
		}
	}
}

func TestCheckGateCommand(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ok")
	wf := config.Workflow{
		Name: "checked",
		Phases: []config.Phase{
			{
				Name:  "prep",
				Agent: "build",
				Exit:  config.ExitGate{Type: config.GateCheck, Command: "touch ok"},
			},
			{
				Name:  "done",
				Agent: "build",
				Exit:  config.ExitGate{Type: config.GateAgent},
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
	eng := engine.New(engine.Options{
		SessionID:       "phase-check",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tool.NewPhaseDone(), tool.NewEnterPlanMode()),
		WorkDir:         dir,
		Agents:          []engine.Agent{{Name: "build"}, {Name: "plan"}},
		Workflows:       []config.Workflow{wf, config.BuiltinPlanImplement()},
		DefaultWorkflow: "checked",
		Rules:           []permission.Ruleset{permission.Defaults()},
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
	deadline := time.After(10 * time.Second)
	for !sawDone {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for done phase")
		case ev := <-eng.Events():
			if p, ok := ev.(protocol.PhaseChanged); ok && p.Phase == "done" {
				sawDone = true
			}
		}
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("check command did not create marker: %v", err)
	}
}

func TestPlanSystemPromptIncludesPhaseOverlay(t *testing.T) {
	sys := captureSystemPrompt(t, engine.Options{
		WorkDir:      t.TempDir(),
		Agents:       []engine.Agent{{Name: "plan"}, {Name: "build"}},
		InitialAgent: "plan",
		Workflows:    []config.Workflow{config.BuiltinPlanImplement()},
	}, "echo", "echo")
	if !strings.Contains(sys, "Plan mode (read-only)") {
		t.Fatalf("missing plan overlay:\n%s", sys)
	}
	if !strings.Contains(sys, "Interview first") {
		t.Fatalf("missing interview duties:\n%s", sys)
	}
}
