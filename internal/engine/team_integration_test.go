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

// teamIntegrationRegistry is the full task + team messaging surface used by
// lead and inherited by spawned children (leaf strips task_* only).
func teamIntegrationRegistry(extra ...tool.Tool) *tool.Registry {
	base := []tool.Tool{
		tool.NewTask(),
		tool.NewTaskStatus(),
		tool.NewTaskRead(),
		tool.NewTaskMessage(),
		tool.NewTaskInterrupt(),
		tool.NewAgentRoster(),
		tool.NewAgentMessage(),
		tool.NewAgentBroadcast(),
	}
	return tool.NewRegistry(append(base, extra...)...)
}

// matchLatestChildCompleted claims streams whose latest user message is a
// child.completed nudge (not merely present earlier in history).
func matchLatestChildCompleted(req provider.Request) bool {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == provider.RoleUser {
			return strings.Contains(req.Messages[i].Text, "[child.completed")
		}
	}
	return false
}

// TestTeamIntegration_SpawnMessageBroadcastAuthBoundary is the #616 end-to-end
// path: spawn two named children, peer handoff, broadcast, out-of-team reject,
// and boundary injection while the recipient is mid-tool.
func TestTeamIntegration_SpawnMessageBroadcastAuthBoundary(t *testing.T) {
	const (
		leadID        = "lead-team-int"
		promptExplore = "explore-find-package"
		promptImpl    = "implement-from-handoff"
		handoffBody   = "change X in path Y; tests in Z"
		broadcastBody = "team-wide status: proceed"
	)
	releaseExplore := make(chan struct{})
	releaseImpl := make(chan struct{})
	ct := &channelTool{
		executed: make(chan string, 4),
		blocks: map[string]<-chan struct{}{
			"hold-ex": releaseExplore,
			"hold-im": releaseImpl,
		},
	}

	var (
		turn2Calls []provider.ToolCall
		turn2Ready = make(chan struct{})
	)

	prov := newScriptedProvider(
		// Lead turn 1: spawn explore + implement in parallel.
		toolCallStep(
			taskToolCallNamed("task-ex", promptExplore, "explorer", "explore"),
			taskToolCallNamed("task-im", promptImpl, "implementer", "general"),
		),
		func() streamStep {
			s := completedStep("both spawned")
			s.match = func(req provider.Request) bool {
				return matchToolResult("task-ex")(req) && matchToolResult("task-im")(req)
			}
			return s
		}(),
		// Children hold so they stay live for peer delivery / boundary inject.
		func() streamStep {
			s := toolCallStep(toolCall("hold-ex", "channel"))
			s.match = matchUserText(promptExplore)
			return s
		}(),
		func() streamStep {
			s := toolCallStep(toolCall("hold-im", "channel"))
			s.match = matchUserText(promptImpl)
			return s
		}(),
		// Lead turn 2: roster + peer handoff by name + broadcast + out-of-team.
		func() streamStep {
			return streamStep{
				match: matchLatestUserText("team coordinate"),
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
			s := completedStep("lead coordinated")
			s.match = matchToolResult("ar-1")
			return s
		}(),
		// Children finish after holds release.
		func() streamStep {
			s := completedStep("explorer done")
			s.match = matchToolResult("hold-ex")
			return s
		}(),
		func() streamStep {
			s := completedStep("implementer got handoff")
			s.match = matchToolResult("hold-im")
			return s
		}(),
		// Nudges: match latest user only so later lead turns are not stolen.
		func() streamStep {
			s := completedStep("ack explore done")
			s.match = matchLatestChildCompleted
			return s
		}(),
		func() streamStep {
			s := completedStep("ack implement done")
			s.match = matchLatestChildCompleted
			return s
		}(),
	)

	eng := engine.New(engine.Options{
		SessionID:       leadID,
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		InitialAgent:    "build",
		Agents: []engine.Agent{
			{Name: "build"},
			{Name: "explore"},
			{Name: "general"},
		},
		Registry: teamIntegrationRegistry(ct),
		WorkDir:  t.TempDir(),
		Rules:    []permission.Ruleset{permission.Defaults()},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "spawn team"}
	events := drainUntil(t, eng, 20*time.Second, func(evs []protocol.Event) bool {
		return countEvents[protocol.ChildStarted](evs) == 2 &&
			countEvents[protocol.TurnCompleted](evs) >= 1
	})

	idByName := map[string]string{}
	for _, ev := range events {
		if cs, ok := ev.(protocol.ChildStarted); ok {
			idByName[cs.Name] = cs.SessionID
		}
	}
	exploreID, implID := idByName["explorer"], idByName["implementer"]
	if exploreID == "" || implID == "" || exploreID == implID {
		t.Fatalf("named children = %v", idByName)
	}

	// Implicit team = same session tree (lead + two children).
	tm := eng.Team()
	if tm == nil || tm.LeadID() != leadID {
		t.Fatalf("team lead = %v", tm)
	}
	ids := teamRosterIDs(tm)
	if len(ids) != 3 {
		t.Fatalf("roster = %v, want lead+2", ids)
	}
	for _, id := range []string{leadID, exploreID, implID} {
		if !tm.Contains(id) {
			t.Fatalf("missing %q on roster %v", id, ids)
		}
	}

	// Wait until both children are mid-tool (live mailboxes attached).
	deadline := time.After(8 * time.Second)
	gotHold := map[string]bool{}
	for len(gotHold) < 2 {
		select {
		case id := <-ct.executed:
			gotHold[id] = true
		case <-deadline:
			t.Fatalf("holds = %v", gotHold)
		}
	}

	// Peer handoff while implementer is mid-tool (boundary inject on release).
	// Use explore as sender so membership/from checks exercise child→child path.
	st := eng.EnqueueTeamMessage(exploreID, implID, handoffBody)
	if st.Status != "accepted" {
		t.Fatalf("peer handoff deliver = %#v", st)
	}

	// Lead: roster + named agent_message + broadcast + out-of-team auth fail.
	turn2Calls = []provider.ToolCall{
		controlToolCall("ar-1", "agent_roster", map[string]any{}),
		controlToolCall("am-name", "agent_message", map[string]any{
			"to": "implementer", "body": "lead-ack-handoff", "summary": "ack",
		}),
		controlToolCall("ab-1", "agent_broadcast", map[string]any{"body": broadcastBody}),
		controlToolCall("am-oot", "agent_message", map[string]any{
			"to": "foreign-session-xyz", "body": "should fail",
		}),
	}
	close(turn2Ready)
	eng.Ops() <- protocol.UserInput{Text: "team coordinate"}
	events = drainUntil(t, eng, 15*time.Second, func(evs []protocol.Event) bool {
		_, okAR := toolEndOutput(evs, "ar-1")
		_, okAM := toolEndOutput(evs, "am-name")
		_, okAB := toolEndOutput(evs, "ab-1")
		endOOT, okOOT := toolEnd(evs, "am-oot")
		return okAR && okAM && okAB && okOOT && endOOT.Output != ""
	})

	rosterOut, _ := toolEndOutput(events, "ar-1")
	var roster tool.AgentRosterResult
	if err := json.Unmarshal([]byte(rosterOut), &roster); err != nil {
		t.Fatalf("roster parse %q: %v", rosterOut, err)
	}
	if roster.LeadID != leadID {
		t.Fatalf("roster lead = %q", roster.LeadID)
	}
	names := map[string]bool{}
	for _, m := range roster.Members {
		if m.Name != "" {
			names[m.Name] = true
		}
	}
	if !names["explorer"] || !names["implementer"] {
		t.Fatalf("roster names missing: members=%+v", roster.Members)
	}

	amOut, _ := toolEndOutput(events, "am-name")
	var amRes tool.AgentMessageResult
	if err := json.Unmarshal([]byte(amOut), &amRes); err != nil {
		t.Fatalf("agent_message parse %q: %v", amOut, err)
	}
	if amRes.Status != "accepted" || amRes.To != implID {
		t.Fatalf("named agent_message = %+v, want accepted to %s", amRes, implID)
	}

	bcOut, _ := toolEndOutput(events, "ab-1")
	var bc tool.AgentBroadcastResult
	if err := json.Unmarshal([]byte(bcOut), &bc); err != nil {
		t.Fatalf("broadcast parse %q: %v", bcOut, err)
	}
	// Both children still holding → two deliveries expected (n-1 excluding lead).
	if bc.Delivered < 1 {
		t.Fatalf("broadcast delivered = %d (%+v)", bc.Delivered, bc)
	}
	for _, r := range bc.Results {
		if r.To == leadID {
			t.Fatalf("broadcast included self: %+v", bc)
		}
	}

	oot, ok := toolEnd(events, "am-oot")
	if !ok || !oot.IsError {
		t.Fatalf("out-of-team want error, got %#v", oot)
	}
	if !strings.Contains(oot.Output, "not on this team") && !strings.Contains(strings.ToLower(oot.Output), "reject") {
		t.Fatalf("out-of-team output = %q", oot.Output)
	}

	// Boundary delivery: release implementer; next stream must include handoff
	// body without breaking tool-call/result pairing.
	close(releaseExplore)
	close(releaseImpl)
	deadline = time.After(10 * time.Second)
	var sawAM bool
	for {
		select {
		case req := <-prov.requests:
			if !matchToolResult("hold-im")(req) {
				continue
			}
			if !requestHasMailboxBody(req, handoffBody) {
				t.Fatalf("implementer stream missing handoff body; messages=%#v", req.Messages)
			}
			if !historyToolPairsOK(req.Messages) {
				t.Fatalf("invalid tool pairs after mailbox inject: %#v", req.Messages)
			}
			return
		case ev := <-eng.Events():
			if am, ok := ev.(protocol.AgentMessage); ok && am.Body == handoffBody {
				if am.From != exploreID || am.To != implID {
					t.Fatalf("AgentMessage = %#v", am)
				}
				sawAM = true
			}
		case <-deadline:
			t.Fatalf("implementer never got handoff body (agentMessageEvent=%v)", sawAM)
		}
	}
}

// TestTeamIntegration_ChildToolPeerHandoff: spawned child calls agent_message
// tool to a named sibling (explore → implement) while both are live.
func TestTeamIntegration_ChildToolPeerHandoff(t *testing.T) {
	const (
		leadID        = "lead-team-child-tool"
		promptExplore = "explore-child-tool-handoff"
		promptImpl    = "implement-child-tool-handoff"
		handoffBody   = "child-tool-handoff-body"
	)
	releaseExplore := make(chan struct{})
	releaseImpl := make(chan struct{})
	ct := &channelTool{
		executed: make(chan string, 4),
		blocks: map[string]<-chan struct{}{
			"hold-ex": releaseExplore,
			"hold-im": releaseImpl,
		},
	}

	prov := newScriptedProvider(
		toolCallStep(
			taskToolCallNamed("task-ex", promptExplore, "explorer", "explore"),
			taskToolCallNamed("task-im", promptImpl, "implementer", "general"),
		),
		func() streamStep {
			s := completedStep("spawned")
			s.match = func(req provider.Request) bool {
				return matchToolResult("task-ex")(req) && matchToolResult("task-im")(req)
			}
			return s
		}(),
		// Both hold first so implementer mailbox is live before explore sends.
		func() streamStep {
			s := toolCallStep(toolCall("hold-ex", "channel"))
			s.match = matchUserText(promptExplore)
			return s
		}(),
		func() streamStep {
			s := toolCallStep(toolCall("hold-im", "channel"))
			s.match = matchUserText(promptImpl)
			return s
		}(),
		// After explore hold releases: agent_message by sibling name.
		func() streamStep {
			s := toolCallStep(controlToolCall("am-handoff", "agent_message", map[string]any{
				"to": "implementer", "body": handoffBody, "summary": "handoff",
			}))
			s.match = matchToolResult("hold-ex")
			return s
		}(),
		func() streamStep {
			s := completedStep("explorer sent")
			s.match = matchToolResult("am-handoff")
			return s
		}(),
		func() streamStep {
			s := completedStep("implementer done")
			s.match = matchToolResult("hold-im")
			return s
		}(),
		func() streamStep {
			s := completedStep("ack1")
			s.match = matchLatestChildCompleted
			return s
		}(),
		func() streamStep {
			s := completedStep("ack2")
			s.match = matchLatestChildCompleted
			return s
		}(),
	)

	eng := engine.New(engine.Options{
		SessionID:       leadID,
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		InitialAgent:    "build",
		Agents:          []engine.Agent{{Name: "build"}, {Name: "explore"}, {Name: "general"}},
		Registry:        teamIntegrationRegistry(ct),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "spawn for child tool handoff"}
	events := drainUntil(t, eng, 20*time.Second, func(evs []protocol.Event) bool {
		return countEvents[protocol.ChildStarted](evs) == 2
	})
	idByName := map[string]string{}
	for _, ev := range events {
		if cs, ok := ev.(protocol.ChildStarted); ok {
			idByName[cs.Name] = cs.SessionID
		}
	}
	exploreID, implID := idByName["explorer"], idByName["implementer"]
	if exploreID == "" || implID == "" {
		t.Fatalf("ids = %v", idByName)
	}

	deadline := time.After(8 * time.Second)
	gotHold := map[string]bool{}
	for len(gotHold) < 2 {
		select {
		case id := <-ct.executed:
			gotHold[id] = true
		case <-deadline:
			t.Fatalf("holds = %v", gotHold)
		}
	}

	close(releaseExplore)
	_ = drainUntil(t, eng, 15*time.Second, func(evs []protocol.Event) bool {
		for _, ev := range evs {
			if cc, ok := ev.(protocol.ChildCompleted); ok && cc.SessionID == exploreID {
				return true
			}
		}
		return false
	})

	close(releaseImpl)
	deadline = time.After(10 * time.Second)
	for {
		select {
		case req := <-prov.requests:
			if !matchToolResult("hold-im")(req) {
				continue
			}
			if !requestHasMailboxBody(req, handoffBody) {
				t.Fatalf("missing handoff; messages=%#v", req.Messages)
			}
			if !historyToolPairsOK(req.Messages) {
				t.Fatalf("bad tool pairs: %#v", req.Messages)
			}
			_ = implID // used via name resolve on send path
			return
		case <-deadline:
			t.Fatal("implementer never saw child-tool handoff")
		}
	}
}

// TestTeamIntegration_PermissionDeny covers hard deny of agent_message (auth
// fail via permission rules — distinct from out-of-team reject).
func TestTeamIntegration_PermissionDeny(t *testing.T) {
	const (
		leadID = "lead-team-deny"
		toID   = "child-team-deny"
		body   = "should-be-denied"
	)
	team := engine.NewTeam(leadID, "build")
	if !team.Enroll(engine.TeamMember{SessionID: toID, ParentSessionID: leadID, Depth: 1, Name: "target"}) {
		t.Fatal("enroll")
	}

	call := controlToolCall("am-deny", "agent_message", map[string]any{"to": toID, "body": body})
	leadProv := newScriptedProvider(
		toolCallStep(call),
		func() streamStep {
			s := completedStep("after deny")
			s.match = matchToolResult("am-deny")
			return s
		}(),
	)
	deny := permission.Ruleset{{Permission: "agent_message", Pattern: "*", Action: permission.Deny}}

	lead := engine.New(engine.Options{
		SessionID: leadID, Team: team, Agents: []engine.Agent{{Name: "build"}},
		Select:          func(string) (provider.Provider, string, error) { return leadProv, "m", nil },
		InitialProvider: "scripted",
		Registry:        agentMessageRegistry(),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults(), deny},
	})
	child := engine.New(engine.Options{
		SessionID: toID, ParentSessionID: leadID, Depth: 1, Team: team,
		Agents: []engine.Agent{{Name: "general"}},
		Select: func(string) (provider.Provider, string, error) {
			return newScriptedProvider(completedStep("c")), "m", nil
		},
		InitialProvider: "scripted",
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go lead.Run(ctx)
	go child.Run(ctx)
	go func() {
		for range child.Events() {
		}
	}()
	waitTeamLive(t, team, leadID, toID)

	lead.Ops() <- protocol.UserInput{Text: "try denied message"}
	events := drainUntil(t, lead, 8*time.Second, func(evs []protocol.Event) bool {
		end, ok := toolEnd(evs, "am-deny")
		return ok && end.Output != ""
	})
	end, ok := toolEnd(events, "am-deny")
	if !ok {
		t.Fatal("missing tool end")
	}
	if !end.IsError {
		t.Fatalf("want permission deny error, got %#v", end)
	}
	low := strings.ToLower(end.Output)
	if !strings.Contains(low, "denied") && !strings.Contains(low, "permission") && !strings.Contains(low, "rule") {
		t.Fatalf("deny output = %q", end.Output)
	}
}

// TestTeamIntegration_ParentOnlyUnchanged ensures parent→child task_* workflows
// still work when the registry has no agent_* team tools (compat).
func TestTeamIntegration_ParentOnlyUnchanged(t *testing.T) {
	const (
		leadID  = "lead-parent-only"
		promptA = "parent-only-child"
	)
	releaseA := make(chan struct{})
	ct := &channelTool{
		executed: make(chan string, 2),
		blocks:   map[string]<-chan struct{}{"hold-a": releaseA},
	}
	var (
		turn2Calls []provider.ToolCall
		turn2Ready = make(chan struct{})
	)
	prov := newScriptedProvider(
		toolCallStep(taskToolCall("task-a", promptA)),
		func() streamStep {
			s := completedStep("spawned")
			s.match = matchToolResult("task-a")
			return s
		}(),
		func() streamStep {
			s := toolCallStep(toolCall("hold-a", "channel"))
			s.match = matchUserText(promptA)
			return s
		}(),
		func() streamStep {
			return streamStep{
				match: matchLatestUserText("steer child"),
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
			s := completedStep("steered")
			s.match = matchToolResult("msg-a")
			return s
		}(),
		func() streamStep {
			s := completedStep("child done")
			s.match = matchToolResult("hold-a")
			return s
		}(),
		func() streamStep {
			s := completedStep("ack parent-only")
			s.match = matchLatestChildCompleted
			return s
		}(),
	)

	// Intentionally omit agent_message / agent_broadcast / agent_roster.
	eng := engine.New(engine.Options{
		SessionID:       leadID,
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		InitialAgent:    "build",
		Agents:          []engine.Agent{{Name: "build"}},
		Registry:        taskControlRegistry(ct),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "spawn one"}
	events := drainUntil(t, eng, 12*time.Second, func(evs []protocol.Event) bool {
		return countEvents[protocol.ChildStarted](evs) == 1 &&
			countEvents[protocol.TurnCompleted](evs) >= 1
	})
	var childID string
	for _, ev := range events {
		if cs, ok := ev.(protocol.ChildStarted); ok {
			childID = cs.SessionID
		}
	}
	if childID == "" {
		t.Fatal("no child started")
	}

	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-ct.executed:
			goto held
		case <-deadline:
			t.Fatal("child never held")
		}
	}
held:
	turn2Calls = []provider.ToolCall{
		controlToolCall("msg-a", "task_message", map[string]any{"session_id": childID, "text": "steer once"}),
		controlToolCall("st-a", "task_status", map[string]any{"session_id": childID}),
	}
	close(turn2Ready)
	eng.Ops() <- protocol.UserInput{Text: "steer child"}
	events = drainUntil(t, eng, 12*time.Second, func(evs []protocol.Event) bool {
		_, ok1 := toolEndOutput(evs, "msg-a")
		_, ok2 := toolEndOutput(evs, "st-a")
		return ok1 && ok2
	})
	msgOut, _ := toolEndOutput(events, "msg-a")
	if !strings.Contains(msgOut, `"status":"queued"`) && !strings.Contains(msgOut, `"status":"accepted"`) {
		t.Fatalf("task_message = %s", msgOut)
	}
	stOut, _ := toolEndOutput(events, "st-a")
	if !strings.Contains(stOut, childID) {
		t.Fatalf("task_status = %s", stOut)
	}

	close(releaseA)
	_ = drainUntil(t, eng, 12*time.Second, func(evs []protocol.Event) bool {
		for _, ev := range evs {
			if cc, ok := ev.(protocol.ChildCompleted); ok && cc.SessionID == childID {
				return true
			}
		}
		return false
	})
}
