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

func TestTaskSpawnDerivesNameFromPrompt(t *testing.T) {
	const (
		leadID = "lead-derived-name"
		prompt = "Fix the auth bug"
	)
	taskCall := taskToolCallNamed("task-derived", prompt, "", "")
	rosterCall := controlToolCall("ar-d", "agent_roster", map[string]any{})

	prov := newScriptedProvider(
		toolCallStep(taskCall),
		func() streamStep {
			s := completedStep("child-done")
			s.match = matchUserText(prompt)
			return s
		}(),
		func() streamStep {
			s := toolCallStep(rosterCall)
			s.match = matchToolResult("task-derived")
			return s
		}(),
		func() streamStep {
			s := completedStep("parent after roster")
			s.match = matchToolResult("ar-d")
			return s
		}(),
		childCompletedNudgeStep("ack derived"),
	)

	eng := engine.New(engine.Options{
		SessionID:       leadID,
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		InitialAgent:    "build",
		Agents:          []engine.Agent{{Name: "build"}},
		Registry: tool.NewRegistry(
			tool.NewTask(),
			tool.NewAgentRoster(),
		),
		WorkDir: t.TempDir(),
		Rules:   []permission.Ruleset{permission.Defaults()},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "spawn unnamed"}
	events := drainAndReply(t, eng, 20*time.Second)

	want := engine.DeriveMemberName(prompt)
	var started protocol.ChildStarted
	var rosterOut string
	var sawStart bool
	for _, ev := range events {
		switch ev := ev.(type) {
		case protocol.ChildStarted:
			started = ev
			sawStart = true
		case protocol.ToolCallEnd:
			if ev.CallID == "ar-d" {
				rosterOut = ev.Output
			}
		case protocol.EngineError:
			t.Fatalf("engine error: %s", ev.Message)
		}
	}
	if !sawStart {
		t.Fatalf("missing ChildStarted; events=%v", summarizeEvents(events))
	}
	if started.Name != want {
		t.Fatalf("ChildStarted.Name = %q, want %q", started.Name, want)
	}
	if _, err := engine.ValidateMemberName(started.Name); err != nil {
		t.Fatalf("derived name invalid: %v", err)
	}
	tm := eng.Team()
	m, ok := tm.Member(started.SessionID)
	if !ok || m.Name != want {
		t.Fatalf("team member = %+v ok=%v", m, ok)
	}
	id, ok := tm.Resolve(want)
	if !ok || id != started.SessionID {
		t.Fatalf("Resolve(%s) = %q ok=%v", want, id, ok)
	}
	if rosterOut == "" {
		t.Fatal("missing agent_roster output")
	}
	var parsed tool.AgentRosterResult
	if err := json.Unmarshal([]byte(rosterOut), &parsed); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, mem := range parsed.Members {
		if mem.SessionID == started.SessionID {
			found = true
			if mem.Name != want {
				t.Fatalf("roster name = %q, want %q", mem.Name, want)
			}
		}
	}
	if !found {
		t.Fatalf("child missing from roster: %+v", parsed)
	}
}

func TestTaskSpawnExplicitNameWins(t *testing.T) {
	const (
		leadID = "lead-explicit-wins"
		prompt = "Fix the auth bug"
		name   = "ship-it"
	)
	taskCall := taskToolCallNamed("task-explicit", prompt, name, "")

	prov := newScriptedProvider(
		toolCallStep(taskCall),
		func() streamStep {
			s := completedStep("child-done")
			s.match = matchUserText(prompt)
			return s
		}(),
		func() streamStep {
			s := completedStep("parent after spawn")
			s.match = matchToolResult("task-explicit")
			return s
		}(),
		childCompletedNudgeStep("ack explicit"),
	)

	eng := engine.New(engine.Options{
		SessionID:       leadID,
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		InitialAgent:    "build",
		Agents:          []engine.Agent{{Name: "build"}},
		Registry:        tool.NewRegistry(tool.NewTask()),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "spawn explicit"}
	events := drainAndReply(t, eng, 20*time.Second)

	var started protocol.ChildStarted
	var sawStart bool
	for _, ev := range events {
		switch ev := ev.(type) {
		case protocol.ChildStarted:
			started = ev
			sawStart = true
		case protocol.EngineError:
			t.Fatalf("engine error: %s", ev.Message)
		}
	}
	if !sawStart {
		t.Fatalf("missing ChildStarted; events=%v", summarizeEvents(events))
	}
	if started.Name != name {
		t.Fatalf("ChildStarted.Name = %q, want explicit %q (not derived %q)", started.Name, name, engine.DeriveMemberName(prompt))
	}
}

func TestTaskSpawnDerivedNameCollisionSuffix(t *testing.T) {
	const (
		leadID  = "lead-derived-collision"
		promptA = "Fix auth\nfirst child"
		promptB = "Fix auth\nsecond child"
	)
	taskA := taskToolCallNamed("task-a", promptA, "", "")
	taskB := taskToolCallNamed("task-b", promptB, "", "")

	prov := newScriptedProvider(
		toolCallStep(taskA),
		func() streamStep {
			s := completedStep("a-done")
			s.match = matchUserText(promptA)
			return s
		}(),
		func() streamStep {
			s := toolCallStep(taskB)
			s.match = matchToolResult("task-a")
			return s
		}(),
		func() streamStep {
			s := completedStep("b-done")
			s.match = matchUserText(promptB)
			return s
		}(),
		func() streamStep {
			s := completedStep("parent after both")
			s.match = matchToolResult("task-b")
			return s
		}(),
		childCompletedNudgeStep("ack"),
	)

	eng := engine.New(engine.Options{
		SessionID:       leadID,
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		InitialAgent:    "build",
		Agents:          []engine.Agent{{Name: "build"}},
		Registry:        tool.NewRegistry(tool.NewTask()),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "spawn two"}
	events := drainAndReply(t, eng, 20*time.Second)

	var names []string
	for _, ev := range events {
		switch ev := ev.(type) {
		case protocol.ChildStarted:
			names = append(names, ev.Name)
		case protocol.EngineError:
			t.Fatalf("engine error: %s", ev.Message)
		}
	}
	if len(names) != 2 {
		t.Fatalf("ChildStarted names = %v, want 2", names)
	}
	if names[0] != "fix-auth" {
		t.Fatalf("first derived = %q, want fix-auth", names[0])
	}
	if names[1] != "fix-auth-2" {
		t.Fatalf("collision suffix = %q, want fix-auth-2", names[1])
	}
	if names[0] == names[1] {
		t.Fatal("derived names collided")
	}
}

func TestTaskSpawnInvalidExplicitNameRejected(t *testing.T) {
	const (
		leadID = "lead-invalid-name"
		prompt = "Fix the auth bug"
	)
	taskCall := taskToolCallNamed("task-bad", prompt, "bad name!", "")

	prov := newScriptedProvider(
		toolCallStep(taskCall),
		func() streamStep {
			s := completedStep("parent after bad")
			s.match = matchToolResult("task-bad")
			return s
		}(),
	)

	eng := engine.New(engine.Options{
		SessionID:       leadID,
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		InitialAgent:    "build",
		Agents:          []engine.Agent{{Name: "build"}},
		Registry:        tool.NewRegistry(tool.NewTask()),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "bad name"}
	events := drainAndReply(t, eng, 20*time.Second)

	var started int
	var taskErr string
	for _, ev := range events {
		switch ev := ev.(type) {
		case protocol.ChildStarted:
			started++
		case protocol.ToolCallEnd:
			if ev.CallID == "task-bad" && ev.IsError {
				taskErr = ev.Output
			}
		case protocol.EngineError:
			t.Fatalf("engine error: %s", ev.Message)
		}
	}
	if started != 0 {
		t.Fatalf("ChildStarted = %d, want 0", started)
	}
	if taskErr == "" {
		t.Fatal("expected invalid name error")
	}
	if !strings.Contains(taskErr, "whitespace") && !strings.Contains(taskErr, "letters") {
		t.Fatalf("task error = %q, want validation failure", taskErr)
	}
}
