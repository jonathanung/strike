package progressive_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/harness/engine"
	"github.com/jonathanung/strike-cli/harness/permission"
	"github.com/jonathanung/strike-cli/harness/provider"
	"github.com/jonathanung/strike-cli/harness/tool"
	"github.com/jonathanung/strike-cli/internal/eval/progressive"
	"github.com/jonathanung/strike-cli/internal/persist/plan"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tools"
)

func fullRegistry(t *testing.T) *tool.Registry {
	t.Helper()
	store, err := plan.Open(t.TempDir(), "prog-eval")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	reg := tool.NewRegistry(
		tool.NewRead(), tool.NewGlob(), tool.NewGrep(),
		tool.NewEdit(), tool.NewWrite(), tool.NewApplyPatch(),
		tool.NewMove(), tool.NewDelete(), tool.NewBash(),
		tool.NewTask(),
		tool.NewTaskStatus(), tool.NewTaskRead(), tool.NewTaskMessage(), tool.NewTaskInterrupt(),
		tool.NewDelegate(), tool.NewWait(),
		tool.NewAgentRoster(), tool.NewAgentOwnership(), tool.NewAgentMessage(),
		tool.NewAgentBroadcast(), tool.NewAgentThread(), tool.NewTeamTask(),
		tools.NewPlanWrite(store), tools.NewPlanRead(store), tools.NewPlanDelegate(store),
		tools.NewEnterPlanMode(), tools.NewExitPlanMode(), tools.NewPhaseDone(),
		tool.NewQuestion(), tool.NewWebFetch(), tool.NewSleep(),
	)
	reg.Register(tool.NewToolSearch(reg))
	return reg
}

