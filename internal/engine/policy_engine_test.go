package engine_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/engine"
	"github.com/jonathanung/strike-cli/internal/permission"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/provider"
	"github.com/jonathanung/strike-cli/internal/tool"
)

func taskToolCallPolicy(id, prompt string, extra map[string]any) provider.ToolCall {
	args := map[string]any{"prompt": prompt}
	for k, v := range extra {
		args[k] = v
	}
	b, _ := json.Marshal(args)
	return provider.ToolCall{ID: id, Name: "task", Args: b}
}

func TestPolicyEngineTinyTaskLocal(t *testing.T) {
	const callID = "task-tiny-local"
	taskCall := taskToolCallPolicy(callID, "fix one typo", nil)
	parent := newScriptedProvider(
		toolCallStep(taskCall),
		func() streamStep {
			s := completedStep("did it locally")
			s.match = matchToolResult(callID)
			return s
		}(),
	)
	eng := engine.New(engine.Options{
		SessionID:       "pol-tiny",
		Select:          func(string) (provider.Provider, string, error) { return parent, "echo-default", nil },
		InitialProvider: "echo",
		InitialModel:    "echo-default",
		Registry:        tool.NewRegistry(tool.NewTask()),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
		Agents:          []engine.Agent{{Name: "build"}, {Name: "general"}},
		DelegationPolicy: engine.DelegationPolicyConfig{
			Mode: engine.PolicyEnforce,
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	_ = waitForEvent(t, eng, func(ev protocol.Event) bool {
		_, ok := ev.(protocol.ModelSelected)
		return ok
	})
	eng.Ops() <- protocol.UserInput{Text: "please help"}
	events := drainAndReply(t, eng, 10*time.Second)

	if n := countEvents[protocol.ChildStarted](events); n != 0 {
		t.Fatalf("ChildStarted = %d, want 0; events=%v", n, summarizeEvents(events))
	}
	_, loc, _, _, _ := eng.PolicyMetricsSnapshot()
	if loc < 1 {
		t.Fatalf("local metric = %d", loc)
	}
}

func TestPolicyEngineIndependentTaskSpawns(t *testing.T) {
	const (
		callID = "task-indep"
		prompt = "find where MaxChildDepth is enforced in the engine"
	)
	taskCall := taskToolCallPolicy(callID, prompt, map[string]any{
		"specialty": "explore",
		"agent":     "explore",
	})
	parent := newScriptedProvider(
		toolCallStep(taskCall),
		func() streamStep {
			s := completedStep("parent after child")
			s.match = matchToolResult(callID)
			return s
		}(),
		func() streamStep {
			s := completedStep(`{"summary":"found","files_changed":[],"verification":"","findings":[],"blockers":[],"recommended_next_action":""}`)
			s.match = matchUserText(prompt)
			return s
		}(),
		childCompletedNudgeStep("ack"),
	)
	eng := engine.New(engine.Options{
		SessionID:       "pol-indep",
		Select:          func(string) (provider.Provider, string, error) { return parent, "echo-default", nil },
		InitialProvider: "echo",
		InitialModel:    "echo-default",
		Registry:        tool.NewRegistry(tool.NewTask()),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
		Agents: []engine.Agent{
			{Name: "build"},
			{Name: "explore", Description: "scout", Capabilities: []string{"explore", "search"}},
			{Name: "general"},
		},
		DelegationPolicy: engine.DelegationPolicyConfig{
			Mode: engine.PolicyEnforce,
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	_ = waitForEvent(t, eng, func(ev protocol.Event) bool {
		_, ok := ev.(protocol.ModelSelected)
		return ok
	})
	eng.Ops() <- protocol.UserInput{Text: "explore"}
	events := drainAndReply(t, eng, 12*time.Second)

	var started protocol.ChildStarted
	found := false
	for _, ev := range events {
		if cs, ok := ev.(protocol.ChildStarted); ok {
			started = cs
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("no ChildStarted; events=%v", summarizeEvents(events))
	}
	if !strings.Contains(started.PolicyReason, "action=delegate") {
		t.Fatalf("PolicyReason = %q", started.PolicyReason)
	}
	del, _, _, _, _ := eng.PolicyMetricsSnapshot()
	if del < 1 {
		t.Fatalf("delegate metric = %d", del)
	}
}

func TestPolicyEngineForceDelegateOverridesTiny(t *testing.T) {
	const (
		callID = "task-force"
		prompt = "tiny"
	)
	// Bare tiny without agent pin — force should override soft local.
	taskCall := taskToolCallPolicy(callID, prompt, map[string]any{
		"force_delegate": true,
	})
	parent := newScriptedProvider(
		toolCallStep(taskCall),
		func() streamStep {
			s := completedStep("parent after")
			s.match = matchToolResult(callID)
			return s
		}(),
		func() streamStep {
			s := completedStep(`{"summary":"ok","files_changed":[],"verification":"","findings":[],"blockers":[],"recommended_next_action":""}`)
			s.match = matchUserText(prompt)
			return s
		}(),
		childCompletedNudgeStep("ack"),
	)
	eng := engine.New(engine.Options{
		SessionID:       "pol-force",
		Select:          func(string) (provider.Provider, string, error) { return parent, "echo-default", nil },
		InitialProvider: "echo",
		InitialModel:    "echo-default",
		Registry:        tool.NewRegistry(tool.NewTask()),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
		Agents:          []engine.Agent{{Name: "build"}, {Name: "general"}},
		DelegationPolicy: engine.DelegationPolicyConfig{
			Mode: engine.PolicyEnforce,
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	_ = waitForEvent(t, eng, func(ev protocol.Event) bool {
		_, ok := ev.(protocol.ModelSelected)
		return ok
	})
	eng.Ops() <- protocol.UserInput{Text: "go"}
	events := drainAndReply(t, eng, 12*time.Second)

	var started protocol.ChildStarted
	found := false
	for _, ev := range events {
		if cs, ok := ev.(protocol.ChildStarted); ok {
			started = cs
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("no ChildStarted; events=%v", summarizeEvents(events))
	}
	if !strings.Contains(started.PolicyReason, "override") && !strings.Contains(started.PolicyReason, "force_delegate") {
		t.Fatalf("PolicyReason = %q want override", started.PolicyReason)
	}
	_, _, _, ov, _ := eng.PolicyMetricsSnapshot()
	if ov < 1 {
		t.Fatalf("override metric = %d", ov)
	}
}

func TestPolicyEngineAgentPinWithPathsDelegates(t *testing.T) {
	// Overlap→local is covered in package engine (TestSpawnChildPolicyOverlapLocal).
	// Here: agent pin + paths without overlap still fans out.
	const (
		callID = "task-paths-ok"
		prompt = "edit the shared file carefully with full context and tests"
	)
	dir := t.TempDir()
	taskCall := taskToolCallPolicy(callID, prompt, map[string]any{
		"agent": "general",
		"context_bundle": map[string]any{
			"allowed_paths": []string{"shared.go"},
		},
	})
	parent := newScriptedProvider(
		toolCallStep(taskCall),
		func() streamStep {
			s := completedStep("parent after")
			s.match = matchToolResult(callID)
			return s
		}(),
		func() streamStep {
			s := completedStep(`{"summary":"ok","files_changed":["shared.go"],"verification":"","findings":[],"blockers":[],"recommended_next_action":""}`)
			s.match = matchUserText(prompt)
			return s
		}(),
		childCompletedNudgeStep("ack"),
	)
	eng := engine.New(engine.Options{
		SessionID:       "pol-paths-ok",
		Select:          func(string) (provider.Provider, string, error) { return parent, "echo-default", nil },
		InitialProvider: "echo",
		InitialModel:    "echo-default",
		Registry:        tool.NewRegistry(tool.NewTask()),
		WorkDir:         dir,
		Rules:           []permission.Ruleset{permission.Defaults()},
		Agents:          []engine.Agent{{Name: "build"}, {Name: "general"}},
		DelegationPolicy: engine.DelegationPolicyConfig{
			Mode: engine.PolicyEnforce,
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	_ = waitForEvent(t, eng, func(ev protocol.Event) bool {
		_, ok := ev.(protocol.ModelSelected)
		return ok
	})
	eng.Ops() <- protocol.UserInput{Text: "edit"}
	events := drainAndReply(t, eng, 12*time.Second)
	if n := countEvents[protocol.ChildStarted](events); n != 1 {
		t.Fatalf("ChildStarted = %d want 1 (agent pin, no overlap); events=%v", n, summarizeEvents(events))
	}
}

func TestPolicyEngineExhaustedBudgetDenies(t *testing.T) {
	const callID = "task-budget"
	taskCall := taskToolCallPolicy(callID, "big independent investigation of the whole codebase architecture", map[string]any{
		"agent":          "explore",
		"force_delegate": true,
	})
	parent := newScriptedProvider(
		toolCallStep(taskCall),
		func() streamStep {
			// Tool returns error → model may still complete.
			s := completedStep("could not delegate")
			s.match = matchToolResult(callID)
			return s
		}(),
	)
	eng := engine.New(engine.Options{
		SessionID:       "pol-budget",
		Select:          func(string) (provider.Provider, string, error) { return parent, "echo-default", nil },
		InitialProvider: "echo",
		InitialModel:    "echo-default",
		Registry:        tool.NewRegistry(tool.NewTask()),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
		Agents:          []engine.Agent{{Name: "build"}, {Name: "explore"}},
		DelegationPolicy: engine.DelegationPolicyConfig{
			Mode: engine.PolicyEnforce,
		},
		SessionBudgetExhausted: func() bool { return true },
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	_ = waitForEvent(t, eng, func(ev protocol.Event) bool {
		_, ok := ev.(protocol.ModelSelected)
		return ok
	})
	eng.Ops() <- protocol.UserInput{Text: "go"}
	events := drainAndReply(t, eng, 10*time.Second)

	if n := countEvents[protocol.ChildStarted](events); n != 0 {
		t.Fatalf("ChildStarted = %d want 0; events=%v", n, summarizeEvents(events))
	}
	_, _, den, _, _ := eng.PolicyMetricsSnapshot()
	if den < 1 {
		t.Fatalf("deny metric = %d", den)
	}
}

func TestPolicyEngineMaxLiveChildrenDenies(t *testing.T) {
	// Pure evaluation covers the ceiling; engine path uses liveChildCount.
	// Call EvaluateDelegationPolicy with live children at ceiling.
	d := engine.EvaluateDelegationPolicy(engine.PolicyInput{
		Config: engine.DelegationPolicyConfig{
			Mode:            engine.PolicyEnforce,
			MaxLiveChildren: 1,
		},
		Prompt:       "independent work",
		AgentPin:     "general",
		LiveChildren: 1,
		Force:        true,
	})
	if d.Action != engine.PolicyActionDeny {
		t.Fatalf("action = %q want deny; %+v", d.Action, d)
	}
	if !strings.Contains(d.Reason, "child_count_ceiling") {
		t.Fatalf("reason = %q", d.Reason)
	}
}
