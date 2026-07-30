package engine_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/engine"
	"github.com/jonathanung/strike-cli/internal/permission"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/provider"
	"github.com/jonathanung/strike-cli/internal/tool"
)

// TestMailboxInjectedAtToolRoundBoundary: A→B while B is mid-turn (after a
// tool batch, before next Stream) lands in B's next model request — never
// mid-tool-call corruption of history shape.
func TestMailboxInjectedAtToolRoundBoundary(t *testing.T) {
	const (
		leadID = "lead-mb-boundary"
		toID   = "agent-b-mb"
		body   = "change X in path Y"
	)

	releaseTool := make(chan struct{})
	childProv := newScriptedProvider(
		toolCallStep(provider.ToolCall{
			ID:   "slow-1",
			Name: "gate",
			Args: json.RawMessage(`{}`),
		}),
		completedStep("b-done-after-mail"),
	)

	team := engine.NewTeam(leadID, "build")
	if team == nil {
		t.Fatal("nil team")
	}
	if !team.Enroll(engine.TeamMember{SessionID: toID, ParentSessionID: leadID, Persona: "general", Depth: 1}) {
		t.Fatal("enroll B")
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
		Select:          func(string) (provider.Provider, string, error) { return childProv, "m", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(gatedTool{name: "gate", release: releaseTool}),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go lead.Run(ctx)
	go child.Run(ctx)

	// Drain lead events so AgentMessage re-emit does not fill the buffer.
	go func() {
		for range lead.Events() {
		}
	}()

	// Wait for both to attach (Run starts).
	waitAttached := time.After(2 * time.Second)
	for {
		st := team.Deliver(leadID, toID, "probe")
		if st.Status == "accepted" {
			if box := child.Mailbox(); box != nil {
				_ = box.TakePending()
			}
			break
		}
		select {
		case <-waitAttached:
			t.Fatalf("attach timeout: %#v", st)
		case <-time.After(10 * time.Millisecond):
		}
	}

	// Start B's turn.
	child.Ops() <- protocol.UserInput{Text: "b-start-work"}

	// Wait until first provider request (tool call round).
	_ = receiveRequest(t, childProv.requests)

	// Enqueue while B is mid-turn (tool blocked on gate).
	st := lead.EnqueueTeamMessage(leadID, toID, body)
	if st.Status != "accepted" {
		t.Fatalf("deliver mid-turn = %#v", st)
	}

	close(releaseTool)

	// Second stream should include the peer message at the tool-round boundary.
	second := receiveRequest(t, childProv.requests)
	if !requestHasMailboxBody(second, body) {
		t.Fatalf("second stream missing peer body %q; messages=%#v", body, second.Messages)
	}
	if !historyToolPairsOK(second.Messages) {
		t.Fatalf("invalid tool pairs after mailbox inject: %#v", second.Messages)
	}

	if st.MessageID == "" {
		t.Fatal("missing message id on deliver")
	}

	// AgentMessage is emitted on boundary inject (recipient Run/turn path).
	// Drain until we see it (other turn lifecycle events may come first).
	msgDeadline := time.After(3 * time.Second)
	for {
		select {
		case ev := <-child.Events():
			if am, ok := ev.(protocol.AgentMessage); ok && am.Body == body {
				if am.From != leadID || am.To != toID || am.TeamID != leadID {
					t.Fatalf("AgentMessage = %#v", am)
				}
				if am.MessageID != st.MessageID {
					t.Fatalf("MessageID = %q, want %q", am.MessageID, st.MessageID)
				}
				return
			}
		case <-msgDeadline:
			t.Fatal("no AgentMessage event after boundary inject")
		}
	}
}

// TestMailboxIdleAutoNudge: message to idle teammate starts a follow-up turn
// with the body visible to the model.
func TestMailboxIdleAutoNudge(t *testing.T) {
	const (
		leadID = "lead-mb-idle"
		toID   = "agent-idle-mb"
		body   = "idle-peer-ping"
	)
	childProv := newScriptedProvider(
		completedStep("warmup-ack"),
		completedStep("idle-ack"),
	)

	team := engine.NewTeam(leadID, "build")
	_ = team.Enroll(engine.TeamMember{SessionID: toID, ParentSessionID: leadID, Depth: 1})

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
		for range lead.Events() {
		}
	}()
	go func() {
		for range child.Events() {
		}
	}()

	// Wait for attach via a warmup deliver that also exercises idle nudge.
	deadline := time.After(2 * time.Second)
	for {
		st := team.Deliver(leadID, toID, "warmup")
		if st.Status == "accepted" {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("attach: %#v", st)
		case <-time.After(5 * time.Millisecond):
		}
	}

	// Consume warmup stream if it started.
	select {
	case <-childProv.requests:
	case <-time.After(500 * time.Millisecond):
	}

	st := lead.EnqueueTeamMessage("", toID, body)
	if st.Status != "accepted" {
		t.Fatalf("idle deliver = %#v", st)
	}

	deadline = time.After(3 * time.Second)
	for {
		select {
		case req := <-childProv.requests:
			if requestHasMailboxBody(req, body) {
				return
			}
		case <-deadline:
			t.Fatal("idle nudge missing body in provider requests")
		}
	}
}

// TestMailboxRejectsCompletedRecipient: defined behavior for completed agents.
func TestMailboxRejectsCompletedRecipient(t *testing.T) {
	const (
		leadID = "lead-mb-done"
		toID   = "agent-done-mb"
	)
	team := engine.NewTeam(leadID, "build")
	_ = team.Enroll(engine.TeamMember{SessionID: toID, ParentSessionID: leadID, Depth: 1})
	team.SetState(toID, protocol.TeamMemberCompleted)

	lead := engine.New(engine.Options{
		SessionID: leadID,
		Team:      team,
		Agents:    []engine.Agent{{Name: "build"}},
		Select: func(string) (provider.Provider, string, error) {
			return newScriptedProvider(completedStep("x")), "m", nil
		},
		InitialProvider: "scripted",
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go lead.Run(ctx)
	go func() {
		for range lead.Events() {
		}
	}()
	time.Sleep(20 * time.Millisecond)

	st := lead.EnqueueTeamMessage(leadID, toID, "too late")
	if st.Status != "rejected" {
		t.Fatalf("want rejected, got %#v", st)
	}
	if !strings.Contains(st.Detail, "closed") && !strings.Contains(st.Detail, "completed") {
		t.Fatalf("detail = %q", st.Detail)
	}
}

// TestMailboxOrderingPreservedUnderConcurrentSenders uses one recipient and
// many concurrent EnqueueTeamMessage calls; all bodies arrive without corruption.
func TestMailboxOrderingPreservedUnderConcurrentSenders(t *testing.T) {
	const (
		leadID = "lead-mb-order"
		toID   = "agent-order-mb"
	)
	team := engine.NewTeam(leadID, "build")
	_ = team.Enroll(engine.TeamMember{SessionID: toID, ParentSessionID: leadID, Depth: 1})

	child := engine.New(engine.Options{
		SessionID:       toID,
		ParentSessionID: leadID,
		Depth:           1,
		Team:            team,
		Agents:          []engine.Agent{{Name: "general"}},
	})
	lead := engine.New(engine.Options{
		SessionID: leadID,
		Team:      team,
		Agents:    []engine.Agent{{Name: "build"}},
	})
	team.AttachMailbox(lead)
	team.AttachMailbox(child)

	const n = 40
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			st := team.Deliver(leadID, toID, fmt.Sprintf("ord-%d", i))
			if st.Status != "accepted" {
				t.Errorf("deliver %d: %#v", i, st)
			}
		}()
	}
	wg.Wait()

	box := child.Mailbox()
	if box == nil {
		t.Fatal("nil mailbox")
	}
	got := box.TakePending()
	if len(got) != n {
		t.Fatalf("len = %d, want %d", len(got), n)
	}
	seen := map[string]bool{}
	for _, m := range got {
		if seen[m.Body] {
			t.Fatalf("dup %q", m.Body)
		}
		seen[m.Body] = true
	}
	for i := 0; i < n; i++ {
		want := fmt.Sprintf("ord-%d", i)
		if !seen[want] {
			t.Fatalf("missing body %q", want)
		}
	}
}

// TestMailboxCapDropOldestUnderFlood verifies capacity policy via team deliver.
func TestMailboxCapDropOldestUnderFlood(t *testing.T) {
	const (
		leadID = "lead-mb-flood"
		toID   = "agent-flood-mb"
	)
	team := engine.NewTeam(leadID, "build")
	_ = team.Enroll(engine.TeamMember{SessionID: toID, ParentSessionID: leadID, Depth: 1})
	child := engine.New(engine.Options{
		SessionID: toID, ParentSessionID: leadID, Depth: 1, Team: team,
		Agents: []engine.Agent{{Name: "general"}},
	})
	lead := engine.New(engine.Options{
		SessionID: leadID, Team: team, Agents: []engine.Agent{{Name: "build"}},
	})
	team.AttachMailbox(lead)
	team.AttachMailbox(child)

	// Flood past capacity; last enqueue should report Dropped when eviction happens.
	var sawDrop bool
	for i := 0; i < 70; i++ {
		st := team.Deliver(leadID, toID, fmt.Sprintf("flood-%d", i))
		if st.Status != "accepted" {
			t.Fatalf("deliver %d: %#v", i, st)
		}
		if st.Dropped {
			sawDrop = true
		}
	}
	if !sawDrop {
		t.Fatal("expected at least one Dropped=true under flood")
	}
	box := child.Mailbox()
	if box.Len() > 64 {
		t.Fatalf("Len = %d, want <= 64", box.Len())
	}
	if box.Dropped() < 1 {
		t.Fatalf("Dropped = %d", box.Dropped())
	}
}

func requestHasMailboxBody(req provider.Request, body string) bool {
	for _, m := range req.Messages {
		if m.Role == provider.RoleUser && strings.Contains(m.Text, body) &&
			strings.Contains(m.Text, "[agent.message") {
			return true
		}
	}
	return false
}

// gatedTool blocks Execute until release is closed.
type gatedTool struct {
	name    string
	release <-chan struct{}
}

func (g gatedTool) Name() string { return g.name }
func (gatedTool) Description() string {
	return "test gate"
}
func (gatedTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (g gatedTool) Execute(ctx context.Context, _ json.RawMessage, _ *tool.Context) (tool.Result, error) {
	select {
	case <-ctx.Done():
		return tool.Result{}, ctx.Err()
	case <-g.release:
		return tool.Result{Title: g.name, Output: "ok"}, nil
	}
}
