package engine_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/engine"
	"github.com/jonathanung/strike-cli/internal/permission"
	"github.com/jonathanung/strike-cli/internal/plan"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/provider"
	"github.com/jonathanung/strike-cli/internal/tool"
)

func activationRegistry(t *testing.T) *tool.Registry {
	t.Helper()
	store, err := plan.Open(t.TempDir(), "act-proj")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	reg := tool.NewRegistry(
		tool.NewRead(),
		tool.NewBash(),
		tool.NewTask(),
		tool.NewQuestion(),
		tool.NewPlanWrite(store),
		tool.NewPlanRead(store),
		tool.NewPlanDelegate(store),
		tool.NewEnterPlanMode(),
		tool.NewExitPlanMode(),
		tool.NewPhaseDone(),
		tool.NewAgentRoster(),
		tool.NewAgentOwnership(),
		tool.NewAgentMessage(),
		tool.NewAgentBroadcast(),
		tool.NewAgentThread(),
		tool.NewTeamTask(),
		tool.NewWait(),
		tool.NewDelegate(),
		tool.NewTaskStatus(),
		tool.NewWebFetch(),
	)
	reg.Register(tool.NewToolSearch(reg))
	reg.SetDeferLoading(true)
	return reg
}

func toolNames(schemas []provider.ToolSchema) map[string]bool {
	out := make(map[string]bool, len(schemas))
	for _, s := range schemas {
		out[s.Name] = true
	}
	return out
}

// TestPlanModeActivatesPlanFamily: plan permission mode exposes plan tools
// without toolsearch (#991).
func TestPlanModeActivatesPlanFamily(t *testing.T) {
	reg := activationRegistry(t)
	req := captureStreamRequest(t, engine.Options{
		WorkDir:               t.TempDir(),
		Registry:              reg,
		Agents:                []engine.Agent{{Name: "build"}, {Name: "plan"}},
		Rules:                 []permission.Ruleset{permission.Defaults()},
		InitialPermissionMode: protocol.PermissionModePlan,
	}, "echo", "echo")

	names := toolNames(req.Tools)
	for _, want := range []string{"plan_write", "plan_read", "enter_plan_mode", "exit_plan_mode", "phase_done", "read", "task"} {
		if !names[want] {
			t.Errorf("plan mode missing %q: %v", want, names)
		}
	}
	// Unrelated deferred tools stay omitted.
	if names["webfetch"] {
		t.Fatal("webfetch should stay deferred in plan mode")
	}
	// Hard-deny still authoritative: deny plan_write and ensure absent.
	reg2 := activationRegistry(t)
	req2 := captureStreamRequest(t, engine.Options{
		WorkDir:  t.TempDir(),
		Registry: reg2,
		Agents:   []engine.Agent{{Name: "build"}, {Name: "plan"}},
		Rules: []permission.Ruleset{
			permission.Defaults(),
			{{Permission: "plan_write", Pattern: "*", Action: permission.Deny}},
		},
		InitialPermissionMode: protocol.PermissionModePlan,
	}, "echo", "echo")
	if toolNames(req2.Tools)["plan_write"] {
		t.Fatal("hard-denied plan_write must stay absent even when activation matches")
	}
}

// TestPlanAgentActivatesPlanFamily: plan persona alone activates plan tools.
func TestPlanAgentActivatesPlanFamily(t *testing.T) {
	reg := activationRegistry(t)
	req := captureStreamRequest(t, engine.Options{
		WorkDir:      t.TempDir(),
		Registry:     reg,
		Agents:       []engine.Agent{{Name: "plan"}, {Name: "build"}},
		Rules:        []permission.Ruleset{permission.Defaults()},
		InitialAgent: "plan",
	}, "echo", "echo")
	if !toolNames(req.Tools)["plan_write"] {
		t.Fatalf("plan agent should activate plan_write: %v", toolNames(req.Tools))
	}
}

