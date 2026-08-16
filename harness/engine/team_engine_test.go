package engine_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/harness/engine"
	"github.com/jonathanung/strike-cli/harness/permission"
	"github.com/jonathanung/strike-cli/harness/provider"
	"github.com/jonathanung/strike-cli/harness/tool"
	"github.com/jonathanung/strike-cli/pkg/protocol"
)

func TestImplicitTeamLeadAndTwoChildren(t *testing.T) {
	const (
		leadID  = "lead-team-2"
		promptA = "child-a-work-team"
		promptB = "child-b-work-team"
	)
	taskA := taskToolCall("task-a", promptA)
	taskB := taskToolCall("task-b", promptB)
	prov := newScriptedProvider(
		toolCallStep(taskA, taskB),
		func() streamStep {
			s := completedStep("summary-alpha")
			s.match = matchUserText(promptA)
			return s
		}(),
		func() streamStep {
			s := completedStep("summary-beta")
			s.match = matchUserText(promptB)
			return s
		}(),
		func() streamStep {
			s := completedStep("parent after both tasks")
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
		childCompletedNudgeStep("ack concurrent 1"),
		childCompletedNudgeStep("ack concurrent 2"),
	)
	eng := engine.New(engine.Options{
		SessionID:       leadID,
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		InitialAgent:    "build",
		Agents:          []engine.Agent{{Name: "build"}, {Name: "explore"}},
		Registry:        tool.NewRegistry(tool.NewTask()),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	tm := eng.Team()
	if tm == nil {
		t.Fatal("root Team() is nil")
	}
	if tm.LeadID() != leadID {
		t.Fatalf("LeadID = %q, want %q", tm.LeadID(), leadID)
	}
	if !tm.Contains(leadID) {
		t.Fatal("lead must be on roster before spawn")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "spawn two"}
	events := drainAndReply(t, eng, 15*time.Second)

	var started []protocol.ChildStarted
	var completed []protocol.ChildCompleted
	for _, ev := range events {
		switch ev := ev.(type) {
		case protocol.ChildStarted:
			started = append(started, ev)
		case protocol.ChildCompleted:
			completed = append(completed, ev)
		case protocol.EngineError:
			t.Fatalf("engine error: %s", ev.Message)
		}
	}
	if len(started) != 2 {
		t.Fatalf("ChildStarted = %d, want 2; events=%v", len(started), summarizeEvents(events))
	}
	if len(completed) != 2 {
		t.Fatalf("ChildCompleted = %d, want 2", len(completed))
	}

	// Given lead L and children A,B → roster is {L,A,B}
	ids := teamRosterIDs(tm)
	if len(ids) != 3 {
		t.Fatalf("roster = %v, want lead + 2 children", ids)
	}
	if ids[0] != leadID {
		t.Fatalf("roster lead first = %q", ids[0])
	}
	childIDs := []string{started[0].SessionID, started[1].SessionID}
	slices.Sort(childIDs)
	gotChildren := append([]string(nil), ids[1:]...)
	slices.Sort(gotChildren)
	if !slices.Equal(gotChildren, childIDs) {
		t.Fatalf("roster children = %v, want %v", gotChildren, childIDs)
	}
	for _, c := range completed {
		m, ok := tm.Member(c.SessionID)
		if !ok {
			t.Fatalf("missing member %q", c.SessionID)
		}
		if m.State != protocol.TeamMemberCompleted {
			t.Errorf("member %s state = %q, want completed", c.SessionID, m.State)
		}
	}
}

func TestImplicitTeamUnrelatedRootExcluded(t *testing.T) {
	runOne := func(t *testing.T, id, prompt string) (*engine.Team, string) {
		t.Helper()
		call := taskToolCall("t1", prompt)
		prov := newScriptedProvider(
			toolCallStep(call),
			func() streamStep {
				s := completedStep("child done")
				s.match = matchUserText(prompt)
				return s
			}(),
			func() streamStep {
				s := completedStep("parent done")
				s.match = matchToolResult("t1")
				return s
			}(),
			childCompletedNudgeStep("ack"),
		)
		eng := engine.New(engine.Options{
			SessionID:       id,
			Select:          func(string) (provider.Provider, string, error) { return prov, "m", nil },
			InitialProvider: "scripted",
			Registry:        tool.NewRegistry(tool.NewTask()),
			WorkDir:         t.TempDir(),
			Rules:           []permission.Ruleset{permission.Defaults()},
		})
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		go eng.Run(ctx)
		eng.Ops() <- protocol.UserInput{Text: "go"}
		events := drainAndReply(t, eng, 10*time.Second)
		var childID string
		for _, ev := range events {
			if s, ok := ev.(protocol.ChildStarted); ok {
				childID = s.SessionID
			}
		}
		if childID == "" {
			t.Fatalf("%s: no ChildStarted", id)
		}
		return eng.Team(), childID
	}

	teamL, childL := runOne(t, "lead-L", "work-L")
	teamR, childR := runOne(t, "lead-R", "work-R")

	if teamL.Contains(childR) {
		t.Fatal("R's child must not be on L's team")
	}
	if teamR.Contains(childL) {
		t.Fatal("L's child must not be on R's team")
	}
	if teamL.Contains("lead-R") || teamR.Contains("lead-L") {
		t.Fatal("unrelated leads must not share membership")
	}
}

func TestImplicitTeamNestedDescendant(t *testing.T) {
	// Nested policy: grandchildren enroll on the lead roster (flat team).
	const (
		leadID      = "lead-nest-team"
		childPrompt = "child-level-1-team"
		gcPrompt    = "grandchild-level-2-team"
	)
	taskL1 := taskToolCall("task-l1", childPrompt)
	taskL2 := taskToolCall("task-l2", gcPrompt)
	prov := newScriptedProvider(
		toolCallStep(taskL1),
		func() streamStep {
			s := toolCallStep(taskL2)
			s.match = matchUserText(childPrompt)
			return s
		}(),
		func() streamStep {
			s := completedStep("grandchild finished work")
			s.match = matchUserText(gcPrompt)
			return s
		}(),
		func() streamStep {
			s := completedStep("child finished after spawn")
			s.match = matchToolResult("task-l2")
			return s
		}(),
		func() streamStep {
			s := completedStep("parent finished after spawn")
			s.match = matchToolResult("task-l1")
			return s
		}(),
		childCompletedNudgeStep("ack nested 1"),
		childCompletedNudgeStep("ack nested 2"),
		childCompletedNudgeStep("ack nested 3"),
	)
	eng := engine.New(engine.Options{
		SessionID:       leadID,
		MaxChildDepth:   2,
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tool.NewTask()),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	tm := eng.Team()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "nest two deep"}
	events := drainNested(t, eng, 2, 15*time.Second)

	var started []protocol.ChildStarted
	for _, ev := range events {
		if s, ok := ev.(protocol.ChildStarted); ok {
			started = append(started, s)
		}
	}
	if len(started) != 2 {
		t.Fatalf("ChildStarted = %d, want 2; events=%v", len(started), summarizeEvents(events))
	}

	ids := teamRosterIDs(tm)
	if len(ids) != 3 {
		t.Fatalf("roster = %v (len %d), want lead + 2 descendants", ids, len(ids))
	}
	if ids[0] != leadID {
		t.Fatalf("lead first = %q", ids[0])
	}
	var d1, d2 string
	for _, s := range started {
		switch s.Depth {
		case 1:
			d1 = s.SessionID
		case 2:
			d2 = s.SessionID
		}
	}
	if d1 == "" || d2 == "" {
		t.Fatalf("missing depths in started: %+v", started)
	}
	if !tm.Contains(d1) || !tm.Contains(d2) {
		t.Fatalf("roster missing nested members: %v d1=%s d2=%s", ids, d1, d2)
	}
	g, ok := tm.Member(d2)
	if !ok || g.ParentSessionID != d1 || g.Depth != 2 {
		t.Fatalf("grandchild member = %+v ok=%v", g, ok)
	}
}

func TestImplicitTeamDissolvesOnLeadExit(t *testing.T) {
	const leadID = "lead-dissolve"
	prov := newScriptedProvider(completedStep("hi"))
	eng := engine.New(engine.Options{
		SessionID:       leadID,
		Select:          func(string) (provider.Provider, string, error) { return prov, "m", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	tm := eng.Team()
	if tm.Len() != 1 {
		t.Fatalf("Len before run = %d", tm.Len())
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		eng.Run(ctx)
	}()
	eng.Ops() <- protocol.UserInput{Text: "hi"}
	_ = drainAndReply(t, eng, 5*time.Second)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not exit")
	}
	if tm.Len() != 0 {
		t.Fatalf("team should dissolve on lead exit, Len=%d", tm.Len())
	}
}

func teamRosterIDs(tm *engine.Team) []string {
	if tm == nil {
		return nil
	}
	roster := tm.Roster()
	ids := make([]string, len(roster))
	for i, m := range roster {
		ids[i] = m.SessionID
	}
	return ids
}
