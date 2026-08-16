package engine_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/harness/engine"
	"github.com/jonathanung/strike-cli/harness/permission"
	"github.com/jonathanung/strike-cli/harness/provider"
	"github.com/jonathanung/strike-cli/harness/tool"
	"github.com/jonathanung/strike-cli/internal/enginebind"
	"github.com/jonathanung/strike-cli/internal/persist/plan"
	"github.com/jonathanung/strike-cli/internal/tools"
	"github.com/jonathanung/strike-cli/pkg/protocol"
)

func openTestPlanStore(t *testing.T) *plan.Store {
	t.Helper()
	s, err := plan.Open(t.TempDir(), "handoff-proj")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestHandoffStructuredPlanApprovesAndEmits(t *testing.T) {
	store := openTestPlanStore(t)
	p, err := store.Create("root-h1", "Ship it", []plan.SectionInput{
		{Title: "Do", Body: "the work"},
	})
	if err != nil {
		t.Fatal(err)
	}

	exitArgs, _ := json.Marshal(map[string]any{
		"plan_id":          p.ID,
		"expected_version": p.Version,
	})
	call := provider.ToolCall{ID: "ex-struct", Name: "exit_plan_mode", Args: exitArgs}
	prov := newScriptedProvider(
		toolCallStep(call),
		completedStep("implementing"),
	)
	eng := engine.New(engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		SessionID:       "root-h1",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tools.NewExitPlanMode()),
		PlanStore:       enginebind.Plan(store),
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
		ph, ok := ev.(protocol.PhaseChanged)
		return ok && ph.Phase == "plan"
	})

	eng.Ops() <- protocol.UserInput{Text: "handoff"}
	var (
		sawHandoff   bool
		sawImplement bool
		source       string
	)
	deadline := time.After(10 * time.Second)
	for !sawHandoff || !sawImplement {
		select {
		case <-deadline:
			t.Fatalf("timeout handoff=%v implement=%v", sawHandoff, sawImplement)
		case ev := <-eng.Events():
			switch e := ev.(type) {
			case protocol.QuestionAsked:
				eng.Ops() <- protocol.QuestionReply{RequestID: e.RequestID, Answers: []string{"Yes"}}
			case protocol.PlanHandoff:
				sawHandoff = true
				source = e.ApprovalSource
				if e.PlanID != p.ID {
					t.Fatalf("PlanID = %q", e.PlanID)
				}
				if e.PlanVersion < p.Version {
					t.Fatalf("PlanVersion = %d", e.PlanVersion)
				}
				if e.ApprovalSource != protocol.PlanApprovalUser {
					t.Fatalf("source = %q", e.ApprovalSource)
				}
			case protocol.PhaseChanged:
				if e.Phase == "implement" {
					sawImplement = true
				}
			case protocol.ToolCallEnd:
				if e.CallID == "ex-struct" && e.IsError {
					t.Fatalf("exit error: %s", e.Output)
				}
			case protocol.EngineError:
				t.Fatalf("engine error: %s", e.Message)
			}
		}
	}
	if source != protocol.PlanApprovalUser {
		t.Fatalf("source = %q", source)
	}
	got, ok, err := store.Get(p.ID)
	if err != nil || !ok {
		t.Fatalf("get plan: ok=%v err=%v", ok, err)
	}
	if got.Status != plan.StatusApproved {
		t.Fatalf("status = %q, want approved", got.Status)
	}
}

func TestHandoffRejectLeavesPlanDraft(t *testing.T) {
	store := openTestPlanStore(t)
	p, err := store.Create("root-h2", "Draft stay", nil)
	if err != nil {
		t.Fatal(err)
	}
	exitArgs, _ := json.Marshal(map[string]any{
		"plan_id":          p.ID,
		"expected_version": p.Version,
	})
	call := provider.ToolCall{ID: "ex-no", Name: "exit_plan_mode", Args: exitArgs}
	prov := newScriptedProvider(toolCallStep(call))
	eng := engine.New(engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		SessionID:       "root-h2",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tools.NewExitPlanMode()),
		PlanStore:       enginebind.Plan(store),
		Agents:          []engine.Agent{{Name: "build"}, {Name: "plan"}},
		InitialAgent:    "plan",
		Workflows:       []engine.Workflow{engine.BuiltinPlanImplement()},
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	waitForEvent(t, eng, func(ev protocol.Event) bool {
		ph, ok := ev.(protocol.PhaseChanged)
		return ok && ph.Phase == "plan"
	})

	eng.Ops() <- protocol.UserInput{Text: "no"}
	var stop string
	deadline := time.After(10 * time.Second)
	for stop == "" {
		select {
		case <-deadline:
			t.Fatal("timeout")
		case ev := <-eng.Events():
			switch e := ev.(type) {
			case protocol.QuestionAsked:
				eng.Ops() <- protocol.QuestionReply{RequestID: e.RequestID, Answers: []string{"No"}}
			case protocol.PlanHandoff:
				t.Fatal("must not emit PlanHandoff on reject")
			case protocol.PhaseChanged:
				if e.Phase == "implement" {
					t.Fatal("must not advance on reject")
				}
			case protocol.TurnCompleted:
				stop = e.StopReason
			}
		}
	}
	got, ok, err := store.Get(p.ID)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if got.Status != plan.StatusDraft {
		t.Fatalf("status = %q, want draft", got.Status)
	}
	if got.Version != p.Version {
		t.Fatalf("version changed on reject: %d → %d", p.Version, got.Version)
	}
}

