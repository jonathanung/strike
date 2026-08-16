package engine_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/harness/engine"
	"github.com/jonathanung/strike-cli/harness/permission"
	"github.com/jonathanung/strike-cli/harness/provider"
	"github.com/jonathanung/strike-cli/harness/tool"
	"github.com/jonathanung/strike-cli/internal/enginebind"
	"github.com/jonathanung/strike-cli/internal/tools"
	"github.com/jonathanung/strike-cli/pkg/protocol"
)

func fullToolRegistry(t *testing.T) *tool.Registry {
	t.Helper()
	store := tool.NewTodoStore()
	reg := tool.NewRegistry(
		tool.NewRead(),
		tool.NewGlob(),
		tool.NewGrep(),
		tool.NewEdit(),
		tool.NewWrite(),
		tool.NewApplyPatch(),
		tool.NewMove(),
		tool.NewDelete(),
		tool.NewBash(),
		tool.NewTask(),
		tool.NewWebFetch(),
		tool.NewTodoWrite(store),
		tool.NewTodoRead(store),
		tool.NewSleep(),
		tools.NewSkill(nil),
		tool.NewQuestion(),
		tools.NewEnterPlanMode(),
		tools.NewExitPlanMode(),
		tools.NewPhaseDone(),
	)
	reg.Register(tool.NewToolSearch(reg))
	return reg
}

func TestToolGuidanceRootBuildAdditiveLayer(t *testing.T) {
	reg := fullToolRegistry(t)
	sys := captureSystemPrompt(t, engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		WorkDir:         t.TempDir(),
		Registry:        reg,
		Agents:          []engine.Agent{{Name: "build"}},
		Rules:           []permission.Ruleset{permission.Defaults()},
	}, "echo", "echo")

	if !strings.Contains(sys, "# Available tools") {
		t.Fatalf("missing tools section:\n%s", sys)
	}
	if !strings.Contains(sys, "provider Tools array") {
		t.Fatal("missing schemas-vs-guidance split note")
	}
	if !strings.Contains(sys, "## Recommended use") {
		t.Fatal("missing recommended-use section")
	}
	if !strings.Contains(sys, "Prefer `read`/`glob`/`grep`") {
		t.Fatal("missing explore guidance")
	}
	if !strings.Contains(sys, "`task`") {
		t.Fatal("root build should include task guidance")
	}
	// Additive only: no name — purpose catalog lines.
	for _, name := range reg.Names() {
		if strings.Contains(sys, "`"+name+"` —") {
			t.Errorf("guidance restates catalog line for %q", name)
		}
	}
}

func TestToolGuidanceReadOnlyAgentOmitsMutation(t *testing.T) {
	reg := fullToolRegistry(t)
	sys := captureSystemPrompt(t, engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		WorkDir:         t.TempDir(),
		Registry:        reg,
		Agents: []engine.Agent{
			{Name: "build"},
			{
				Name: "reviewer",
				Permissions: permission.Ruleset{
					{Permission: "write", Pattern: "*", Action: permission.Deny},
					{Permission: "edit", Pattern: "*", Action: permission.Deny},
					{Permission: "bash", Pattern: "*", Action: permission.Deny},
				},
			},
		},
		InitialAgent: "reviewer",
		Rules:        []permission.Ruleset{permission.Defaults()},
	}, "echo", "echo")

	for _, banned := range []string{
		"`write`", "`edit`", "`apply_patch`", "`bash`",
		"multi-file coordinated", "Prefer `apply_patch`",
	} {
		if strings.Contains(sys, banned) {
			t.Errorf("read-only agent prompt contains %q", banned)
		}
	}
	if !strings.Contains(sys, "Use `read`/`glob`/`grep` for ordinary code exploration") {
		t.Fatal("read-only agent missing explore tip")
	}
}

func TestToolGuidanceChildDepthOmitsTask(t *testing.T) {
	parent := fullToolRegistry(t)
	child := parent.CloneWithout("task")
	sys := captureSystemPrompt(t, engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		WorkDir:         t.TempDir(),
		Registry:        child,
		Agents:          []engine.Agent{{Name: "general"}},
		Rules:           []permission.Ruleset{permission.Defaults()},
	}, "echo", "echo")

	if strings.Contains(sys, "`task`") {
		t.Fatalf("child-depth prompt still mentions task:\n%s", sys)
	}
	if !strings.Contains(sys, "# Available tools") {
		t.Fatal("child prompt missing tools layer")
	}
}