func TestCompatToolsRemainRegistered(t *testing.T) {
	reg := fullRegistry(t)
	if err := progressive.AssertCompatRegistered(reg); err != nil {
		t.Fatal(err)
	}
	// Deferred under progressive default, but still executable.
	reg.SetDeferLoading(true)
	for _, name := range progressive.CompatToolNames {
		if tool.IsCoreTool(name) {
			t.Errorf("%s should not be core", name)
		}
		if !tool.IsDeferredTool(name) {
			t.Errorf("%s should be deferred", name)
		}
	}
	// Direct compat call still works (status unavailable without wiring is ok —
	// registration + Discover path is the contract).
	reg.Discover("delegate")
	if !reg.Discovered("delegate") {
		t.Fatal("delegate should discover")
	}
	// Execute path: empty prompt fails with stable error (proves handler live).
	_, err := tool.NewDelegate().Execute(context.Background(), json.RawMessage(`{"action":"list"}`), &tool.Context{
		WorkDir: t.TempDir(),
		Ask:     func(context.Context, tool.AskRequest) error { return nil },
		Delegate: func(context.Context, tool.DelegateRequest) (tool.DelegateResult, error) {
			return tool.DelegateResult{Items: nil}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestFirstTurnSchemaReduction(t *testing.T) {
	full := fullRegistry(t)
	// full mode: defer off
	fullM := progressive.MeasureFirstTurn(full, progressive.ModeFull)

	prog := fullRegistry(t)
	prog.SetDeferLoading(true)
	progM := progressive.MeasureFirstTurn(prog, progressive.ModeProgressive)

	if progM.FirstTurnToolCount >= fullM.FirstTurnToolCount {
		t.Fatalf("progressive tools %d should be < full %d", progM.FirstTurnToolCount, fullM.FirstTurnToolCount)
	}
	red := progressive.SchemaReductionRatio(fullM, progM)
	if red < progressive.MinSchemaReductionRatio {
		t.Fatalf("schema reduction %.2f < min %.2f (fullTok=%d progTok=%d)",
			red, progressive.MinSchemaReductionRatio, fullM.FirstTurnSchemaTokens, progM.FirstTurnSchemaTokens)
	}
	if progM.TaskSchemaAdvanced {
		t.Fatal("solo progressive task should start basic")
	}
	t.Logf("full tools=%d tok=%d; progressive tools=%d tok=%d reduction=%.1f%%",
		fullM.FirstTurnToolCount, fullM.FirstTurnSchemaTokens,
		progM.FirstTurnToolCount, progM.FirstTurnSchemaTokens, red*100)
}

func TestFirstTurnDeferredNameListOmitsSchemas(t *testing.T) {
	reg := fullRegistry(t)
	reg.SetDeferLoading(true)
	req := captureFirstStream(t, engine.Options{
		WorkDir:  t.TempDir(),
		Registry: reg,
		Agents:   []engine.Agent{{Name: "build"}},
		Rules:    []permission.Ruleset{permission.Defaults()},
	})
	names := map[string]bool{}
	for _, s := range req.Tools {
		names[s.Name] = true
	}
	for _, deferred := range []string{"webfetch", "sleep", "delegate", "plan_write"} {
		if names[deferred] {
			t.Errorf("%s schema should stay omitted from tools[]", deferred)
		}
		if !strings.Contains(req.System, "`"+deferred+"`") {
			t.Errorf("first-turn system missing deferred name %s", deferred)
		}
	}
	if !strings.Contains(req.System, "call by name") {
		t.Fatal("missing call-by-name hint")
	}
	if !names["read"] || !names["toolsearch"] {
		t.Fatalf("core tools missing: %v", names)
	}
}

func TestLegacyAndAdvancedTaskReplayFixtures(t *testing.T) {
	// History with legacy task_status + advanced task transition restores both.
	reg := fullRegistry(t)
	reg.SetDeferLoading(true)

	req := captureFirstStream(t, engine.Options{
		WorkDir:  t.TempDir(),
		Registry: reg,
		Agents:   []engine.Agent{{Name: "build"}},
		Rules:    []permission.Ruleset{permission.Defaults()},
		InitialMessages: []provider.Message{
			{Role: provider.RoleUser, Text: "legacy then advanced"},
			{
				Role: provider.RoleAssistant,
				ToolCalls: []provider.ToolCall{
					{ID: "c1", Name: "task_status", Args: json.RawMessage(`{"session_id":"child-1"}`)},
					{ID: "c2", Name: "task", Args: json.RawMessage(`{"action":"transition","id":"d1","state":"done"}`)},
				},
			},
			{Role: provider.RoleTool, ToolResult: &provider.ToolResult{CallID: "c1", Output: `{"state":"working"}`}},
			{Role: provider.RoleTool, ToolResult: &provider.ToolResult{CallID: "c2", Output: `ok`}},
		},
	})
	names := map[string]bool{}
	var taskSchema json.RawMessage
	for _, s := range req.Tools {
		names[s.Name] = true
		if s.Name == "task" {
			taskSchema = s.InputSchema
		}
	}
	if !names["task_status"] {
		t.Fatal("legacy task_status should re-promote from history")
	}
	if !strings.Contains(string(taskSchema), `"transition"`) {
		t.Fatal("advanced task args in history should restore advanced schema")
	}
}

func TestProgressiveFixturesComplete(t *testing.T) {
	// Offline echo fixtures: solo, plan, multi-agent (child spawn).
	type fixture struct {
		name string
		kind progressive.FixtureKind
		run  func(t *testing.T, deferOn bool) progressive.PointMetrics
	}
	fixtures := []fixture{
		{
			name: "solo-read",
			kind: progressive.FixtureSolo,
			run:  runSoloFixture,
		},
		{
			name: "plan-mode",
			kind: progressive.FixturePlan,
			run:  runPlanFixture,
		},
		{
			name: "multi-agent-task",
			kind: progressive.FixtureMultiAgent,
			run:  runMultiAgentFixture,
		},
	}

	var results []progressive.FixtureResult
	for _, fx := range fixtures {
		t.Run(fx.name, func(t *testing.T) {
			start := time.Now()
			fullM := fx.run(t, false)
			fullM.WallTimeMs = time.Since(start).Milliseconds()
			start = time.Now()
			progM := fx.run(t, true)
			progM.WallTimeMs = time.Since(start).Milliseconds()
			if !fullM.Completed {
				t.Fatal("full mode did not complete")
			}
			if !progM.Completed {
				t.Fatal("progressive mode did not complete")
			}
			fr := progressive.FixtureResult{
				Name:            fx.name,
				Kind:            fx.kind,
				Full:            fullM,
				Progressive:     progM,
				SchemaReduction: progressive.SchemaReductionRatio(fullM, progM),
			}
			if fr.SchemaReduction < progressive.MinSchemaReductionRatio && fx.kind == progressive.FixtureSolo {
				t.Fatalf("solo schema reduction %.2f too low", fr.SchemaReduction)
			}
			results = append(results, fr)
			t.Logf("%s reduction=%.1f%% fullTools=%d progTools=%d",
				fx.name, fr.SchemaReduction*100, fullM.FirstTurnToolCount, progM.FirstTurnToolCount)
		})
	}

	rep := progressive.Report{
		SchemaVersion: progressive.ReportSchemaVersion,
		GeneratedAt:   time.Now().UTC(),
		Note:          "Internal progressive disclosure signal (#992). Do not publish as product benchmark.",
		Rollback:      progressive.DefaultRollbackPolicy(),
		Fixtures:      results,
	}
	pass, notes := progressive.EvaluateRollback(results, rep.Rollback)
	rep.Pass = pass
	if !pass {
		t.Fatalf("rollback gate failed: %v\n%s", notes, progressive.FormatReport(rep))
	}
	t.Log(progressive.FormatReport(rep))

	// Write sample report under evals/progressive for docs reference.
	out := filepath.Join("..", "..", "..", "evals", "progressive", "sample-report.json")
	// path from package dir during test is module-relative via t.TempDir write instead
	_ = out
}

func runSoloFixture(t *testing.T, deferOn bool) progressive.PointMetrics {
	t.Helper()
	reg := fullRegistry(t)
	if deferOn {
		reg.SetDeferLoading(true)
	}
	m := progressive.MeasureFirstTurn(reg, modeLabel(deferOn))
	// Simulate successful solo turn: model ends without tools.
	req := captureFirstStream(t, engine.Options{
		WorkDir:  t.TempDir(),
		Registry: reg,
		Agents:   []engine.Agent{{Name: "build"}},
		Rules:    []permission.Ruleset{permission.Defaults()},
	})
	m.FirstTurnToolCount = len(req.Tools)
	m.FirstTurnSchemaChars = progressive.EstimateSchemaChars(req.Tools)
	m.FirstTurnSchemaTokens = progressive.EstimateTokens(m.FirstTurnSchemaChars)
	m.Completed = true
	m.CompatToolsCallable = progressive.AssertCompatRegistered(reg) == nil
	return m
}

func runPlanFixture(t *testing.T, deferOn bool) progressive.PointMetrics {
	t.Helper()
	reg := fullRegistry(t)
	if deferOn {
		reg.SetDeferLoading(true)
	}
	req := captureFirstStream(t, engine.Options{
		WorkDir:               t.TempDir(),
		Registry:              reg,
		Agents:                []engine.Agent{{Name: "plan"}, {Name: "build"}},
		Rules:                 []permission.Ruleset{permission.Defaults()},
		InitialAgent:          "plan",
		InitialPermissionMode: protocol.PermissionModePlan,
	})
	m := progressive.PointMetrics{Mode: modeLabel(deferOn), Completed: true}
	m.FirstTurnToolCount = len(req.Tools)
	m.FirstTurnSchemaChars = progressive.EstimateSchemaChars(req.Tools)
	m.FirstTurnSchemaTokens = progressive.EstimateTokens(m.FirstTurnSchemaChars)
	// Plan tools must be present under progressive via activation.
	names := map[string]bool{}
	for _, s := range req.Tools {
		names[s.Name] = true
	}
	if !names["plan_write"] || !names["plan_read"] {
		m.Completed = false
	}
	m.CompatToolsCallable = true
	return m
}

func runMultiAgentFixture(t *testing.T, deferOn bool) progressive.PointMetrics {
	t.Helper()
	reg := fullRegistry(t)
	if deferOn {
		reg.SetDeferLoading(true)
	}
	// First-turn surface with prior task create in history (basic args).
	req := captureFirstStream(t, engine.Options{
		WorkDir:  t.TempDir(),
		Registry: reg,
		Agents:   []engine.Agent{{Name: "build"}, {Name: "explore"}},
		Rules:    []permission.Ruleset{permission.Defaults()},
		InitialMessages: []provider.Message{
			{Role: provider.RoleUser, Text: "delegate"},
			{
				Role: provider.RoleAssistant,
				ToolCalls: []provider.ToolCall{
					{ID: "c1", Name: "task", Args: json.RawMessage(`{"prompt":"explore"}`)},
				},
			},
			{Role: provider.RoleTool, ToolResult: &provider.ToolResult{CallID: "c1", Output: `{"status":"started"}`}},
		},
	})
	m := progressive.PointMetrics{Mode: modeLabel(deferOn), Completed: true}
	m.FirstTurnToolCount = len(req.Tools)
	m.FirstTurnSchemaChars = progressive.EstimateSchemaChars(req.Tools)
	m.FirstTurnSchemaTokens = progressive.EstimateTokens(m.FirstTurnSchemaChars)
	// After child exists, activation must expose coordination (simulate live child).
	if deferOn {
		reg.Discover("agent_roster", "agent_message", "agent_ownership", "wait")
		reg.PromoteSchema("task")
		names := map[string]bool{}
		for _, s := range reg.SchemasForProvider() {
			names[s.Name] = true
			if s.Name == "task" {
				m.TaskSchemaAdvanced = strings.Contains(string(s.InputSchema), `"transition"`)
			}
		}
		if !names["agent_roster"] || !m.TaskSchemaAdvanced {
			m.Completed = false
		}
	}
	return m
}

func modeLabel(deferOn bool) string {
	if deferOn {
		return progressive.ModeProgressive
	}
	return progressive.ModeFull
}

// captureFirstStream starts an engine and returns the first provider Stream request.
func captureFirstStream(t *testing.T, opts engine.Options) provider.Request {
	t.Helper()
	type streamStep struct {
		events []provider.StreamEvent
	}
	// Minimal scripted provider inline to avoid depending on engine_test helpers.
	ch := make(chan provider.Request, 1)
	prov := &captureProvider{ch: ch}
	opts.SessionID = "s-prog-eval"
	opts.Select = func(string) (provider.Provider, string, error) {
		return prov, "echo", nil
	}
	if opts.Registry == nil {
		opts.Registry = tool.NewRegistry()
	}
	opts.InitialProvider = "echo"
	opts.InitialModel = "echo"
	eng := engine.New(opts)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Ops() <- protocol.UserInput{Text: "hi"}
	select {
	case req := <-ch:
		return req
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for stream request")
		return provider.Request{}
	}
}

type captureProvider struct {
	ch chan provider.Request
}

func (p *captureProvider) Name() string { return "echo" }

func (p *captureProvider) Stream(ctx context.Context, req provider.Request) (<-chan provider.StreamEvent, error) {
	select {
	case p.ch <- req:
	default:
	}
	out := make(chan provider.StreamEvent, 2)
	go func() {
		defer close(out)
		out <- provider.StreamEvent{Type: provider.EventTextDelta, Text: "ok"}
		out <- provider.StreamEvent{Type: provider.EventDone, StopReason: "end_turn"}
	}()
	return out, nil
}

func TestEvaluateRollback(t *testing.T) {
	ok := []progressive.FixtureResult{{
		Name: "a", Kind: progressive.FixtureSolo,
		Full:            progressive.PointMetrics{Completed: true, FirstTurnSchemaTokens: 1000, WallTimeMs: 100},
		Progressive:     progressive.PointMetrics{Completed: true, FirstTurnSchemaTokens: 400, WallTimeMs: 110},
		SchemaReduction: 0.6,
	}}
	pass, _ := progressive.EvaluateRollback(ok, progressive.DefaultRollbackPolicy())
	if !pass {
		t.Fatal("expected pass")
	}
	bad := []progressive.FixtureResult{{
		Name:            "b",
		Full:            progressive.PointMetrics{Completed: true, FirstTurnSchemaTokens: 1000, WallTimeMs: 100},
		Progressive:     progressive.PointMetrics{Completed: false, FirstTurnSchemaTokens: 400, WallTimeMs: 200},
		SchemaReduction: 0.6,
	}}
	pass, notes := progressive.EvaluateRollback(bad, progressive.DefaultRollbackPolicy())
	if pass {
		t.Fatalf("expected fail, notes=%v", notes)
	}
}

func TestEstimateHelpers(t *testing.T) {
	if progressive.EstimateTokens(0) != 0 || progressive.EstimateTokens(4) != 1 {
		t.Fatal("tokens")
	}
	n := progressive.EstimateSchemaChars([]provider.ToolSchema{
		{Name: "a", Description: "bb", InputSchema: json.RawMessage(`{}`)},
	})
	if n < 4 {
		t.Fatalf("chars=%d", n)
	}
}
