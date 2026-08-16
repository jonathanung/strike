package engine_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/harness/engine"
	"github.com/jonathanung/strike-cli/harness/permission"
	"github.com/jonathanung/strike-cli/harness/provider"
	"github.com/jonathanung/strike-cli/harness/tool"
	"github.com/jonathanung/strike-cli/pkg/protocol"
)

func agentRosterRegistry() *tool.Registry {
	return tool.NewRegistry(
		tool.NewTask(),
		tool.NewTaskStatus(),
		tool.NewAgentRoster(),
	)
}

func TestAgentRosterSoloLead(t *testing.T) {
	const leadID = "lead-solo-roster"
	call := controlToolCall("ar-1", "agent_roster", map[string]any{})
	prov := newScriptedProvider(
		toolCallStep(call),
		func() streamStep {
			s := completedStep("solo done")
			s.match = matchToolResult("ar-1")
			return s
		}(),
	)
	eng := engine.New(engine.Options{
		SessionID:       leadID,
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		InitialAgent:    "build",
		Agents:          []engine.Agent{{Name: "build"}},
		Registry:        agentRosterRegistry(),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "list roster"}
	events := drainAndReply(t, eng, 10*time.Second)

	var rosterOut string
	for _, ev := range events {
		switch ev := ev.(type) {
		case protocol.ToolCallEnd:
			if ev.CallID == "ar-1" {
				rosterOut = ev.Output
			}
		case protocol.EngineError:
			t.Fatalf("engine error: %s", ev.Message)
		}
	}
	if rosterOut == "" {
		t.Fatalf("missing agent_roster output; events=%v", summarizeEvents(events))
	}
	var parsed tool.AgentRosterResult
	if err := json.Unmarshal([]byte(rosterOut), &parsed); err != nil {
		t.Fatalf("parse %q: %v", rosterOut, err)
	}
	if parsed.LeadID != leadID {
		t.Fatalf("lead_id = %q, want %q", parsed.LeadID, leadID)
	}
	if len(parsed.Members) != 1 {
		t.Fatalf("members = %d, want 1 (solo lead): %+v", len(parsed.Members), parsed.Members)
	}
	m := parsed.Members[0]
	if m.SessionID != leadID || m.Role != "lead" || !m.IsSelf {
		t.Fatalf("member = %+v", m)
	}
	if m.State != "working" {
		t.Fatalf("solo lead state = %q, want working", m.State)
	}
	if m.Agent != "build" {
		t.Fatalf("agent = %q, want build", m.Agent)
	}
}

func TestAgentRosterMultiChildAndTeamEvents(t *testing.T) {
	const (
		leadID  = "lead-multi-roster"
		promptA = "child-a-roster-work"
		promptB = "child-b-roster-work"
	)
	taskA := taskToolCall("task-a", promptA)
	taskB := taskToolCall("task-b", promptB)
	leadRoster := controlToolCall("ar-lead", "agent_roster", map[string]any{})

	prov := newScriptedProvider(
		toolCallStep(taskA, taskB),
		func() streamStep {
			s := completedStep("summary-a")
			s.match = matchUserText(promptA)
			return s
		}(),
		func() streamStep {
			s := completedStep("summary-b")
			s.match = matchUserText(promptB)
			return s
		}(),
		func() streamStep {
			s := toolCallStep(leadRoster)
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
		func() streamStep {
			s := completedStep("parent after roster")
			s.match = matchToolResult("ar-lead")
			return s
		}(),
		childCompletedNudgeStep("ack 1"),
		childCompletedNudgeStep("ack 2"),
	)

	eng := engine.New(engine.Options{
		SessionID:       leadID,
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		InitialAgent:    "build",
		Agents:          []engine.Agent{{Name: "build"}, {Name: "explore"}},
		Registry:        agentRosterRegistry(),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "spawn and list"}
	events := drainAndReply(t, eng, 20*time.Second)

	var (
		started       []protocol.ChildStarted
		completed     []protocol.ChildCompleted
		teamRosters   []protocol.TeamRoster
		leadRosterOut string
	)
	for _, ev := range events {
		switch ev := ev.(type) {
		case protocol.ChildStarted:
			started = append(started, ev)
		case protocol.ChildCompleted:
			completed = append(completed, ev)
		case protocol.TeamRoster:
			teamRosters = append(teamRosters, ev)
		case protocol.ToolCallEnd:
			if ev.CallID == "ar-lead" {
				leadRosterOut = ev.Output
			}
		case protocol.EngineError:
			t.Fatalf("engine error: %s", ev.Message)
		}
	}
	if len(started) != 2 {
		t.Fatalf("ChildStarted = %d, want 2; events=%v", len(started), summarizeEvents(events))
	}
	if leadRosterOut == "" {
		t.Fatal("missing lead agent_roster output")
	}
	if len(teamRosters) == 0 {
		t.Fatal("expected at least one team.roster event on spawn/complete")
	}

	var leadParsed tool.AgentRosterResult
	if err := json.Unmarshal([]byte(leadRosterOut), &leadParsed); err != nil {
		t.Fatal(err)
	}
	if leadParsed.LeadID != leadID || len(leadParsed.Members) != 3 {
		t.Fatalf("lead roster = %+v", leadParsed)
	}
	if leadParsed.Members[0].SessionID != leadID || leadParsed.Members[0].Role != "lead" || !leadParsed.Members[0].IsSelf {
		t.Fatalf("lead first = %+v", leadParsed.Members[0])
	}
	childIDs := map[string]bool{started[0].SessionID: true, started[1].SessionID: true}
	for _, m := range leadParsed.Members[1:] {
		if !childIDs[m.SessionID] {
			t.Fatalf("unexpected member %q", m.SessionID)
		}
		if m.Role != "member" || m.IsSelf {
			t.Fatalf("child row = %+v", m)
		}
		switch m.State {
		case "starting", "working", "needs_attention", "completed", "failed", "canceled":
		default:
			t.Fatalf("bad state %q on %s", m.State, m.SessionID)
		}
	}

	// Live event snapshots carry lead id and non-empty members after enroll.
	var sawPopulated bool
	for _, tr := range teamRosters {
		if tr.LeadID != leadID {
			t.Fatalf("TeamRoster lead = %q", tr.LeadID)
		}
		if len(tr.Members) >= 3 {
			sawPopulated = true
		}
	}
	if !sawPopulated {
		t.Fatalf("no TeamRoster with lead+2 children; got %d snapshots", len(teamRosters))
	}
	if len(completed) != 2 {
		t.Fatalf("ChildCompleted = %d, want 2", len(completed))
	}
}

func TestAgentRosterPermissionDefaultAllow(t *testing.T) {
	if got := permission.Evaluate("agent_roster", "*", permission.Defaults()); got != permission.Allow {
		t.Fatalf("Defaults agent_roster = %q, want allow", got)
	}
}