func TestToolGuidanceMCPDynamic(t *testing.T) {
	reg := tool.NewRegistry(tool.NewRead())
	sys1 := captureSystemPrompt(t, engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		WorkDir:         t.TempDir(),
		Registry:        reg,
		Agents:          []engine.Agent{{Name: "build"}},
		Rules:           []permission.Ruleset{permission.Defaults()},
	}, "echo", "echo")
	if strings.Contains(sys1, "registered `mcp_*` names") {
		t.Fatal("MCP tip before any MCP tool registered")
	}

	reg.Register(stubMCPTool{name: "mcp_demo_ping", desc: "ping the demo server"})
	sys2 := captureSystemPrompt(t, engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		WorkDir:         t.TempDir(),
		Registry:        reg,
		Agents:          []engine.Agent{{Name: "build"}},
		Rules:           []permission.Ruleset{permission.Defaults()},
	}, "echo", "echo")
	if !strings.Contains(sys2, "registered `mcp_*` names") {
		t.Fatalf("MCP add not reflected in tips:\n%s", sys2)
	}
	// Few MCP tools: no per-tool catalog restatement.
	if strings.Contains(sys2, "`mcp_demo_ping`") {
		t.Fatalf("small MCP set should not list tool name in guidance:\n%s", sys2)
	}

	reg.Unregister("mcp_demo_ping")
	sys3 := captureSystemPrompt(t, engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		WorkDir:         t.TempDir(),
		Registry:        reg,
		Agents:          []engine.Agent{{Name: "build"}},
		Rules:           []permission.Ruleset{permission.Defaults()},
	}, "echo", "echo")
	if strings.Contains(sys3, "registered `mcp_*` names") {
		t.Fatalf("MCP remove not reflected:\n%s", sys3)
	}
}

func TestToolGuidancePermissionModePlan(t *testing.T) {
	reg := fullToolRegistry(t)
	sys := captureSystemPrompt(t, engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		WorkDir:         t.TempDir(),
		Registry:        reg,
		Agents: []engine.Agent{
			{Name: "build"},
			{Name: "plan"},
		},
		Rules:                 []permission.Ruleset{permission.Defaults()},
		InitialPermissionMode: protocol.PermissionModePlan,
	}, "echo", "echo")

	for _, banned := range []string{"Prefer `apply_patch`", "`write` only", "multi-file coordinated"} {
		if strings.Contains(sys, banned) {
			t.Errorf("plan permission mode still has mutation tip %q", banned)
		}
	}
	if !strings.Contains(sys, "# Available tools") {
		t.Fatal("plan mode should still include tools layer")
	}
	if !strings.Contains(sys, "Prefer `read`/`glob`/`grep`") {
		t.Fatal("plan mode should still include explore tip")
	}
}

func TestToolGuidanceProvenanceLayer(t *testing.T) {
	reg := fullToolRegistry(t)
	prov := newScriptedProvider(streamStep{
		events: []provider.StreamEvent{
			{Type: provider.EventTextDelta, Text: "ok"},
			{Type: provider.EventDone, StopReason: "end_turn"},
		},
	})
	eng := engine.New(engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		SessionID:       "s-tools-prov",
		WorkDir:         t.TempDir(),
		Registry:        reg,
		Agents:          []engine.Agent{{Name: "build"}},
		Rules:           []permission.Ruleset{permission.Defaults()},
		Select: func(string) (provider.Provider, string, error) {
			return prov, "echo", nil
		},
		InitialProvider: "echo",
		InitialModel:    "echo",
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "hi"}
	waitStreamRequest(t, eng, prov)
	waitTurnCompleted(t, eng)

	eng.Ops() <- protocol.InspectEffectivePrompt{}
	ev := waitEffectivePrompt(t, eng)
	found := false
	for _, layer := range ev.Layers {
		if layer.Kind == protocol.PromptLayerTools {
			found = true
			if !strings.HasPrefix(layer.Source, "registry:effective") {
				t.Fatalf("tools layer source = %q", layer.Source)
			}
			if layer.Mode != protocol.PromptLayerAppend {
				t.Fatalf("tools layer mode = %q", layer.Mode)
			}
			if layer.Chars == 0 {
				t.Fatal("tools layer chars = 0")
			}
		}
	}
	if !found {
		t.Fatalf("PromptLayerTools missing: %+v", ev.Layers)
	}
	if len(ev.Layers) < 2 || ev.Layers[0].Kind != protocol.PromptLayerShared || ev.Layers[1].Kind != protocol.PromptLayerTools {
		t.Fatalf("layer order = %+v", ev.Layers)
	}
}