// TestSoloSessionOmitsCoordination: no children → no roster/team tools.
func TestSoloSessionOmitsCoordination(t *testing.T) {
	reg := activationRegistry(t)
	req := captureStreamRequest(t, engine.Options{
		WorkDir:  t.TempDir(),
		Registry: reg,
		Agents:   []engine.Agent{{Name: "build"}},
		Rules:    []permission.Ruleset{permission.Defaults()},
	}, "echo", "echo")
	names := toolNames(req.Tools)
	for _, no := range []string{"agent_roster", "agent_message", "agent_broadcast", "team_task", "plan_write"} {
		if names[no] {
			t.Errorf("solo session should omit %q", no)
		}
	}
	// Core still present.
	if !names["read"] || !names["task"] || !names["toolsearch"] {
		t.Fatalf("core missing: %v", names)
	}
	// task stays basic (no transition) without children.
	for _, s := range req.Tools {
		if s.Name == "task" && strings.Contains(string(s.InputSchema), `"transition"`) {
			t.Fatal("solo task should stay basic schema")
		}
	}
}

// TestChildCreationActivatesCoordination: after a child exists, next stream
// exposes roster/message/ownership and advanced task.
func TestChildCreationActivatesCoordination(t *testing.T) {
	reg := activationRegistry(t)

	// Script: first stream spawn task, second stream after child.started should
	// include coordination tools. We simulate by seeding child via a full
	// engine run with task spawn, then capturing the post-tool stream.
	callID := "task-1"
	prov := newScriptedProvider(
		toolCallStep(provider.ToolCall{
			ID:   callID,
			Name: "task",
			Args: json.RawMessage(`{"prompt":"explore pkg"}`),
		}),
		streamStep{
			match: matchToolResult(callID),
			events: []provider.StreamEvent{
				{Type: provider.EventTextDelta, Text: "spawned"},
				{Type: provider.EventDone, StopReason: "end_turn"},
			},
		},
	)

	// Child provider ends immediately so parent can continue.
	childProv := newScriptedProvider(streamStep{
		events: []provider.StreamEvent{
			{Type: provider.EventTextDelta, Text: "child done"},
			{Type: provider.EventDone, StopReason: "end_turn"},
		},
	})

	eng := engine.New(engine.Options{
		SessionID: "s-activate-child",
		WorkDir:   t.TempDir(),
		Registry:  reg,
		Agents:    []engine.Agent{{Name: "build"}, {Name: "explore"}},
		Rules:     []permission.Ruleset{permission.Defaults()},
		Select: func(name string) (provider.Provider, string, error) {
			// Parent uses scripted; children get echo-like childProv.
			if name != "" && name != "echo" {
				return childProv, "echo", nil
			}
			return prov, "echo", nil
		},
		InitialProvider: "echo",
		InitialModel:    "echo",
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "spawn a helper"}
	req1 := waitStreamRequest(t, eng, prov)
	if toolNames(req1.Tools)["agent_roster"] {
		t.Fatal("first stream should not have roster before child")
	}

	req2 := waitStreamRequest(t, eng, prov)
	names2 := toolNames(req2.Tools)
	for _, want := range []string{"agent_roster", "agent_message", "agent_ownership", "wait"} {
		if !names2[want] {
			t.Errorf("post-child stream missing %q: %v", want, names2)
		}
	}
	// team tools require 2+ live children
	if names2["agent_broadcast"] || names2["team_task"] {
		t.Fatalf("single child should not activate team family: %v", names2)
	}
	// advanced task
	for _, s := range req2.Tools {
		if s.Name == "task" && !strings.Contains(string(s.InputSchema), `"transition"`) {
			t.Fatal("post-child task should be advanced schema")
		}
	}
	waitTurnCompleted(t, eng)
}

// TestActivationSourceMentionsFamilies: plan activation exposes plan tools.
func TestActivationSourceMentionsFamilies(t *testing.T) {
	reg := activationRegistry(t)
	req := captureStreamRequest(t, engine.Options{
		WorkDir:               t.TempDir(),
		Registry:              reg,
		Agents:                []engine.Agent{{Name: "plan"}},
		Rules:                 []permission.Ruleset{permission.Defaults()},
		InitialAgent:          "plan",
		InitialPermissionMode: protocol.PermissionModePlan,
	}, "echo", "echo")
	if !toolNames(req.Tools)["plan_write"] {
		t.Fatal("expected plan activation")
	}
}