func TestHandoffStaleVersionFails(t *testing.T) {
	store := openTestPlanStore(t)
	p, err := store.Create("root-h3", "Stale", nil)
	if err != nil {
		t.Fatal(err)
	}
	exitArgs, _ := json.Marshal(map[string]any{
		"plan_id":          p.ID,
		"expected_version": p.Version + 99,
	})
	call := provider.ToolCall{ID: "ex-stale", Name: "exit_plan_mode", Args: exitArgs}
	prov := newScriptedProvider(toolCallStep(call), completedStep("done"))
	eng := engine.New(engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		SessionID:       "root-h3",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		InitialAutonomy: protocol.AutonomyAgent,
		Registry:        tool.NewRegistry(tools.NewExitPlanMode()),
		PlanStore:       enginebind.Plan(store),
		Agents:          []engine.Agent{{Name: "build"}, {Name: "plan"}},
		InitialAgent:    "plan",
		Workflows:       []engine.Workflow{engine.BuiltinPlanImplement()},
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	waitForEvent(t, eng, func(ev protocol.Event) bool {
		ph, ok := ev.(protocol.PhaseChanged)
		return ok && ph.Phase == "plan"
	})

	eng.Ops() <- protocol.UserInput{Text: "stale"}
	var toolErr string
	deadline := time.After(10 * time.Second)
	for toolErr == "" {
		select {
		case <-deadline:
			t.Fatal("timeout")
		case ev := <-eng.Events():
			switch e := ev.(type) {
			case protocol.ToolCallEnd:
				if e.CallID == "ex-stale" {
					if !e.IsError {
						t.Fatal("want error on stale version")
					}
					toolErr = e.Output
				}
			case protocol.PlanHandoff:
				t.Fatal("no handoff on stale")
			}
		}
	}
	if !strings.Contains(strings.ToLower(toolErr), "conflict") &&
		!strings.Contains(toolErr, "expected") {
		t.Fatalf("tool err = %q", toolErr)
	}
}

func TestHandoffMissingPlanFails(t *testing.T) {
	store := openTestPlanStore(t)
	exitArgs, _ := json.Marshal(map[string]any{
		"plan_id":          "deadbeef",
		"expected_version": 1,
	})
	call := provider.ToolCall{ID: "ex-miss", Name: "exit_plan_mode", Args: exitArgs}
	prov := newScriptedProvider(toolCallStep(call), completedStep("done"))
	eng := engine.New(engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		SessionID:       "root-h4",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		InitialAutonomy: protocol.AutonomyAgent,
		Registry:        tool.NewRegistry(tools.NewExitPlanMode()),
		PlanStore:       enginebind.Plan(store),
		Agents:          []engine.Agent{{Name: "build"}, {Name: "plan"}},
		InitialAgent:    "plan",
		Workflows:       []engine.Workflow{engine.BuiltinPlanImplement()},
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	waitForEvent(t, eng, func(ev protocol.Event) bool {
		ph, ok := ev.(protocol.PhaseChanged)
		return ok && ph.Phase == "plan"
	})

	eng.Ops() <- protocol.UserInput{Text: "miss"}
	var toolErr string
	deadline := time.After(10 * time.Second)
	for toolErr == "" {
		select {
		case <-deadline:
			t.Fatal("timeout")
		case ev := <-eng.Events():
			if e, ok := ev.(protocol.ToolCallEnd); ok && e.CallID == "ex-miss" {
				if !e.IsError {
					t.Fatal("want miss error")
				}
				toolErr = e.Output
			}
		}
	}
	if !strings.Contains(toolErr, "not found") {
		t.Fatalf("err = %q", toolErr)
	}
}

func TestHandoffUnauthorizedOwnerFails(t *testing.T) {
	store := openTestPlanStore(t)
	p, err := store.Create("other-root", "Not yours", nil)
	if err != nil {
		t.Fatal(err)
	}
	exitArgs, _ := json.Marshal(map[string]any{
		"plan_id":          p.ID,
		"expected_version": p.Version,
	})
	call := provider.ToolCall{ID: "ex-own", Name: "exit_plan_mode", Args: exitArgs}
	prov := newScriptedProvider(toolCallStep(call), completedStep("done"))
	eng := engine.New(engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		SessionID:       "root-h5",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		InitialAutonomy: protocol.AutonomyAgent,
		Registry:        tool.NewRegistry(tools.NewExitPlanMode()),
		PlanStore:       enginebind.Plan(store),
		Agents:          []engine.Agent{{Name: "build"}, {Name: "plan"}},
		InitialAgent:    "plan",
		Workflows:       []engine.Workflow{engine.BuiltinPlanImplement()},
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	waitForEvent(t, eng, func(ev protocol.Event) bool {
		ph, ok := ev.(protocol.PhaseChanged)
		return ok && ph.Phase == "plan"
	})

	eng.Ops() <- protocol.UserInput{Text: "own"}
	var toolErr string
	deadline := time.After(10 * time.Second)
	for toolErr == "" {
		select {
		case <-deadline:
			t.Fatal("timeout")
		case ev := <-eng.Events():
			if e, ok := ev.(protocol.ToolCallEnd); ok && e.CallID == "ex-own" {
				if !e.IsError {
					t.Fatal("want owner error")
				}
				toolErr = e.Output
			}
		}
	}
	if !strings.Contains(strings.ToLower(toolErr), "own") {
		t.Fatalf("err = %q", toolErr)
	}
}

func TestPhaseDoneCannotLeavePlanPhase(t *testing.T) {
	args, _ := json.Marshal(map[string]any{})
	call := provider.ToolCall{ID: "pd-block", Name: "phase_done", Args: args}
	prov := newScriptedProvider(toolCallStep(call), completedStep("still planning"))
	eng := engine.New(engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		SessionID:       "root-pd-block",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		InitialAutonomy: protocol.AutonomyAgent,
		Registry:        tool.NewRegistry(tools.NewPhaseDone()),
		Agents:          []engine.Agent{{Name: "build"}, {Name: "plan"}},
		InitialAgent:    "plan",
		Workflows:       []engine.Workflow{engine.BuiltinPlanImplement()},
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	waitForEvent(t, eng, func(ev protocol.Event) bool {
		ph, ok := ev.(protocol.PhaseChanged)
		return ok && ph.Phase == "plan"
	})

	eng.Ops() <- protocol.UserInput{Text: "try phase_done"}
	var toolErr string
	deadline := time.After(10 * time.Second)
	for toolErr == "" {
		select {
		case <-deadline:
			t.Fatal("timeout")
		case ev := <-eng.Events():
			switch e := ev.(type) {
			case protocol.ToolCallEnd:
				if e.CallID == "pd-block" {
					if !e.IsError {
						t.Fatal("phase_done must fail on plan phase")
					}
					toolErr = e.Output
				}
			case protocol.PhaseChanged:
				if e.Phase == "implement" {
					t.Fatal("phase_done must not advance plan phase")
				}
			}
		}
	}
	if !strings.Contains(toolErr, "exit_plan_mode") && !strings.Contains(toolErr, "handoff") {
		t.Fatalf("err = %q", toolErr)
	}
}

func TestHandoffSkipAllRecordsBypass(t *testing.T) {
	args, _ := json.Marshal(map[string]any{})
	call := provider.ToolCall{ID: "ex-skip2", Name: "exit_plan_mode", Args: args}
	prov := newScriptedProvider(toolCallStep(call), completedStep("go"))
	eng := engine.New(engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		SessionID:       "root-skip-h",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		InitialAutonomy: protocol.AutonomySkipAll,
		Registry:        tool.NewRegistry(tools.NewExitPlanMode()),
		Agents:          []engine.Agent{{Name: "build"}, {Name: "plan"}},
		InitialAgent:    "plan",
		Workflows:       []engine.Workflow{engine.BuiltinPlanImplement()},
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	waitForEvent(t, eng, func(ev protocol.Event) bool {
		ph, ok := ev.(protocol.PhaseChanged)
		return ok && ph.Phase == "plan"
	})

	eng.Ops() <- protocol.UserInput{Text: "skip"}
	var source string
	deadline := time.After(10 * time.Second)
	for source == "" {
		select {
		case <-deadline:
			t.Fatal("timeout")
		case ev := <-eng.Events():
			switch e := ev.(type) {
			case protocol.QuestionAsked:
				t.Fatalf("skip-all must not ask: %#v", e)
			case protocol.PlanHandoff:
				source = e.ApprovalSource
				if e.PlanID != "" {
					t.Fatalf("unexpected plan id %q", e.PlanID)
				}
			case protocol.ToolCallEnd:
				if e.CallID == "ex-skip2" && e.IsError {
					t.Fatalf("error: %s", e.Output)
				}
			}
		}
	}
	if source != protocol.PlanApprovalSkipAll {
		t.Fatalf("source = %q", source)
	}
}

func TestRestorePlanHandoff(t *testing.T) {
	events := []protocol.Event{
		protocol.PlanHandoff{
			PlanID:         "abc",
			PlanVersion:    4,
			ApprovalSource: protocol.PlanApprovalUser,
			Title:          "T",
			Agent:          "build",
		},
	}
	got := engine.Restore(events)
	if !got.PlanHandoff.Active || got.PlanHandoff.PlanID != "abc" || got.PlanHandoff.Version != 4 {
		t.Fatalf("PlanHandoff = %+v", got.PlanHandoff)
	}
	if got.PlanHandoff.ApprovalSource != protocol.PlanApprovalUser {
		t.Fatalf("source = %q", got.PlanHandoff.ApprovalSource)
	}
}

func TestHandoffImplementerSeesPlanInPrompt(t *testing.T) {
	store := openTestPlanStore(t)
	p, err := store.Create("root-prompt", "Visible plan", []plan.SectionInput{
		{Title: "Step", Body: "unique-handoff-body-xyz"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Approve via handoff path under agent autonomy.
	exitArgs, _ := json.Marshal(map[string]any{
		"plan_id":          p.ID,
		"expected_version": p.Version,
	})
	call := provider.ToolCall{ID: "ex-prompt", Name: "exit_plan_mode", Args: exitArgs}
	var lastSystem string
	prov := &captureSystemProvider{
		scripted: newScriptedProvider(
			toolCallStep(call),
			completedStep("implementing with plan"),
		),
		onStream: func(sys string) { lastSystem = sys },
	}
	eng := engine.New(engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		SessionID:       "root-prompt",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		InitialAutonomy: protocol.AutonomyAgent,
		Registry:        tool.NewRegistry(tools.NewExitPlanMode(), tools.NewPlanRead(store)),
		PlanStore:       enginebind.Plan(store),
		Agents:          []engine.Agent{{Name: "build"}, {Name: "plan"}},
		InitialAgent:    "plan",
		Workflows:       []engine.Workflow{engine.BuiltinPlanImplement()},
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	waitForEvent(t, eng, func(ev protocol.Event) bool {
		ph, ok := ev.(protocol.PhaseChanged)
		return ok && ph.Phase == "plan"
	})

	eng.Ops() <- protocol.UserInput{Text: "go"}
	deadline := time.After(10 * time.Second)
	var implementTurn bool
	for !implementTurn {
		select {
		case <-deadline:
			t.Fatal("timeout")
		case ev := <-eng.Events():
			switch e := ev.(type) {
			case protocol.PhaseChanged:
				if e.Phase == "implement" {
					implementTurn = true
				}
			case protocol.TurnCompleted:
				// second stream may be implement
				if strings.Contains(lastSystem, "unique-handoff-body-xyz") ||
					strings.Contains(lastSystem, p.ID) {
					implementTurn = true
				}
			case protocol.ToolCallEnd:
				if e.CallID == "ex-prompt" && e.IsError {
					t.Fatalf("exit: %s", e.Output)
				}
			}
		}
	}
	// Drain a bit for the follow-up stream system prompt.
	drainDeadline := time.After(2 * time.Second)
drain:
	for {
		select {
		case <-drainDeadline:
			break drain
		case ev := <-eng.Events():
			if _, ok := ev.(protocol.TurnCompleted); ok {
				break drain
			}
		}
	}
	if !strings.Contains(lastSystem, "unique-handoff-body-xyz") && !strings.Contains(lastSystem, p.ID) {
		t.Fatalf("implementer system prompt missing plan; got %q", truncate(lastSystem, 400))
	}
}

// captureSystemProvider records the last Stream system prompt.
type captureSystemProvider struct {
	scripted *scriptedProvider
	onStream func(system string)
}

func (p *captureSystemProvider) Name() string { return p.scripted.Name() }

func (p *captureSystemProvider) Stream(ctx context.Context, req provider.Request) (<-chan provider.StreamEvent, error) {
	if p.onStream != nil {
		p.onStream(req.System)
	}
	return p.scripted.Stream(ctx, req)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