func TestToolGuidanceNoCatalogDrift(t *testing.T) {
	reg := fullToolRegistry(t)
	sys := captureSystemPrompt(t, engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		WorkDir:         t.TempDir(),
		Registry:        reg,
		Agents:          []engine.Agent{{Name: "build"}},
		Rules:           []permission.Ruleset{permission.Defaults()},
	}, "echo", "echo")

	// Guidance is tip-based; unregistered control tools must not appear.
	if strings.Contains(sys, "task_status") {
		t.Fatal("unregistered task_status mentioned in guidance")
	}
	for _, name := range reg.Names() {
		if strings.Contains(sys, "`"+name+"` —") {
			t.Errorf("catalog line for %q should not appear in additive guidance", name)
		}
	}
}

func TestToolGuidanceMCPSizeCap(t *testing.T) {
	reg := tool.NewRegistry(tool.NewRead())
	for i := 0; i < tool.MaxMCPGuidanceListed+10; i++ {
		reg.Register(stubMCPTool{
			name: fmt.Sprintf("mcp_bulk_t%d", i),
			desc: fmt.Sprintf("bulk tool number %d with a longer description padding", i),
		})
	}
	reg.Register(tool.NewToolSearch(reg))
	sys := captureSystemPrompt(t, engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		WorkDir:         t.TempDir(),
		Registry:        reg,
		Agents:          []engine.Agent{{Name: "build"}},
		Rules:           []permission.Ruleset{permission.Defaults()},
	}, "echo", "echo")

	if strings.Contains(sys, "`mcp_bulk_t0`") {
		t.Fatal("expected MCP summary, found per-tool listing")
	}
	if !strings.Contains(sys, "MCP tools (") {
		t.Fatalf("missing MCP summary:\n%s", sys)
	}
	idx := strings.Index(sys, "# Available tools")
	if idx < 0 {
		t.Fatal("missing tools section")
	}
	section := sys[idx:]
	if end := strings.Index(section[1:], "\n# "); end > 0 {
		section = section[:end+1]
	}
	if len(section) > 3500 {
		t.Fatalf("tools section too large: %d bytes", len(section))
	}
}

// TestFirstTurnPreloadsEffectiveTools proves turn 1 binds the full effective
// tool set as provider schemas and includes the additive tools prompt layer.
func TestFirstTurnPreloadsEffectiveTools(t *testing.T) {
	reg := fullToolRegistry(t)
	req := captureStreamRequest(t, engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		WorkDir:         t.TempDir(),
		Registry:        reg,
		Agents:          []engine.Agent{{Name: "build"}},
		Rules:           []permission.Ruleset{permission.Defaults()},
	}, "echo", "echo")

	if len(req.Tools) == 0 {
		t.Fatal("first-turn Tools empty; expected effective schemas preloaded")
	}
	want := reg.Names()
	if len(req.Tools) != len(want) {
		t.Fatalf("first-turn Tools len = %d, want %d (registry)", len(req.Tools), len(want))
	}
	fullByName := make(map[string]provider.ToolSchema, len(want))
	for _, s := range reg.Schemas() {
		fullByName[s.Name] = s
	}
	gotNames := make(map[string]bool, len(req.Tools))
	for _, s := range req.Tools {
		gotNames[s.Name] = true
		if len(s.InputSchema) == 0 {
			t.Errorf("tool %q missing InputSchema on first turn", s.Name)
		}
		if strings.TrimSpace(s.Description) == "" {
			t.Errorf("tool %q missing Description on first turn", s.Name)
		}
		// Wire descriptions are compacted; registry keeps full prose.
		full := fullByName[s.Name]
		wantDesc := tool.CompactSchemaDescription(s.Name, full.Description)
		if s.Description != wantDesc {
			t.Errorf("tool %q Description = %q, want compact %q", s.Name, s.Description, wantDesc)
		}
		if s.Name != "skill" && len(full.Description) > len(wantDesc)+20 && s.Description == full.Description {
			t.Errorf("tool %q still has full registry description on the wire", s.Name)
		}
	}
	for _, name := range want {
		if !gotNames[name] {
			t.Errorf("registry tool %q missing from first-turn Tools", name)
		}
	}
	if !strings.Contains(req.System, "# Available tools") {
		t.Fatalf("first-turn system missing tools guidance:\n%s", req.System)
	}
	if !strings.Contains(req.System, "provider Tools array") {
		t.Fatal("first-turn guidance missing schemas-vs-guidance split")
	}
	if !strings.Contains(req.System, "## Recommended use") {
		t.Fatal("first-turn guidance missing recommended-use layer")
	}
	// Schemas own the catalog; guidance must not restate name — purpose lines.
	for _, name := range want {
		if strings.Contains(req.System, "`"+name+"` —") {
			t.Errorf("first-turn guidance catalog line for %q", name)
		}
	}
}

