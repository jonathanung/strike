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

func taskToolCallNamed(id, prompt, name, agent string) provider.ToolCall {
	args := map[string]any{"prompt": prompt}
	if name != "" {
		args["name"] = name
	}
	if agent != "" {
		args["agent"] = agent
	}
	b, _ := json.Marshal(args)
	return provider.ToolCall{ID: id, Name: "task", Args: b}
}

func TestTaskSpawnNameOnRosterAndEvents(t *testing.T) {
	const (
		leadID = "lead-named-spawn"
		prompt = "named-child-work"
	)
	taskCall := taskToolCallNamed("task-named", prompt, "explorer", "explore")
	rosterCall := controlToolCall("ar1", "agent_roster", map[string]any{})

	prov := newScriptedProvider(
		toolCallStep(taskCall),
		func() streamStep {
			s := completedStep("child-done")
			s.match = matchUserText(prompt)
			return s
		}(),
		func() streamStep {
			s := toolCallStep(rosterCall)
			s.match = matchToolResult("task-named")
			return s
		}(),
		func() streamStep {
			s := completedStep("parent after roster")
			s.match = matchToolResult("ar1")
			return s
		}(),
		childCompletedNudgeStep("ack named"),
	)

	eng := engine.New(engine.Options{
		SessionID:       leadID,
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		InitialAgent:    "build",
		Agents:          []engine.Agent{{Name: "build"}, {Name: "explore"}},
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

	eng.Ops() <- protocol.UserInput{Text: "spawn named"}
	events := drainAndReply(t, eng, 20*time.Second)

	var (
		started   protocol.ChildStarted
		completed protocol.ChildCompleted
		rosterOut string
		sawStart  bool
		sawDone   bool
	)
	for _, ev := range events {
		switch ev := ev.(type) {
		case protocol.ChildStarted:
			started = ev
			sawStart = true
		case protocol.ChildCompleted:
			completed = ev
			sawDone = true
		case protocol.ToolCallEnd:
			if ev.CallID == "ar1" {
				rosterOut = ev.Output
			}
		case protocol.EngineError:
			t.Fatalf("engine error: %s", ev.Message)
		}
	}
	if !sawStart {
		t.Fatalf("missing ChildStarted; events=%v", summarizeEvents(events))
	}
	if started.Name != "explorer" {
		t.Fatalf("ChildStarted.Name = %q, want explorer", started.Name)
	}
	if !sawDone {
		t.Fatal("missing ChildCompleted")
	}
	if completed.Name != "explorer" {
		t.Fatalf("ChildCompleted.Name = %q, want explorer", completed.Name)
	}

	tm := eng.Team()
	m, ok := tm.Member(started.SessionID)
	if !ok || m.Name != "explorer" {
		t.Fatalf("team member = %+v ok=%v", m, ok)
	}
	id, ok := tm.Resolve("explorer")
	if !ok || id != started.SessionID {
		t.Fatalf("Resolve(explorer) = %q ok=%v, want %s", id, ok, started.SessionID)
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
			if mem.Name != "explorer" {
				t.Fatalf("roster name = %q, want explorer", mem.Name)
			}
		}
	}
	if !found {
		t.Fatalf("child missing from roster: %+v", parsed)
	}
}

func TestTaskSpawnDuplicateNameRejected(t *testing.T) {
	const (
		leadID  = "lead-dup-name"
		promptA = "first-named"
		promptB = "second-named"
	)
	taskA := taskToolCallNamed("task-a", promptA, "explorer", "")
	taskB := taskToolCallNamed("task-b", promptB, "explorer", "")

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
			s := completedStep("parent after dup")
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

	eng.Ops() <- protocol.UserInput{Text: "dup name"}
	events := drainAndReply(t, eng, 20*time.Second)

	var started int
	var taskBErr string
	for _, ev := range events {
		switch ev := ev.(type) {
		case protocol.ChildStarted:
			started++
		case protocol.ToolCallEnd:
			if ev.CallID == "task-b" && ev.IsError {
				taskBErr = ev.Output
			}
		case protocol.EngineError:
			t.Fatalf("engine error: %s", ev.Message)
		}
	}
	if started != 1 {
		t.Fatalf("ChildStarted = %d, want 1", started)
	}
	if !strings.Contains(taskBErr, "already used") {
		t.Fatalf("task-b error = %q, want already used", taskBErr)
	}
}

func TestTaskMessageByNameAlias(t *testing.T) {
	const prompt = "child-hold-named"
	release := make(chan struct{})
	ct := &channelTool{
		executed: make(chan string, 2),
		blocks: map[string]<-chan struct{}{
			"hold-n": release,
		},
	}
	taskCall := taskToolCallNamed("task-n", prompt, "worker", "")

	var (
		turn2Calls []provider.ToolCall
		turn2Ready = make(chan struct{})
	)

	prov := newScriptedProvider(
		toolCallStep(taskCall),
		func() streamStep {
			s := completedStep("spawned")
			s.match = matchToolResult("task-n")
			return s
		}(),
		func() streamStep {
			s := toolCallStep(toolCall("hold-n", "channel"))
			s.match = matchUserText(prompt)
			return s
		}(),
		func() streamStep {
			return streamStep{
				match: matchLatestUserText("message by name"),
				stream: func(ctx context.Context) <-chan provider.StreamEvent {
					select {
					case <-turn2Ready:
					case <-ctx.Done():
						ch := make(chan provider.StreamEvent)
						close(ch)
						return ch
					}
					events := make([]provider.StreamEvent, 0, len(turn2Calls)+1)
					for i := range turn2Calls {
						call := turn2Calls[i]
						events = append(events, provider.StreamEvent{Type: provider.EventToolCall, ToolCall: &call})
					}
					events = append(events, provider.StreamEvent{Type: provider.EventDone, StopReason: "tool_use"})
					ch := make(chan provider.StreamEvent, len(events))
					for _, ev := range events {
						ch <- ev
					}
					close(ch)
					return ch
				},
			}
		}(),
		func() streamStep {
			s := completedStep("after msg")
			s.match = matchToolResult("msg-n")
			return s
		}(),
		func() streamStep {
			s := completedStep("child after hold")
			s.match = matchToolResult("hold-n")
			return s
		}(),
		childCompletedNudgeStep("ack"),
	)

	eng := engine.New(engine.Options{
		SessionID:       "parent-msg-name",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        taskControlRegistry(ct),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "spawn"}
	events := drainUntil(t, eng, 10*time.Second, func(evs []protocol.Event) bool {
		return countEvents[protocol.ChildStarted](evs) == 1 &&
			countEvents[protocol.TurnCompleted](evs) >= 1
	})
	var childID string
	for _, ev := range events {
		if cs, ok := ev.(protocol.ChildStarted); ok {
			childID = cs.SessionID
			if cs.Name != "worker" {
				t.Fatalf("ChildStarted.Name = %q", cs.Name)
			}
		}
	}
	if childID == "" {
		t.Fatal("no child id")
	}

	deadline := time.After(5 * time.Second)
	select {
	case <-ct.executed:
	case <-deadline:
		t.Fatal("child hold tool never started")
	}

	turn2Calls = []provider.ToolCall{
		controlToolCall("msg-n", "task_message", map[string]any{
			"session_id": "worker",
			"text":       "steer by name",
		}),
		controlToolCall("st-n", "task_status", map[string]any{
			"session_id": "worker",
		}),
	}
	close(turn2Ready)

	eng.Ops() <- protocol.UserInput{Text: "message by name"}
	events = drainUntil(t, eng, 15*time.Second, func(evs []protocol.Event) bool {
		_, ok1 := toolEndOutput(evs, "msg-n")
		_, ok2 := toolEndOutput(evs, "st-n")
		return ok1 && ok2
	})

	msgOut, _ := toolEndOutput(events, "msg-n")
	if !strings.Contains(msgOut, `"status":"queued"`) && !strings.Contains(msgOut, `"status":"accepted"`) {
		t.Fatalf("message by name = %s", msgOut)
	}
	if !strings.Contains(msgOut, childID) {
		t.Fatalf("message result missing resolved session id %s: %s", childID, msgOut)
	}
	stOut, _ := toolEndOutput(events, "st-n")
	if !strings.Contains(stOut, childID) {
		t.Fatalf("status by name missing session id: %s", stOut)
	}

	close(release)
	cancel()
}

func TestEnqueueTeamMessageResolvesName(t *testing.T) {
	const (
		leadID = "lead-mb-name"
		toID   = "agent-b-name"
		body   = "hello by name"
	)
	team := engine.NewTeam(leadID, "build")
	if !team.Enroll(engine.TeamMember{
		SessionID: toID, ParentSessionID: leadID, Name: "bob", Persona: "general", Depth: 1,
	}) {
		t.Fatal("enroll bob")
	}

	lead := engine.New(engine.Options{
		SessionID: leadID,
		Team:      team,
		Agents:    []engine.Agent{{Name: "build"}},
		Select: func(string) (provider.Provider, string, error) {
			return newScriptedProvider(completedStep("lead-idle")), "m", nil
		},
		InitialProvider: "scripted",
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	child := engine.New(engine.Options{
		SessionID:       toID,
		ParentSessionID: leadID,
		Depth:           1,
		Team:            team,
		Agents:          []engine.Agent{{Name: "general"}},
		Select: func(string) (provider.Provider, string, error) {
			return newScriptedProvider(completedStep("child-idle")), "m", nil
		},
		InitialProvider: "scripted",
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})

	// Attach without Run to avoid racing Mailbox() with AttachMailbox.
	team.AttachMailbox(lead)
	team.AttachMailbox(child)
	if child.Mailbox() == nil {
		t.Fatal("child mailbox not attached")
	}

	st := lead.EnqueueTeamMessage(leadID, "bob", body)
	if st.Status != "accepted" {
		t.Fatalf("EnqueueTeamMessage by name: %+v", st)
	}
	if st.To != toID {
		t.Fatalf("resolved To = %q, want %s", st.To, toID)
	}
	if child.Mailbox().Len() != 1 {
		t.Fatalf("mailbox len = %d, want 1", child.Mailbox().Len())
	}
}
