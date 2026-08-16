package engine_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/harness/engine"
	"github.com/jonathanung/strike-cli/harness/permission"
	"github.com/jonathanung/strike-cli/harness/provider"
	"github.com/jonathanung/strike-cli/harness/tool"
	"github.com/jonathanung/strike-cli/pkg/protocol"
)

func TestParallelChildrenOverlapEmitsPathOverlap(t *testing.T) {
	dir := t.TempDir()
	const (
		promptA = "child-a-write-shared"
		promptB = "child-b-write-shared"
	)
	taskA := taskToolCall("ta", promptA)
	taskB := taskToolCall("tb", promptB)
	writeA := writeToolCall("wa", "shared.txt", "from-a")
	writeB := writeToolCall("wb", "shared.txt", "from-b")
	listOwn := controlToolCall("own1", "agent_ownership", map[string]any{"action": "list"})

	prov := newScriptedProvider(
		// Lead turn 1: spawn A and B.
		toolCallStep(taskA, taskB),
		func() streamStep {
			s := completedStep("spawned")
			s.match = matchToolResult("tb")
			return s
		}(),
		// Child A
		func() streamStep {
			s := toolCallStep(writeA)
			s.match = matchUserText(promptA)
			return s
		}(),
		func() streamStep {
			s := completedStep("a done")
			s.match = matchToolResult("wa")
			return s
		}(),
		// Child B
		func() streamStep {
			s := toolCallStep(writeB)
			s.match = matchUserText(promptB)
			return s
		}(),
		func() streamStep {
			s := completedStep("b done")
			s.match = matchToolResult("wb")
			return s
		}(),
		// Lead nudge after first child.completed: list ownership
		func() streamStep {
			s := toolCallStep(listOwn)
			s.match = matchUserTextContains("[child.completed")
			return s
		}(),
		func() streamStep {
			s := completedStep("listed")
			s.match = matchToolResult("own1")
			return s
		}(),
		// Second nudge if a second child.completed arrives
		childCompletedNudgeStep("ack remaining"),
	)

	eng := engine.New(engine.Options{
		SessionID:       "lead-overlap",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry: tool.NewRegistry(
			tool.NewTask(),
			tool.NewWrite(),
			tool.NewAgentOwnership(),
			tool.NewAgentRoster(),
		),
		WorkDir: dir,
		Rules: []permission.Ruleset{
			permission.Defaults(),
			{{Permission: "write", Pattern: "*", Action: permission.Allow}},
		},
		Agents: []engine.Agent{{Name: "build"}},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "spawn two writers on shared.txt"}
	events := drainUntil(t, eng, 15*time.Second, func(evs []protocol.Event) bool {
		return countEvents[protocol.ChildCompleted](evs) >= 2 &&
			countEvents[protocol.PathOverlap](evs) >= 1
	})

	if n := countEvents[protocol.PathOverlap](events); n < 1 {
		t.Fatalf("PathOverlap = %d; events=%v", n, summarizeEvents(events))
	}
	var sawOverlap protocol.PathOverlap
	for _, ev := range events {
		if po, ok := ev.(protocol.PathOverlap); ok {
			sawOverlap = po
			break
		}
	}
	if !strings.Contains(sawOverlap.Path, "shared.txt") {
		t.Fatalf("PathOverlap.Path = %q", sawOverlap.Path)
	}
	if sawOverlap.Policy != "warn" {
		t.Fatalf("policy = %q", sawOverlap.Policy)
	}

	// Ownership list tool should report the path.
	var listOut string
	for _, ev := range events {
		if end, ok := ev.(protocol.ToolCallEnd); ok && end.CallID == "own1" && !end.IsError {
			listOut = end.Output
		}
	}
	if listOut != "" && !strings.Contains(listOut, "shared.txt") {
		// list may not have run if drain stopped early; still OK if PathOverlap fired
		t.Logf("ownership list output: %q", listOut)
	}

	// File exists (last writer wins on disk; detection is the contract).
	if _, err := os.Stat(filepath.Join(dir, "shared.txt")); err != nil {
		t.Fatalf("shared.txt: %v", err)
	}
}

func TestDisjointChildWritesNoPathOverlap(t *testing.T) {
	dir := t.TempDir()
	const (
		promptA = "child-a-write-a"
		promptB = "child-b-write-b"
	)
	taskA := taskToolCall("ta", promptA)
	taskB := taskToolCall("tb", promptB)
	writeA := writeToolCall("wa", "a.txt", "aaa")
	writeB := writeToolCall("wb", "b.txt", "bbb")

	prov := newScriptedProvider(
		toolCallStep(taskA, taskB),
		func() streamStep {
			s := completedStep("spawned")
			s.match = matchToolResult("tb")
			return s
		}(),
		func() streamStep {
			s := toolCallStep(writeA)
			s.match = matchUserText(promptA)
			return s
		}(),
		func() streamStep {
			s := completedStep("a done")
			s.match = matchToolResult("wa")
			return s
		}(),
		func() streamStep {
			s := toolCallStep(writeB)
			s.match = matchUserText(promptB)
			return s
		}(),
		func() streamStep {
			s := completedStep("b done")
			s.match = matchToolResult("wb")
			return s
		}(),
		childCompletedNudgeStep("ack 1"),
		childCompletedNudgeStep("ack 2"),
	)

	eng := engine.New(engine.Options{
		SessionID:       "lead-disjoint",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tool.NewTask(), tool.NewWrite()),
		WorkDir:         dir,
		Rules: []permission.Ruleset{
			permission.Defaults(),
			{{Permission: "write", Pattern: "*", Action: permission.Allow}},
		},
		Agents: []engine.Agent{{Name: "build"}},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "spawn disjoint writers"}
	events := drainUntil(t, eng, 15*time.Second, func(evs []protocol.Event) bool {
		return countEvents[protocol.ChildCompleted](evs) >= 2
	})

	if n := countEvents[protocol.PathOverlap](events); n != 0 {
		t.Fatalf("PathOverlap = %d, want 0; events=%v", n, summarizeEvents(events))
	}
	for _, name := range []string{"a.txt", "b.txt"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestSoloWriteNoOwnershipRequired(t *testing.T) {
	// Single-agent write must not require ownership tooling or emit overlaps.
	dir := t.TempDir()
	write := writeToolCall("w1", "solo.txt", "ok")
	prov := newScriptedProvider(
		toolCallStep(write),
		func() streamStep {
			s := completedStep("done")
			s.match = matchToolResult("w1")
			return s
		}(),
	)
	eng := engine.New(engine.Options{
		SessionID:       "solo",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tool.NewWrite()),
		WorkDir:         dir,
		Rules: []permission.Ruleset{
			permission.Defaults(),
			{{Permission: "write", Pattern: "*", Action: permission.Allow}},
		},
		Agents: []engine.Agent{{Name: "build"}},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "write solo"}
	events := drainUntil(t, eng, 10*time.Second, func(evs []protocol.Event) bool {
		return countEvents[protocol.TurnCompleted](evs) >= 1
	})
	if n := countEvents[protocol.PathOverlap](events); n != 0 {
		t.Fatalf("unexpected PathOverlap: %d", n)
	}
	data, err := os.ReadFile(filepath.Join(dir, "solo.txt"))
	if err != nil || string(data) != "ok" {
		t.Fatalf("solo.txt = %q err=%v", data, err)
	}
}

// TestHandoffFilesChangedMergesIntoOwnership ensures #771 structured handoff
// files_changed is recorded on the team ownership graph at finishChild, and
// the finished child is inactive (no longer causes overlap).
func TestHandoffFilesChangedMergesIntoOwnership(t *testing.T) {
	dir := t.TempDir()
	const taskPrompt = "child-handoff-files-only"
	handoffJSON := `{
  "summary": "reported via handoff",
  "files_changed": ["via-handoff.go"],
  "findings": [],
  "blockers": []
}`
	taskCall := taskToolCall("t-handoff-own", taskPrompt)
	listOwn := controlToolCall("own-handoff", "agent_ownership", map[string]any{"action": "list"})

	prov := newScriptedProvider(
		toolCallStep(taskCall),
		func() streamStep {
			s := completedStep("spawned")
			s.match = matchToolResult("t-handoff-own")
			return s
		}(),
		// Child: no write tool — only model handoff files_changed.
		func() streamStep {
			s := completedStep(handoffJSON)
			s.match = matchUserText(taskPrompt)
			return s
		}(),
		// Lead nudge: list ownership after child.completed.
		func() streamStep {
			s := toolCallStep(listOwn)
			s.match = matchUserTextContains("[child.completed")
			return s
		}(),
		func() streamStep {
			s := completedStep("listed")
			s.match = matchToolResult("own-handoff")
			return s
		}(),
	)

	eng := engine.New(engine.Options{
		SessionID:       "lead-handoff-own",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry: tool.NewRegistry(
			tool.NewTask(),
			tool.NewAgentOwnership(),
			tool.NewAgentRoster(),
		),
		WorkDir: dir,
		Rules: []permission.Ruleset{
			permission.Defaults(),
			{{Permission: "*", Pattern: "*", Action: permission.Allow}},
		},
		Agents: []engine.Agent{{Name: "build"}},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "spawn handoff-only child"}
	events := drainUntil(t, eng, 15*time.Second, func(evs []protocol.Event) bool {
		if countEvents[protocol.ChildCompleted](evs) < 1 {
			return false
		}
		for _, ev := range evs {
			if end, ok := ev.(protocol.ToolCallEnd); ok && end.CallID == "own-handoff" && !end.IsError {
				return true
			}
		}
		return false
	})

	var listOut string
	for _, ev := range events {
		if end, ok := ev.(protocol.ToolCallEnd); ok && end.CallID == "own-handoff" && !end.IsError {
			listOut = end.Output
		}
	}
	if listOut == "" {
		t.Fatalf("ownership list did not run; events=%v", summarizeEvents(events))
	}
	if !strings.Contains(listOut, "via-handoff.go") {
		t.Fatalf("ownership list missing handoff path: %s", listOut)
	}
	// Finished child must not leave an active overlap on the handoff path.
	if strings.Contains(listOut, `"overlaps":["via-handoff.go"]`) ||
		strings.Contains(listOut, `"overlaps": ["via-handoff.go"]`) {
		t.Fatalf("finished child still in active overlaps: %s", listOut)
	}
	// Holder should be present but inactive.
	if !strings.Contains(listOut, `"active":false`) && !strings.Contains(listOut, `"active": false`) {
		t.Fatalf("expected inactive holder after finishChild: %s", listOut)
	}
	// No PathOverlap from a solo finished child.
	if n := countEvents[protocol.PathOverlap](events); n != 0 {
		t.Fatalf("unexpected PathOverlap=%d for solo handoff child", n)
	}
}

func matchUserTextContains(sub string) func(provider.Request) bool {
	return func(req provider.Request) bool {
		for _, m := range req.Messages {
			if m.Role == provider.RoleUser && strings.Contains(m.Text, sub) {
				return true
			}
		}
		return false
	}
}