// TestEffectiveToolSchemasReducePayload measures description compaction and
// agent/phase subsetting on the always-on Tools array (#436).
func TestEffectiveToolSchemasReducePayload(t *testing.T) {
	reg := fullToolRegistry(t)
	full := reg.Schemas()
	fullDescBytes := 0
	for _, s := range full {
		fullDescBytes += len(s.Description)
	}

	buildReq := captureStreamRequest(t, engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		WorkDir:         t.TempDir(),
		Registry:        reg,
		Agents:          []engine.Agent{{Name: "build"}},
		Rules:           []permission.Ruleset{permission.Defaults()},
	}, "echo", "echo")
	buildDescBytes := 0
	for _, s := range buildReq.Tools {
		buildDescBytes += len(s.Description)
		if len(s.InputSchema) == 0 {
			t.Errorf("build tool %q lost InputSchema", s.Name)
		}
	}
	if len(buildReq.Tools) != len(full) {
		t.Fatalf("build Tools count = %d, want full registry %d (compaction must not drop allowed tools)",
			len(buildReq.Tools), len(full))
	}
	if buildDescBytes >= fullDescBytes {
		t.Fatalf("build wire descriptions not smaller: wire=%d registry=%d", buildDescBytes, fullDescBytes)
	}
	// Expect a large cut: short purposes vs multi-paragraph usage notes.
	if saved := fullDescBytes - buildDescBytes; saved < fullDescBytes/2 {
		t.Fatalf("description savings too small: saved=%d of %d", saved, fullDescBytes)
	}

	// Explore-style agent: hard denies shrink the tool *count* as well.
	exploreReq := captureStreamRequest(t, engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		WorkDir:         t.TempDir(),
		Registry:        reg,
		Agents: []engine.Agent{
			{Name: "build"},
			{
				Name: "explore",
				Permissions: permission.Ruleset{
					{Permission: "write", Pattern: "*", Action: permission.Deny},
					{Permission: "edit", Pattern: "*", Action: permission.Deny},
					{Permission: "bash", Pattern: "*", Action: permission.Deny},
					{Permission: "task", Pattern: "*", Action: permission.Deny},
				},
			},
		},
		InitialAgent: "explore",
		Rules:        []permission.Ruleset{permission.Defaults()},
	}, "echo", "echo")
	if len(exploreReq.Tools) >= len(buildReq.Tools) {
		t.Fatalf("explore Tools count = %d, want fewer than build %d", len(exploreReq.Tools), len(buildReq.Tools))
	}
	for _, banned := range []string{"write", "edit", "apply_patch", "bash", "task"} {
		for _, s := range exploreReq.Tools {
			if s.Name == banned {
				t.Errorf("explore still exposes %q", banned)
			}
		}
	}
	for _, want := range []string{"read", "glob", "grep"} {
		found := false
		for _, s := range exploreReq.Tools {
			if s.Name == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("explore missing allowed tool %q", want)
		}
	}

	// Plan permission mode also subsets mutations.
	planReq := captureStreamRequest(t, engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		WorkDir:         t.TempDir(),
		Registry:        reg,
		Agents: []engine.Agent{
			{Name: "build"},
			{Name: "plan"},
		},
		Rules:                 []permission.Ruleset{permission.Defaults()},
		InitialPermissionMode: protocol.PermissionModePlan,
	}, "echo", "echo")
	if len(planReq.Tools) >= len(buildReq.Tools) {
		t.Fatalf("plan Tools count = %d, want fewer than build %d", len(planReq.Tools), len(buildReq.Tools))
	}
}

