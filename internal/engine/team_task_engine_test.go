package engine_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/engine"
	"github.com/jonathanung/strike-cli/internal/permission"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/provider"
	"github.com/jonathanung/strike-cli/internal/tool"
)

func TestTeamTaskTwoChildrenShareBoardAndClaim(t *testing.T) {
	const (
		leadID  = "lead-board"
		promptA = "child-a-board-work"
		promptB = "child-b-board-work"
	)

	create1 := controlToolCall("c1", "team_task", map[string]any{
		"action": "create", "content": "slice-alpha",
	})
	create2 := controlToolCall("c2", "team_task", map[string]any{
		"action": "create", "content": "slice-beta",
	})
	taskA := taskToolCall("task-a", promptA)
	taskB := taskToolCall("task-b", promptB)

	// Child A: list (shared board) → claim t1 → complete t1
	listA := controlToolCall("la", "team_task", map[string]any{"action": "list"})
	claimA := controlToolCall("ca", "team_task", map[string]any{"action": "claim", "id": "t1"})
	completeA := controlToolCall("xa", "team_task", map[string]any{"action": "complete", "id": "t1"})

	// Child B: list → claim t2 → complete t2
	listB := controlToolCall("lb", "team_task", map[string]any{"action": "list"})
	claimB := controlToolCall("cb", "team_task", map[string]any{"action": "claim", "id": "t2"})
	completeB := controlToolCall("xb", "team_task", map[string]any{"action": "complete", "id": "t2"})

	prov := newScriptedProvider(
		toolCallStep(create1, create2, taskA, taskB),
		func() streamStep {
			s := toolCallStep(listA, claimA, completeA)
			s.match = matchUserText(promptA)
			return s
		}(),
		func() streamStep {
			s := completedStep("child-a-done")
			s.match = matchToolResult("xa")
			return s
		}(),
		func() streamStep {
			s := toolCallStep(listB, claimB, completeB)
			s.match = matchUserText(promptB)
			return s
		}(),
		func() streamStep {
			s := completedStep("child-b-done")
			s.match = matchToolResult("xb")
			return s
		}(),
		func() streamStep {
			s := completedStep("parent after board")
			s.match = func(req provider.Request) bool {
				var sawA, sawB bool
				for _, m := range req.Messages {
					if m.Role == provider.RoleTool && m.ToolResult != nil {
						if m.ToolResult.CallID == "task-a" {
							sawA = true
						}
						if m.ToolResult.CallID == "task-b" {
							sawB = true
						}
					}
				}
				return sawA && sawB
			}
			return s
		}(),
		childCompletedNudgeStep("ack board 1"),
		childCompletedNudgeStep("ack board 2"),
	)

	reg := tool.NewRegistry(
		tool.NewTask(),
		tool.NewTeamTask(),
	)
	eng := engine.New(engine.Options{
		SessionID:       leadID,
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		InitialAgent:    "build",
		Agents:          []engine.Agent{{Name: "build"}, {Name: "explore"}},
		Registry:        reg,
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "split board work"}
	events := drainAndReply(t, eng, 20*time.Second)

	var completed int
	for _, ev := range events {
		switch ev := ev.(type) {
		case protocol.ChildCompleted:
			completed++
			if ev.Status != protocol.ChildStatusCompleted {
				t.Fatalf("child %s status=%s summary=%q", ev.SessionID, ev.Status, ev.Summary)
			}
		case protocol.EngineError:
			t.Fatalf("engine error: %s", ev.Message)
		}
	}
	if completed < 2 {
		t.Fatalf("ChildCompleted = %d; events=%v", completed, summarizeEvents(events))
	}

	// Acceptance: shared board, distinct claims, both completed.
	tm := eng.Team()
	if tm == nil {
		t.Fatal("nil team")
	}
	board := tm.Board()
	if len(board) != 2 {
		t.Fatalf("board = %+v (events=%v)", board, summarizeEvents(events))
	}
	byID := map[string]engine.BoardTask{}
	for _, b := range board {
		byID[b.ID] = b
	}
	t1, ok1 := byID["t1"]
	t2, ok2 := byID["t2"]
	if !ok1 || !ok2 {
		t.Fatalf("missing ids: %+v", board)
	}
	if t1.Status != engine.BoardStatusCompleted || t2.Status != engine.BoardStatusCompleted {
		t.Fatalf("want both completed: t1=%+v t2=%+v", t1, t2)
	}
	if t1.Owner == "" || t2.Owner == "" {
		t.Fatalf("want owners set: t1=%q t2=%q", t1.Owner, t2.Owner)
	}
	if t1.Owner == t2.Owner {
		t.Fatalf("want distinct claim owners, both %q", t1.Owner)
	}
	if !strings.Contains(t1.Content, "alpha") || !strings.Contains(t2.Content, "beta") {
		t.Fatalf("content t1=%q t2=%q", t1.Content, t2.Content)
	}
	// Owners must be children (not lead) — claim by workers.
	if t1.Owner == leadID || t2.Owner == leadID {
		t.Fatalf("expected child owners, got t1=%q t2=%q", t1.Owner, t2.Owner)
	}
}

func TestTeamTaskBoardGCOnLeadExit(t *testing.T) {
	const leadID = "lead-board-gc"
	create := controlToolCall("c1", "team_task", map[string]any{
		"action": "create", "content": "ephemeral",
	})
	prov := newScriptedProvider(
		toolCallStep(create),
		func() streamStep {
			s := completedStep("done")
			s.match = matchToolResult("c1")
			return s
		}(),
	)
	eng := engine.New(engine.Options{
		SessionID:       leadID,
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		InitialAgent:    "build",
		Agents:          []engine.Agent{{Name: "build"}},
		Registry:        tool.NewRegistry(tool.NewTeamTask()),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	tm := eng.Team()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		eng.Run(ctx)
		close(done)
	}()

	eng.Ops() <- protocol.UserInput{Text: "make board"}
	_ = drainAndReply(t, eng, 10*time.Second)
	if len(tm.Board()) != 1 {
		t.Fatalf("board before exit = %+v", tm.Board())
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("engine did not exit")
	}
	if got := tm.Board(); len(got) != 0 {
		t.Fatalf("board after lead exit GC = %+v", got)
	}
}
