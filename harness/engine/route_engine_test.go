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
	"github.com/jonathanung/strike-cli/pkg/protocol"
)

func taskToolCallRoute(id, prompt string, extra map[string]any) provider.ToolCall {
	args := map[string]any{"prompt": prompt}
	for k, v := range extra {
		args[k] = v
	}
	b, _ := json.Marshal(args)
	return provider.ToolCall{ID: id, Name: "task", Args: b}
}

func TestTaskRouteAutoSelectsExploreAndRecordsReason(t *testing.T) {
	const prompt = "find the main package"
	taskCall := taskToolCallRoute("task-auto-route", prompt, map[string]any{
		"route":     "auto",
		"specialty": "explore",
	})
	parent := newScriptedProvider(
		toolCallStep(taskCall),
		func() streamStep {
			s := completedStep("parent after child")
			s.match = matchToolResult("task-auto-route")
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
		SessionID:       "parent-auto-route",
		Select:          func(string) (provider.Provider, string, error) { return parent, "echo-default", nil },
		InitialProvider: "echo",
		InitialModel:    "echo-default",
		Registry:        tool.NewRegistry(tool.NewTask()),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
		Agents: []engine.Agent{
			{Name: "build"},
			{Name: "explore", Description: "scout"},
			{Name: "general"},
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	_ = waitForEvent(t, eng, func(ev protocol.Event) bool {
		_, ok := ev.(protocol.ModelSelected)
		return ok
	})

	eng.Ops() <- protocol.UserInput{Text: "route an explore task"}
	events := drainAndReply(t, eng, 10*time.Second)

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
	if started.Agent != "explore" {
		t.Fatalf("ChildStarted.Agent = %q, want explore", started.Agent)
	}
	if !strings.Contains(started.RouteReason, "route=auto") || !strings.Contains(started.RouteReason, "explore") {
		t.Fatalf("RouteReason = %q", started.RouteReason)
	}
}

func TestTaskRoutePinOverridesSpecialty(t *testing.T) {
	const prompt = "pinned worker"
	taskCall := taskToolCallRoute("task-pin-route", prompt, map[string]any{
		"route":     "auto",
		"specialty": "explore",
		"agent":     "general",
	})
	prov := newScriptedProvider(
		toolCallStep(taskCall),
		func() streamStep {
			s := completedStep("parent after")
			s.match = matchToolResult("task-pin-route")
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
		SessionID:       "parent-pin-route",
		Select:          func(string) (provider.Provider, string, error) { return prov, "m", nil },
		InitialProvider: "echo",
		InitialModel:    "m",
		Registry:        tool.NewRegistry(tool.NewTask()),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
		Agents: []engine.Agent{
			{Name: "build"},
			{Name: "explore"},
			{Name: "general"},
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	_ = waitForEvent(t, eng, func(ev protocol.Event) bool {
		_, ok := ev.(protocol.ModelSelected)
		return ok
	})
	eng.Ops() <- protocol.UserInput{Text: "pin general"}
	events := drainAndReply(t, eng, 10*time.Second)
	var started protocol.ChildStarted
	for _, ev := range events {
		if cs, ok := ev.(protocol.ChildStarted); ok {
			started = cs
			break
		}
	}
	if started.Agent != "general" {
		t.Fatalf("Agent = %q, want general (pin wins); reason=%q events=%v", started.Agent, started.RouteReason, summarizeEvents(events))
	}
	if !strings.Contains(started.RouteReason, "pin") {
		t.Fatalf("RouteReason = %q, want pin", started.RouteReason)
	}
}

func TestTaskRouteOffDoesNotForceAgent(t *testing.T) {
	// No route/specialty: child inherits parent agent (build).
	const prompt = "plain spawn"
	taskCall := taskToolCallRoute("task-off", prompt, map[string]any{})
	prov := newScriptedProvider(
		toolCallStep(taskCall),
		func() streamStep {
			s := completedStep("parent after")
			s.match = matchToolResult("task-off")
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
		SessionID:       "parent-off-route",
		Select:          func(string) (provider.Provider, string, error) { return prov, "m", nil },
		InitialProvider: "echo",
		InitialModel:    "m",
		InitialAgent:    "build",
		Registry:        tool.NewRegistry(tool.NewTask()),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
		Agents: []engine.Agent{
			{Name: "build"},
			{Name: "explore"},
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	_ = waitForEvent(t, eng, func(ev protocol.Event) bool {
		_, ok := ev.(protocol.ModelSelected)
		return ok
	})
	eng.Ops() <- protocol.UserInput{Text: "plain"}
	events := drainAndReply(t, eng, 10*time.Second)
	var started protocol.ChildStarted
	for _, ev := range events {
		if cs, ok := ev.(protocol.ChildStarted); ok {
			started = cs
			break
		}
	}
	if started.Agent != "build" {
		t.Fatalf("Agent = %q, want build inherit; reason=%q", started.Agent, started.RouteReason)
	}
	if started.RouteReason != "" && !strings.Contains(started.RouteReason, "route=off") {
		t.Fatalf("RouteReason = %q, want off/empty", started.RouteReason)
	}
}