// TestFirstTurnToolsRespectPermissionDenies ensures hard-denied tools are
// omitted from the provider Tools array and from guidance tips on turn 1.
func TestFirstTurnToolsRespectPermissionDenies(t *testing.T) {
	reg := fullToolRegistry(t)
	req := captureStreamRequest(t, engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		WorkDir:         t.TempDir(),
		Registry:        reg,
		Agents: []engine.Agent{
			{Name: "build"},
			{
				Name: "reviewer",
				Permissions: permission.Ruleset{
					{Permission: "write", Pattern: "*", Action: permission.Deny},
					{Permission: "edit", Pattern: "*", Action: permission.Deny},
					{Permission: "bash", Pattern: "*", Action: permission.Deny},
				},
			},
		},
		InitialAgent: "reviewer",
		Rules:        []permission.Ruleset{permission.Defaults()},
	}, "echo", "echo")

	banned := []string{"write", "edit", "apply_patch", "bash"}
	for _, name := range banned {
		for _, s := range req.Tools {
			if s.Name == name {
				t.Errorf("denied tool %q still in first-turn Tools", name)
			}
		}
	}
	for _, bannedTip := range []string{
		"`write`", "`edit`", "`apply_patch`", "`bash`", "Prefer `apply_patch`",
	} {
		if strings.Contains(req.System, bannedTip) {
			t.Errorf("denied-tool tip %q still in first-turn guidance", bannedTip)
		}
	}
	for _, want := range []string{"read", "glob", "grep"} {
		found := false
		for _, s := range req.Tools {
			if s.Name == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("allowed tool %q missing from first-turn Tools", want)
		}
	}
	if !strings.Contains(req.System, "Use `read`/`glob`/`grep`") {
		t.Error("allowed explore tip missing from guidance")
	}
}

// TestFirstTurnToolsOmitPlanMutations covers permission-mode plan: mutation
// tools must not appear in first-turn schemas.
func TestFirstTurnToolsOmitPlanMutations(t *testing.T) {
	reg := fullToolRegistry(t)
	req := captureStreamRequest(t, engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		WorkDir:         t.TempDir(),
		Registry:        reg,
		Agents: []engine.Agent{
			{Name: "build"},
			{Name: "plan"},
		},
		Rules:                 []permission.Ruleset{permission.Defaults()},
		InitialPermissionMode: protocol.PermissionModePlan,
	}, "echo", "echo")

	for _, banned := range []string{"write", "edit", "apply_patch"} {
		for _, s := range req.Tools {
			if s.Name == banned {
				t.Errorf("plan mode still exposes %q in first-turn Tools", banned)
			}
		}
	}
	foundRead := false
	for _, s := range req.Tools {
		if s.Name == "read" {
			foundRead = true
			break
		}
	}
	if !foundRead {
		t.Fatal("plan mode first-turn Tools missing read")
	}
}

// TestDeferToolsOmitThenDiscoverThenInclude covers the omit → toolsearch →
// subsequent stream includes lifecycle when registry defer loading is on.
func TestDeferToolsOmitThenDiscoverThenInclude(t *testing.T) {
	reg := tool.NewRegistry(
		tool.NewRead(),
		tool.NewWebFetch(),
		tool.NewSleep(),
	)
	reg.Register(tool.NewToolSearch(reg))
	reg.SetDeferLoading(true)

	callID := "ts-1"
	prov := newScriptedProvider(
		toolCallStep(provider.ToolCall{
			ID:   callID,
			Name: "toolsearch",
			Args: json.RawMessage(`{"query":"webfetch"}`),
		}),
		streamStep{
			match: matchToolResult(callID),
			events: []provider.StreamEvent{
				{Type: provider.EventTextDelta, Text: "found it"},
				{Type: provider.EventDone, StopReason: "end_turn"},
			},
		},
	)

	eng := engine.New(engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		SessionID:       "s-defer-tools",
		WorkDir:         t.TempDir(),
		Registry:        reg,
		Agents:          []engine.Agent{{Name: "build"}},
		Rules:           []permission.Ruleset{permission.Defaults()},
		Select: func(string) (provider.Provider, string, error) {
			return prov, "echo", nil
		},
		InitialProvider: "echo",
		InitialModel:    "echo",
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "find webfetch"}
	req1 := waitStreamRequest(t, eng, prov)
	names1 := toolNameSet(req1.Tools)
	if !names1["read"] || !names1["toolsearch"] {
		t.Fatalf("first stream missing core tools: %v", names1)
	}
	if names1["webfetch"] || names1["sleep"] {
		t.Fatalf("first stream should omit deferred tools: %v", names1)
	}
	if !strings.Contains(req1.System, "`webfetch`") || !strings.Contains(req1.System, "`sleep`") {
		t.Fatalf("first-turn system missing deferred name list:\n%s", req1.System)
	}
	if !strings.Contains(req1.System, "call by name") {
		t.Fatalf("first-turn system missing call-by-name hint:\n%s", req1.System)
	}

	req2 := waitStreamRequest(t, eng, prov)
	names2 := toolNameSet(req2.Tools)
	if !names2["webfetch"] {
		t.Fatalf("second stream missing discovered webfetch: %v", names2)
	}
	if names2["sleep"] {
		t.Fatalf("unrelated sleep should stay deferred: %v", names2)
	}
	waitTurnCompleted(t, eng)
}

