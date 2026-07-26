package engine_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/engine"
	"github.com/jonathanung/strike-cli/internal/permission"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/provider"
	"github.com/jonathanung/strike-cli/internal/tool"
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
		tool.NewBash(),
		tool.NewTask(),
		tool.NewWebFetch(),
		tool.NewTodoWrite(store),
		tool.NewTodoRead(store),
		tool.NewSleep(),
		tool.NewSkill(nil),
		tool.NewQuestion(),
		tool.NewEnterPlanMode(),
		tool.NewExitPlanMode(),
		tool.NewPhaseDone(),
	)
	reg.Register(tool.NewToolSearch(reg))
	return reg
}

func TestToolGuidanceRootBuildListsEffectiveTools(t *testing.T) {
	reg := fullToolRegistry(t)
	sys := captureSystemPrompt(t, engine.Options{
		WorkDir:  t.TempDir(),
		Registry: reg,
		Agents:   []engine.Agent{{Name: "build"}},
		Rules:    []permission.Ruleset{permission.Defaults()},
	}, "echo", "echo")

	if !strings.Contains(sys, "# Available tools") {
		t.Fatalf("missing tools section:\n%s", sys)
	}
	for _, name := range reg.Names() {
		if !strings.Contains(sys, "`"+name+"`") {
			t.Errorf("effective tool %q missing from prompt", name)
		}
	}
	if !strings.Contains(sys, "Prefer `read`/`glob`/`grep`") {
		t.Fatal("missing explore guidance")
	}
	if !strings.Contains(sys, "`task`") {
		t.Fatal("root build should include task guidance")
	}
}

func TestToolGuidanceReadOnlyAgentOmitsMutation(t *testing.T) {
	reg := fullToolRegistry(t)
	sys := captureSystemPrompt(t, engine.Options{
		WorkDir:  t.TempDir(),
		Registry: reg,
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
		"`write` —", "`edit` —", "`apply_patch` —", "`bash` —",
		"multi-file coordinated", "Prefer `apply_patch`",
	} {
		if strings.Contains(sys, banned) {
			t.Errorf("read-only agent prompt contains %q", banned)
		}
	}
	for _, want := range []string{"`read` —", "`glob` —", "`grep` —"} {
		if !strings.Contains(sys, want) {
			t.Errorf("read-only agent missing %q", want)
		}
	}
}

func TestToolGuidanceChildDepthOmitsTask(t *testing.T) {
	parent := fullToolRegistry(t)
	child := parent.CloneWithout("task")
	sys := captureSystemPrompt(t, engine.Options{
		WorkDir:  t.TempDir(),
		Registry: child,
		Agents:   []engine.Agent{{Name: "general"}},
		Rules:    []permission.Ruleset{permission.Defaults()},
	}, "echo", "echo")

	if strings.Contains(sys, "`task`") {
		t.Fatalf("child-depth prompt still mentions task:\n%s", sys)
	}
	if !strings.Contains(sys, "`read` —") {
		t.Fatal("child prompt missing other tools")
	}
}

func TestToolGuidanceMCPDynamic(t *testing.T) {
	reg := tool.NewRegistry(tool.NewRead())
	sys1 := captureSystemPrompt(t, engine.Options{
		WorkDir:  t.TempDir(),
		Registry: reg,
		Agents:   []engine.Agent{{Name: "build"}},
		Rules:    []permission.Ruleset{permission.Defaults()},
	}, "echo", "echo")
	if strings.Contains(sys1, "mcp_demo_ping") {
		t.Fatal("stale MCP name before register")
	}

	reg.Register(stubMCPTool{name: "mcp_demo_ping", desc: "ping the demo server"})
	sys2 := captureSystemPrompt(t, engine.Options{
		WorkDir:  t.TempDir(),
		Registry: reg,
		Agents:   []engine.Agent{{Name: "build"}},
		Rules:    []permission.Ruleset{permission.Defaults()},
	}, "echo", "echo")
	if !strings.Contains(sys2, "`mcp_demo_ping`") {
		t.Fatalf("MCP add not reflected:\n%s", sys2)
	}

	reg.Unregister("mcp_demo_ping")
	sys3 := captureSystemPrompt(t, engine.Options{
		WorkDir:  t.TempDir(),
		Registry: reg,
		Agents:   []engine.Agent{{Name: "build"}},
		Rules:    []permission.Ruleset{permission.Defaults()},
	}, "echo", "echo")
	if strings.Contains(sys3, "mcp_demo_ping") {
		t.Fatalf("MCP remove not reflected:\n%s", sys3)
	}
}

func TestToolGuidancePermissionModePlan(t *testing.T) {
	reg := fullToolRegistry(t)
	sys := captureSystemPrompt(t, engine.Options{
		WorkDir:  t.TempDir(),
		Registry: reg,
		Agents: []engine.Agent{
			{Name: "build"},
			{Name: "plan"},
		},
		Rules:                 []permission.Ruleset{permission.Defaults()},
		InitialPermissionMode: protocol.PermissionModePlan,
	}, "echo", "echo")

	for _, banned := range []string{"`write` —", "`edit` —", "`apply_patch` —"} {
		if strings.Contains(sys, banned) {
			t.Errorf("plan permission mode still lists %q", banned)
		}
	}
	if !strings.Contains(sys, "`read` —") {
		t.Fatal("plan mode should still list read")
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
		SessionID: "s-tools-prov",
		WorkDir:   t.TempDir(),
		Registry:  reg,
		Agents:    []engine.Agent{{Name: "build"}},
		Rules:     []permission.Ruleset{permission.Defaults()},
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

func TestToolGuidanceSingleSourceNoDrift(t *testing.T) {
	reg := fullToolRegistry(t)
	sys := captureSystemPrompt(t, engine.Options{
		WorkDir:  t.TempDir(),
		Registry: reg,
		Agents:   []engine.Agent{{Name: "build"}},
		Rules:    []permission.Ruleset{permission.Defaults()},
	}, "echo", "echo")

	for _, name := range reg.Names() {
		if !strings.Contains(sys, "`"+name+"`") {
			t.Errorf("registry tool %q missing from guidance", name)
		}
	}
	if strings.Contains(sys, "`task_status` —") {
		t.Fatal("unregistered task_status listed as available")
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
		WorkDir:  t.TempDir(),
		Registry: reg,
		Agents:   []engine.Agent{{Name: "build"}},
		Rules:    []permission.Ruleset{permission.Defaults()},
	}, "echo", "echo")

	if strings.Contains(sys, "`mcp_bulk_t0` —") {
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
