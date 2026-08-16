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

func agentMessageRegistry() *tool.Registry {
	return tool.NewRegistry(
		tool.NewAgentMessage(),
		tool.NewAgentBroadcast(),
		tool.NewAgentRoster(),
	)
}

func TestAgentMessageChildToChild(t *testing.T) {
	const (
		leadID = "lead-am-c2c"
		fromID = "agent-a-am"
		toID   = "agent-b-am"
		body   = "change X in path Y"
	)

	team := engine.NewTeam(leadID, "build")
	_ = team.Enroll(engine.TeamMember{SessionID: fromID, ParentSessionID: leadID, Persona: "explore", Depth: 1})
	_ = team.Enroll(engine.TeamMember{SessionID: toID, ParentSessionID: leadID, Persona: "general", Depth: 1})

	toProv := newScriptedProvider(
		completedStep("warmup-b"),
		completedStep("got-mail"),
	)
	fromCall := controlToolCall("am-1", "agent_message", map[string]any{
		"to": toID, "body": body, "summary": "handoff",
	})
	fromProv := newScriptedProvider(
		toolCallStep(fromCall),
		func() streamStep {
			s := completedStep("sent")
			s.match = matchToolResult("am-1")
			return s
		}(),
	)

	lead := engine.New(engine.Options{
		SessionID: leadID,
		Team:      team,
		Agents:    []engine.Agent{{Name: "build"}},
		Select: func(string) (provider.Provider, string, error) {
			return newScriptedProvider(completedStep("lead")), "m", nil
		},
		InitialProvider: "scripted",
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	from := engine.New(engine.Options{
		SessionID:       fromID,
		ParentSessionID: leadID,
		Depth:           1,
		Team:            team,
		Agents:          []engine.Agent{{Name: "explore"}},
		Select:          func(string) (provider.Provider, string, error) { return fromProv, "m", nil },
		InitialProvider: "scripted",
		Registry:        agentMessageRegistry(),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	to := engine.New(engine.Options{
		SessionID:       toID,
		ParentSessionID: leadID,
		Depth:           1,
		Team:            team,
		Agents:          []engine.Agent{{Name: "general"}},
		Select:          func(string) (provider.Provider, string, error) { return toProv, "m", nil },
		InitialProvider: "scripted",
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go lead.Run(ctx)
	go from.Run(ctx)
	go to.Run(ctx)
	go func() {
		for range lead.Events() {
		}
	}()

	waitTeamLive(t, team, leadID, fromID, toID)

	from.Ops() <- protocol.UserInput{Text: "send peer mail"}
	events := drainUntil(t, from, 8*time.Second, func(evs []protocol.Event) bool {
		_, ok := toolEndOutput(evs, "am-1")
		return ok
	})
	out, ok := toolEndOutput(events, "am-1")
	if !ok {
		t.Fatal("missing agent_message tool end")
	}
	var parsed tool.AgentMessageResult
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("parse %q: %v", out, err)
	}
	if parsed.Status != "accepted" || parsed.To != toID || parsed.MessageID == "" {
		t.Fatalf("result = %+v", parsed)
	}

	// Recipient should auto-nudge with body.
	deadline := time.After(3 * time.Second)
	for {
		select {
		case req := <-toProv.requests:
			if requestHasMailboxBody(req, body) {
				return
			}
		case <-deadline:
			t.Fatal("recipient never saw peer body")
		}
	}
}

func TestAgentMessageChildToLead(t *testing.T) {
	const (
		leadID = "lead-am-c2l"
		fromID = "child-am-c2l"
		body   = "child-to-lead-ping"
	)
	team := engine.NewTeam(leadID, "build")
	_ = team.Enroll(engine.TeamMember{SessionID: fromID, ParentSessionID: leadID, Depth: 1})

	leadProv := newScriptedProvider(
		completedStep("lead-warm"),
		completedStep("lead-got-mail"),
	)
	call := controlToolCall("am-c2l", "agent_message", map[string]any{"to": leadID, "body": body})
	childProv := newScriptedProvider(
		toolCallStep(call),
		func() streamStep {
			s := completedStep("child-sent")
			s.match = matchToolResult("am-c2l")
			return s
		}(),
	)

	lead := engine.New(engine.Options{
		SessionID:       leadID,
		Team:            team,
		Agents:          []engine.Agent{{Name: "build"}},
		Select:          func(string) (provider.Provider, string, error) { return leadProv, "m", nil },
		InitialProvider: "scripted",
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	child := engine.New(engine.Options{
		SessionID:       fromID,
		ParentSessionID: leadID,
		Depth:           1,
		Team:            team,
		Agents:          []engine.Agent{{Name: "general"}},
		Select:          func(string) (provider.Provider, string, error) { return childProv, "m", nil },
		InitialProvider: "scripted",
		Registry:        agentMessageRegistry(),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go lead.Run(ctx)
	go child.Run(ctx)
	// Drain lead events so mailbox-nudge turn does not block on a full buffer.
	go func() {
		for range lead.Events() {
		}
	}()
	waitTeamLive(t, team, leadID, fromID)

	child.Ops() <- protocol.UserInput{Text: "msg lead"}
	_ = drainUntil(t, child, 8*time.Second, func(evs []protocol.Event) bool {
		_, ok := toolEndOutput(evs, "am-c2l")
		return ok
	})

	deadline := time.After(3 * time.Second)
	for {
		select {
		case req := <-leadProv.requests:
			if requestHasMailboxBody(req, body) {
				return
			}
		case <-deadline:
			t.Fatal("lead never saw child message")
		}
	}
}

func TestAgentMessageLeadToChild(t *testing.T) {
	const (
		leadID = "lead-am-l2c"
		toID   = "child-am-l2c"
		body   = "lead-to-child-steer"
	)
	team := engine.NewTeam(leadID, "build")
	_ = team.Enroll(engine.TeamMember{SessionID: toID, ParentSessionID: leadID, Depth: 1})

	childProv := newScriptedProvider(
		completedStep("child-warm"),
		completedStep("child-got"),
	)
	call := controlToolCall("am-l2c", "agent_message", map[string]any{"to": toID, "body": body})
	leadProv := newScriptedProvider(
		toolCallStep(call),
		func() streamStep {
			s := completedStep("lead-sent")
			s.match = matchToolResult("am-l2c")
			return s
		}(),
	)

	lead := engine.New(engine.Options{
		SessionID:       leadID,
		Team:            team,
		Agents:          []engine.Agent{{Name: "build"}},
		Select:          func(string) (provider.Provider, string, error) { return leadProv, "m", nil },
		InitialProvider: "scripted",
		Registry:        agentMessageRegistry(),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	child := engine.New(engine.Options{
		SessionID:       toID,
		ParentSessionID: leadID,
		Depth:           1,
		Team:            team,
		Agents:          []engine.Agent{{Name: "general"}},
		Select:          func(string) (provider.Provider, string, error) { return childProv, "m", nil },
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

	lead.Ops() <- protocol.UserInput{Text: "steer child"}
	events := drainUntil(t, lead, 8*time.Second, func(evs []protocol.Event) bool {
		_, ok := toolEndOutput(evs, "am-l2c")
		return ok
	})
	out, _ := toolEndOutput(events, "am-l2c")
	var parsed tool.AgentMessageResult
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Status != "accepted" {
		t.Fatalf("status = %+v", parsed)
	}

	deadline := time.After(3 * time.Second)
	for {
		select {
		case req := <-childProv.requests:
			if requestHasMailboxBody(req, body) {
				return
			}
		case <-deadline:
			t.Fatal("child never saw lead message")
		}
	}
}

func TestAgentBroadcastDeliversNMinus1(t *testing.T) {
	const (
		leadID = "lead-ab-n1"
		aID    = "a-ab"
		bID    = "b-ab"
		body   = "broadcast-body"
	)
	team := engine.NewTeam(leadID, "build")
	_ = team.Enroll(engine.TeamMember{SessionID: aID, ParentSessionID: leadID, Depth: 1})
	_ = team.Enroll(engine.TeamMember{SessionID: bID, ParentSessionID: leadID, Depth: 1})

	aProv := newScriptedProvider(completedStep("a-warm"), completedStep("a-mail"))
	bProv := newScriptedProvider(completedStep("b-warm"), completedStep("b-mail"))
	call := controlToolCall("ab-1", "agent_broadcast", map[string]any{"body": body})
	leadProv := newScriptedProvider(
		toolCallStep(call),
		func() streamStep {
			s := completedStep("lead-bc")
			s.match = matchToolResult("ab-1")
			return s
		}(),
	)

	lead := engine.New(engine.Options{
		SessionID: leadID, Team: team, Agents: []engine.Agent{{Name: "build"}},
		Select:          func(string) (provider.Provider, string, error) { return leadProv, "m", nil },
		InitialProvider: "scripted", Registry: agentMessageRegistry(),
		WorkDir: t.TempDir(), Rules: []permission.Ruleset{permission.Defaults()},
	})
	a := engine.New(engine.Options{
		SessionID: aID, ParentSessionID: leadID, Depth: 1, Team: team,
		Agents:          []engine.Agent{{Name: "general"}},
		Select:          func(string) (provider.Provider, string, error) { return aProv, "m", nil },
		InitialProvider: "scripted", WorkDir: t.TempDir(),
		Rules: []permission.Ruleset{permission.Defaults()},
	})
	b := engine.New(engine.Options{
		SessionID: bID, ParentSessionID: leadID, Depth: 1, Team: team,
		Agents:          []engine.Agent{{Name: "general"}},
		Select:          func(string) (provider.Provider, string, error) { return bProv, "m", nil },
		InitialProvider: "scripted", WorkDir: t.TempDir(),
		Rules: []permission.Ruleset{permission.Defaults()},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go lead.Run(ctx)
	go a.Run(ctx)
	go b.Run(ctx)
	go func() {
		for range a.Events() {
		}
	}()
	go func() {
		for range b.Events() {
		}
	}()
	waitTeamLive(t, team, leadID, aID, bID)

	lead.Ops() <- protocol.UserInput{Text: "broadcast"}
	events := drainUntil(t, lead, 8*time.Second, func(evs []protocol.Event) bool {
		_, ok := toolEndOutput(evs, "ab-1")
		return ok
	})
	out, _ := toolEndOutput(events, "ab-1")
	var parsed tool.AgentBroadcastResult
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("parse %q: %v", out, err)
	}
	if parsed.Delivered != 2 || parsed.Rejected != 0 || len(parsed.Results) != 2 {
		t.Fatalf("broadcast = %+v", parsed)
	}
	// Sender not in results.
	for _, r := range parsed.Results {
		if r.To == leadID {
			t.Fatalf("broadcast included self: %+v", parsed)
		}
	}

	seenA, seenB := false, false
	deadline := time.After(4 * time.Second)
	for !seenA || !seenB {
		select {
		case req := <-aProv.requests:
			if requestHasMailboxBody(req, body) {
				seenA = true
			}
		case req := <-bProv.requests:
			if requestHasMailboxBody(req, body) {
				seenB = true
			}
		case <-deadline:
			t.Fatalf("a=%v b=%v never both got body", seenA, seenB)
		}
	}
}

func TestAgentMessageOutOfTeamFailsClosed(t *testing.T) {
	const (
		leadID = "lead-am-oot"
		fromID = "from-am-oot"
	)
	team := engine.NewTeam(leadID, "build")
	_ = team.Enroll(engine.TeamMember{SessionID: fromID, ParentSessionID: leadID, Depth: 1})

	call := controlToolCall("am-oot", "agent_message", map[string]any{
		"to": "foreign-session", "body": "should fail",
	})
	prov := newScriptedProvider(
		toolCallStep(call),
		func() streamStep {
			s := completedStep("after-reject")
			s.match = matchToolResult("am-oot")
			return s
		}(),
	)
	eng := engine.New(engine.Options{
		SessionID: fromID, ParentSessionID: leadID, Depth: 1, Team: team,
		Agents:          []engine.Agent{{Name: "general"}},
		Select:          func(string) (provider.Provider, string, error) { return prov, "m", nil },
		InitialProvider: "scripted",
		Registry:        agentMessageRegistry(),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	// Lead must exist on team for AttachMailbox of from alone... from can attach alone.
	lead := engine.New(engine.Options{
		SessionID: leadID, Team: team, Agents: []engine.Agent{{Name: "build"}},
		Select: func(string) (provider.Provider, string, error) {
			return newScriptedProvider(completedStep("l")), "m", nil
		},
		InitialProvider: "scripted", WorkDir: t.TempDir(),
		Rules: []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go lead.Run(ctx)
	go eng.Run(ctx)
	go func() {
		for range lead.Events() {
		}
	}()
	waitTeamLive(t, team, leadID, fromID)

	eng.Ops() <- protocol.UserInput{Text: "try oot"}
	events := drainUntil(t, eng, 8*time.Second, func(evs []protocol.Event) bool {
		end, ok := toolEnd(evs, "am-oot")
		return ok && end.Output != ""
	})
	end, ok := toolEnd(events, "am-oot")
	if !ok {
		t.Fatal("no tool end")
	}
	if !end.IsError {
		t.Fatalf("want error tool result, got %#v", end)
	}
	if !strings.Contains(end.Output, "not on this team") && !strings.Contains(end.Output, "rejected") {
		t.Fatalf("output = %q", end.Output)
	}
}

func TestAgentMessageCrossTeamIsolation(t *testing.T) {
	// Two independent roots cannot message across teams.
	team1 := engine.NewTeam("root-1", "build")
	team2 := engine.NewTeam("root-2", "build")
	_ = team1.Enroll(engine.TeamMember{SessionID: "c1", ParentSessionID: "root-1", Depth: 1})
	_ = team2.Enroll(engine.TeamMember{SessionID: "c2", ParentSessionID: "root-2", Depth: 1})

	e1 := engine.New(engine.Options{
		SessionID: "c1", ParentSessionID: "root-1", Depth: 1, Team: team1,
		Agents: []engine.Agent{{Name: "general"}},
	})
	e2 := engine.New(engine.Options{
		SessionID: "c2", ParentSessionID: "root-2", Depth: 1, Team: team2,
		Agents: []engine.Agent{{Name: "general"}},
	})
	team1.AttachMailbox(e1)
	team2.AttachMailbox(e2)

	st := e1.EnqueueTeamMessage("c1", "c2", "cross")
	if st.Status != "rejected" {
		t.Fatalf("cross-team deliver = %#v, want rejected", st)
	}
	if !strings.Contains(st.Detail, "not on this team") {
		t.Fatalf("detail = %q", st.Detail)
	}
}

func TestAgentMessageResolveByName(t *testing.T) {
	team := engine.NewTeam("L", "build")
	_ = team.Enroll(engine.TeamMember{
		SessionID: "sess-x", ParentSessionID: "L", Name: "explorer", Depth: 1,
	})
	id, ok := team.ResolveAddress("explorer")
	if !ok || id != "sess-x" {
		t.Fatalf("resolve name = %q %v", id, ok)
	}
	id, ok = team.ResolveAddress("sess-x")
	if !ok || id != "sess-x" {
		t.Fatalf("resolve id = %q %v", id, ok)
	}
	if _, ok := team.ResolveAddress("missing"); ok {
		t.Fatal("missing should fail")
	}
	_, detail, ok := team.ResolveAddressDetail("missing")
	if ok || detail != "recipient is not on this team" {
		t.Fatalf("missing detail = %q ok=%v", detail, ok)
	}

	// Team.Enroll rejects duplicate names (#611); second enroll must fail and
	// the original alias must still resolve uniquely.
	if team.Enroll(engine.TeamMember{
		SessionID: "sess-y", ParentSessionID: "L", Name: "explorer", Depth: 1,
	}) {
		t.Fatal("duplicate name enroll should fail")
	}
	id, ok = team.ResolveAddress("explorer")
	if !ok || id != "sess-x" {
		t.Fatalf("after failed dup enroll resolve = %q %v", id, ok)
	}
}

// TestAgentMessageSiblingByShortSessionID reproduces #650: two children
// spawned by the same lead; one addresses the other with tool shortID (first
// 8 chars of session id) instead of the full id or stable name. Before the
// fix this failed closed with "recipient is not on this team".
func TestAgentMessageSiblingByShortSessionID(t *testing.T) {
	const (
		leadID = "1f0d0c5d-lead-0000-0000-000000000001"
		fromID = "a1b2c3d4-from-bbbb-cccc-ddddeeeeffff"
		toID   = "c0a9b0d4-toaa-bbbb-cccc-ddddeeeeffff"
		body   = "please create the files under /tmp/demo"
	)
	shortTo := toID[:8] // tool shortID / UI fragment
	if shortTo != "c0a9b0d4" {
		t.Fatalf("fixture short id = %q", shortTo)
	}

	team := engine.NewTeam(leadID, "build")
	if !team.Enroll(engine.TeamMember{
		SessionID: fromID, ParentSessionID: leadID, Name: "worker-b", Persona: "general", Depth: 1,
	}) {
		t.Fatal("enroll from")
	}
	if !team.Enroll(engine.TeamMember{
		SessionID: toID, ParentSessionID: leadID, Name: "worker-a", Persona: "general", Depth: 1,
	}) {
		t.Fatal("enroll to")
	}

	toProv := newScriptedProvider(
		completedStep("warmup-a"),
		completedStep("got-mail"),
	)
	fromCall := controlToolCall("am-short", "agent_message", map[string]any{
		"to": shortTo, "body": body,
	})
	fromProv := newScriptedProvider(
		toolCallStep(fromCall),
		func() streamStep {
			s := completedStep("sent")
			s.match = matchToolResult("am-short")
			return s
		}(),
	)

	lead := engine.New(engine.Options{
		SessionID: leadID, Team: team, Agents: []engine.Agent{{Name: "build"}},
		Select: func(string) (provider.Provider, string, error) {
			return newScriptedProvider(completedStep("lead")), "m", nil
		},
		InitialProvider: "scripted", WorkDir: t.TempDir(),
		Rules: []permission.Ruleset{permission.Defaults()},
	})
	from := engine.New(engine.Options{
		SessionID: fromID, ParentSessionID: leadID, Depth: 1, Team: team,
		Agents:          []engine.Agent{{Name: "general"}},
		Select:          func(string) (provider.Provider, string, error) { return fromProv, "m", nil },
		InitialProvider: "scripted", Registry: agentMessageRegistry(),
		WorkDir: t.TempDir(), Rules: []permission.Ruleset{permission.Defaults()},
	})
	to := engine.New(engine.Options{
		SessionID: toID, ParentSessionID: leadID, Depth: 1, Team: team,
		Agents:          []engine.Agent{{Name: "general"}},
		Select:          func(string) (provider.Provider, string, error) { return toProv, "m", nil },
		InitialProvider: "scripted", WorkDir: t.TempDir(),
		Rules: []permission.Ruleset{permission.Defaults()},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go lead.Run(ctx)
	go from.Run(ctx)
	go to.Run(ctx)
	go func() {
		for range lead.Events() {
		}
	}()
	waitTeamLive(t, team, leadID, fromID, toID)

	from.Ops() <- protocol.UserInput{Text: "msg sibling by short id"}
	events := drainUntil(t, from, 8*time.Second, func(evs []protocol.Event) bool {
		_, ok := toolEndOutput(evs, "am-short")
		return ok
	})
	out, ok := toolEndOutput(events, "am-short")
	if !ok {
		t.Fatal("missing agent_message tool end")
	}
	var parsed tool.AgentMessageResult
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("parse %q: %v", out, err)
	}
	if parsed.Status != "accepted" || parsed.To != toID || parsed.MessageID == "" {
		t.Fatalf("result = %+v, want accepted to full session id", parsed)
	}

	deadline := time.After(3 * time.Second)
	for {
		select {
		case req := <-toProv.requests:
			if requestHasMailboxBody(req, body) {
				return
			}
		case <-deadline:
			t.Fatal("recipient never saw peer body")
		}
	}
}

func TestAgentMessageByNameDelivers(t *testing.T) {
	const (
		leadID = "lead-am-byname"
		toID   = "child-named-am"
		body   = "hello-by-name"
	)
	team := engine.NewTeam(leadID, "build")
	if !team.Enroll(engine.TeamMember{
		SessionID: toID, ParentSessionID: leadID, Name: "researcher", Depth: 1,
	}) {
		t.Fatal("enroll")
	}
	childProv := newScriptedProvider(completedStep("warm"), completedStep("got"))
	call := controlToolCall("am-name", "agent_message", map[string]any{
		"to": "researcher", "body": body,
	})
	leadProv := newScriptedProvider(
		toolCallStep(call),
		func() streamStep {
			s := completedStep("sent")
			s.match = matchToolResult("am-name")
			return s
		}(),
	)
	lead := engine.New(engine.Options{
		SessionID: leadID, Team: team, Agents: []engine.Agent{{Name: "build"}},
		Select:          func(string) (provider.Provider, string, error) { return leadProv, "m", nil },
		InitialProvider: "scripted", Registry: agentMessageRegistry(),
		WorkDir: t.TempDir(), Rules: []permission.Ruleset{permission.Defaults()},
	})
	child := engine.New(engine.Options{
		SessionID: toID, ParentSessionID: leadID, Depth: 1, Team: team,
		Agents:          []engine.Agent{{Name: "explore"}},
		Select:          func(string) (provider.Provider, string, error) { return childProv, "m", nil },
		InitialProvider: "scripted", WorkDir: t.TempDir(),
		Rules: []permission.Ruleset{permission.Defaults()},
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
	lead.Ops() <- protocol.UserInput{Text: "msg by name"}
	events := drainUntil(t, lead, 8*time.Second, func(evs []protocol.Event) bool {
		_, ok := toolEndOutput(evs, "am-name")
		return ok
	})
	out, ok := toolEndOutput(events, "am-name")
	if !ok {
		t.Fatal("missing tool end")
	}
	var parsed tool.AgentMessageResult
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Status != "accepted" || parsed.To != toID {
		t.Fatalf("result = %+v", parsed)
	}
	deadline := time.After(3 * time.Second)
	for {
		select {
		case req := <-childProv.requests:
			if requestHasMailboxBody(req, body) {
				return
			}
		case <-deadline:
			t.Fatal("named recipient never saw body")
		}
	}
}

func TestAgentMessagePermissionDefaultAllow(t *testing.T) {
	if got := permission.Evaluate("agent_message", "*", permission.Defaults()); got != permission.Allow {
		t.Fatalf("Defaults agent_message = %q, want allow", got)
	}
	if got := permission.Evaluate("agent_broadcast", "*", permission.Defaults()); got != permission.Allow {
		t.Fatalf("Defaults agent_broadcast = %q, want allow", got)
	}
}

func TestAgentMessageAvailableAtMaxDepth(t *testing.T) {
	// Depth == MaxChildDepth still wires messaging (team present).
	const (
		leadID = "lead-am-depth"
		leafID = "leaf-am-depth"
		body   = "from-leaf"
	)
	team := engine.NewTeam(leadID, "build")
	_ = team.Enroll(engine.TeamMember{SessionID: leafID, ParentSessionID: leadID, Depth: 1})

	leadProv := newScriptedProvider(completedStep("lw"), completedStep("got"))
	call := controlToolCall("am-leaf", "agent_message", map[string]any{"to": leadID, "body": body})
	leafProv := newScriptedProvider(
		toolCallStep(call),
		func() streamStep {
			s := completedStep("leaf-done")
			s.match = matchToolResult("am-leaf")
			return s
		}(),
	)

	lead := engine.New(engine.Options{
		SessionID: leadID, Team: team, Agents: []engine.Agent{{Name: "build"}},
		Select:          func(string) (provider.Provider, string, error) { return leadProv, "m", nil },
		InitialProvider: "scripted", WorkDir: t.TempDir(),
		Rules: []permission.Ruleset{permission.Defaults()},
	})
	leaf := engine.New(engine.Options{
		SessionID: leafID, ParentSessionID: leadID, Depth: 1, MaxChildDepth: 1,
		Team: team, Agents: []engine.Agent{{Name: "general"}},
		Select:          func(string) (provider.Provider, string, error) { return leafProv, "m", nil },
		InitialProvider: "scripted",
		Registry:        agentMessageRegistry(),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go lead.Run(ctx)
	go leaf.Run(ctx)
	go func() {
		for range lead.Events() {
		}
	}()
	waitTeamLive(t, team, leadID, leafID)

	leaf.Ops() <- protocol.UserInput{Text: "leaf msg"}
	events := drainUntil(t, leaf, 8*time.Second, func(evs []protocol.Event) bool {
		_, ok := toolEndOutput(evs, "am-leaf")
		return ok
	})
	out, ok := toolEndOutput(events, "am-leaf")
	if !ok {
		t.Fatal("missing tool output")
	}
	var parsed tool.AgentMessageResult
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Status != "accepted" {
		t.Fatalf("leaf at max depth could not message: %+v", parsed)
	}
}

// waitTeamLive waits until every id is attached for mailbox delivery.
// Does not enqueue messages (avoids idle auto-nudge races in tests).
func waitTeamLive(t *testing.T, team *engine.Team, ids ...string) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		ready := true
		for _, id := range ids {
			if !team.IsLive(id) {
				ready = false
				break
			}
		}
		if ready {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("attach timeout ids=%v", ids)
		case <-time.After(5 * time.Millisecond):
		}
	}
}