// TestDeferToolsDirectCallPromotes ensures calling a deferred tool by name
// promotes it for subsequent streams.
func TestDeferToolsDirectCallPromotes(t *testing.T) {
	// MCP-named stub is deferred (non-core) and needs no permission Ask.
	deferred := stubMCPTool{name: "mcp_demo_ping", desc: "ping the demo server"}
	reg := tool.NewRegistry(tool.NewRead(), deferred)
	reg.SetDeferLoading(true)

	callID := "mcp-1"
	prov := newScriptedProvider(
		toolCallStep(provider.ToolCall{
			ID:   callID,
			Name: "mcp_demo_ping",
			Args: json.RawMessage(`{}`),
		}),
		streamStep{
			match: matchToolResult(callID),
			events: []provider.StreamEvent{
				{Type: provider.EventTextDelta, Text: "done"},
				{Type: provider.EventDone, StopReason: "end_turn"},
			},
		},
	)

	eng := engine.New(engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		SessionID:       "s-defer-direct",
		WorkDir:         t.TempDir(),
		Registry:        reg,
		Agents:          []engine.Agent{{Name: "build"}},
		Rules: []permission.Ruleset{
			permission.Defaults(),
			{{Permission: "mcp", Pattern: "*", Action: permission.Allow}},
		},
		Select: func(string) (provider.Provider, string, error) {
			return prov, "echo", nil
		},
		InitialProvider: "echo",
		InitialModel:    "echo",
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "ping"}
	req1 := waitStreamRequest(t, eng, prov)
	if toolNameSet(req1.Tools)["mcp_demo_ping"] {
		t.Fatal("mcp_demo_ping should be deferred on first stream")
	}
	req2 := waitStreamRequest(t, eng, prov)
	if !toolNameSet(req2.Tools)["mcp_demo_ping"] {
		t.Fatal("mcp_demo_ping should be promoted after direct call")
	}
	waitTurnCompleted(t, eng)
}

// TestDeferToolsHistoryRepromotesOnResume ensures tools already called in
// InitialMessages are loaded on the first stream after resume.
func TestDeferToolsHistoryRepromotesOnResume(t *testing.T) {
	reg := tool.NewRegistry(tool.NewRead(), tool.NewWebFetch(), tool.NewSleep())
	reg.Register(tool.NewToolSearch(reg))
	reg.SetDeferLoading(true)

	req := captureStreamRequest(t, engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		WorkDir:         t.TempDir(),
		Registry:        reg,
		Agents:          []engine.Agent{{Name: "build"}},
		Rules:           []permission.Ruleset{permission.Defaults()},
		InitialMessages: []provider.Message{
			{Role: provider.RoleUser, Text: "fetch something"},
			{
				Role: provider.RoleAssistant,
				ToolCalls: []provider.ToolCall{
					{ID: "c1", Name: "webfetch", Args: json.RawMessage(`{"url":"https://example.com"}`)},
				},
			},
			{
				Role: provider.RoleTool,
				ToolResult: &provider.ToolResult{
					CallID: "c1",
					Output: "ok",
				},
			},
		},
	}, "echo", "echo")

	names := toolNameSet(req.Tools)
	if !names["webfetch"] {
		t.Fatalf("resume should re-promote webfetch from history: %v", names)
	}
	if names["sleep"] {
		t.Fatalf("unused sleep should stay deferred: %v", names)
	}
}

// TestDeferToolsOffSendsFullSet keeps default (defer off) behavior.
func TestDeferToolsOffSendsFullSet(t *testing.T) {
	reg := tool.NewRegistry(tool.NewRead(), tool.NewWebFetch(), tool.NewSleep())
	// DeferLoading left false.
	req := captureStreamRequest(t, engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		WorkDir:         t.TempDir(),
		Registry:        reg,
		Agents:          []engine.Agent{{Name: "build"}},
		Rules:           []permission.Ruleset{permission.Defaults()},
	}, "echo", "echo")
	names := toolNameSet(req.Tools)
	if !names["read"] || !names["webfetch"] || !names["sleep"] {
		t.Fatalf("defer off should send all tools: %v", names)
	}
	if strings.Contains(req.System, "additional tool(s) are deferred") {
		t.Fatalf("defer off should not list deferred names:\n%s", req.System)
	}
}

func TestDeferToolsNameListOmitsHardDenied(t *testing.T) {
	reg := tool.NewRegistry(tool.NewRead(), tool.NewWebFetch(), tool.NewSleep())
	reg.Register(tool.NewToolSearch(reg))
	reg.SetDeferLoading(true)

	req := captureStreamRequest(t, engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		WorkDir:         t.TempDir(),
		Registry:        reg,
		Agents: []engine.Agent{{
			Name: "build",
			Permissions: permission.Ruleset{
				{Permission: "webfetch", Pattern: "*", Action: permission.Deny},
			},
		}},
		Rules: []permission.Ruleset{permission.Defaults()},
	}, "echo", "echo")

	if toolNameSet(req.Tools)["webfetch"] || toolNameSet(req.Tools)["sleep"] {
		t.Fatalf("deferred schemas leaked: %v", toolNameSet(req.Tools))
	}
	if strings.Contains(req.System, "`webfetch`") {
		t.Fatalf("hard-denied webfetch listed in deferred names:\n%s", req.System)
	}
	if !strings.Contains(req.System, "`sleep`") {
		t.Fatalf("visible deferred sleep missing from name list:\n%s", req.System)
	}
}

func TestDeferToolsNameListTruncatesMCPHeavy(t *testing.T) {
	reg := tool.NewRegistry(tool.NewRead(), tool.NewSleep())
	for i := 0; i < tool.MaxDeferredPendingListed+3; i++ {
		reg.Register(stubMCPTool{
			name: fmt.Sprintf("mcp_bulk_n%02d", i),
			desc: "bulk deferred mcp",
		})
	}
	reg.Register(tool.NewToolSearch(reg))
	reg.SetDeferLoading(true)

	req := captureStreamRequest(t, engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		WorkDir:         t.TempDir(),
		Registry:        reg,
		Agents:          []engine.Agent{{Name: "build"}},
		Rules: []permission.Ruleset{
			permission.Defaults(),
			{{Permission: "mcp", Pattern: "*", Action: permission.Allow}},
		},
	}, "echo", "echo")

	if toolNameSet(req.Tools)["sleep"] {
		t.Fatal("sleep schema should stay omitted")
	}
	for name := range toolNameSet(req.Tools) {
		if strings.HasPrefix(name, "mcp_") {
			t.Fatalf("mcp schema leaked on first turn: %s", name)
		}
	}
	if !strings.Contains(req.System, "`sleep`") {
		t.Fatalf("sleep missing from name list:\n%s", req.System)
	}
	// sleep + MaxDeferredPendingListed+3 mcp names = cap+4 pending → +4 more.
	if !strings.Contains(req.System, "(+4 more)") {
		t.Fatalf("remainder missing from truncated list:\n%s", req.System)
	}
}

func toolNameSet(schemas []provider.ToolSchema) map[string]bool {
	out := make(map[string]bool, len(schemas))
	for _, s := range schemas {
		out[s.Name] = true
	}
	return out
}

// captureStreamRequest starts an engine and returns the first provider Stream request.
func captureStreamRequest(t *testing.T, opts engine.Options, providerName, model string) provider.Request {
	t.Helper()
	prov := newScriptedProvider(streamStep{
		events: []provider.StreamEvent{
			{Type: provider.EventTextDelta, Text: "ok"},
			{Type: provider.EventDone, StopReason: "end_turn"},
		},
	})
	opts.SessionID = "s-tool-preload"
	opts.Select = func(string) (provider.Provider, string, error) {
		return prov, model, nil
	}
	if opts.Registry == nil {
		opts.Registry = tool.NewRegistry()
	}
	opts.InitialProvider = providerName
	opts.InitialModel = model

	eng := engine.New(opts)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "hi"}
	return waitStreamRequest(t, eng, prov)
}

type stubMCPTool struct {
	name, desc string
}

func (s stubMCPTool) Name() string        { return s.name }
func (s stubMCPTool) Description() string { return s.desc }
func (s stubMCPTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (s stubMCPTool) Execute(context.Context, json.RawMessage, *tool.Context) (tool.Result, error) {
	return tool.Result{}, nil
}

// TestTaskSchemaBasicThenPromoteOnHistory ensures progressive task starts
// basic and history with advanced args restores advanced on resume (#989).
func TestTaskSchemaBasicThenPromoteOnHistory(t *testing.T) {
	reg := tool.NewRegistry(tool.NewRead(), tool.NewTask())
	reg.Register(tool.NewToolSearch(reg))

	// Fresh session: basic task schema.
	req := captureStreamRequest(t, engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		WorkDir:         t.TempDir(),
		Registry:        reg,
		Agents:          []engine.Agent{{Name: "build"}},
		Rules:           []permission.Ruleset{permission.Defaults()},
	}, "echo", "echo")
	var basic json.RawMessage
	for _, s := range req.Tools {
		if s.Name == "task" {
			basic = s.InputSchema
		}
	}
	if len(basic) == 0 {
		t.Fatal("task missing on first stream")
	}
	if strings.Contains(string(basic), `"transition"`) {
		t.Fatal("fresh task should be basic schema")
	}

	// Resume with advanced task call in history.
	reg2 := tool.NewRegistry(tool.NewRead(), tool.NewTask())
	reg2.Register(tool.NewToolSearch(reg2))
	req2 := captureStreamRequest(t, engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		WorkDir:         t.TempDir(),
		Registry:        reg2,
		Agents:          []engine.Agent{{Name: "build"}},
		Rules:           []permission.Ruleset{permission.Defaults()},
		InitialMessages: []provider.Message{
			{Role: provider.RoleUser, Text: "delegate carefully"},
			{
				Role: provider.RoleAssistant,
				ToolCalls: []provider.ToolCall{
					{ID: "c1", Name: "task", Args: json.RawMessage(`{"action":"transition","id":"d1","state":"done"}`)},
				},
			},
			{
				Role:       provider.RoleTool,
				ToolResult: &provider.ToolResult{CallID: "c1", Output: "ok"},
			},
		},
	}, "echo", "echo")
	var adv json.RawMessage
	for _, s := range req2.Tools {
		if s.Name == "task" {
			adv = s.InputSchema
		}
	}
	if !strings.Contains(string(adv), `"transition"`) {
		t.Fatalf("resume should restore advanced task schema: %s", adv)
	}

	// Resume with prompt-only history stays basic.
	reg3 := tool.NewRegistry(tool.NewRead(), tool.NewTask())
	req3 := captureStreamRequest(t, engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		WorkDir:         t.TempDir(),
		Registry:        reg3,
		Agents:          []engine.Agent{{Name: "build"}},
		Rules:           []permission.Ruleset{permission.Defaults()},
		InitialMessages: []provider.Message{
			{Role: provider.RoleUser, Text: "spawn"},
			{
				Role: provider.RoleAssistant,
				ToolCalls: []provider.ToolCall{
					{ID: "c1", Name: "task", Args: json.RawMessage(`{"prompt":"explore pkg"}`)},
				},
			},
			{
				Role:       provider.RoleTool,
				ToolResult: &provider.ToolResult{CallID: "c1", Output: "started"},
			},
		},
	}, "echo", "echo")
	for _, s := range req3.Tools {
		if s.Name == "task" && strings.Contains(string(s.InputSchema), `"transition"`) {
			t.Fatal("prompt-only history should keep basic task schema")
		}
	}
}
