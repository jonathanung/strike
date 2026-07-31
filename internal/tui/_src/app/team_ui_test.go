package tui

import (
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

func TestOnTeamRosterUpdatesNamesAndStates(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "lead-1"
	m.children = []childActivity{
		{sessionID: "c1", agent: "explore", status: "running"},
	}
	m.onTeamRoster(protocol.TeamRoster{
		Correlation: protocol.Correlation{SessionID: "lead-1"},
		LeadID:      "lead-1",
		Members: []protocol.TeamRosterMember{
			{SessionID: "lead-1", Role: "lead", State: "working", Name: "lead"},
			{SessionID: "c1", Role: "member", Name: "researcher", Agent: "explore", State: "working", ParentSessionID: "lead-1"},
			{SessionID: "c2", Role: "member", Name: "implementer", Agent: "general", State: "needs_attention", ParentSessionID: "lead-1"},
		},
	})
	if len(m.children) != 2 {
		t.Fatalf("children = %d, want 2 (lead skipped)", len(m.children))
	}
	byID := map[string]childActivity{}
	for _, ch := range m.children {
		byID[ch.sessionID] = ch
	}
	if byID["c1"].name != "researcher" {
		t.Errorf("c1 name = %q, want researcher", byID["c1"].name)
	}
	if byID["c1"].status != "running" {
		t.Errorf("c1 status = %q, want running", byID["c1"].status)
	}
	if byID["c2"].name != "implementer" || byID["c2"].status != "running" {
		t.Errorf("c2 = %+v", byID["c2"])
	}
	if byID["c2"].rosterState != "needs you" {
		t.Errorf("c2 rosterState = %q, want needs you", byID["c2"].rosterState)
	}
}

func TestOnAgentMessageBoundedAndDeduped(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "lead"
	// Flood past the cap with unique ids.
	for i := 0; i < maxTeamMessages+10; i++ {
		m.onAgentMessage(protocol.AgentMessage{
			Correlation: protocol.Correlation{SessionID: "lead"},
			From:        "c1",
			To:          "lead",
			Body:        "hello " + itoa(i),
			MessageID:   "m" + itoa(i),
			TeamID:      "lead",
		})
	}
	if len(m.teamMessages) != maxTeamMessages {
		t.Fatalf("len = %d, want %d", len(m.teamMessages), maxTeamMessages)
	}
	// Oldest dropped.
	if m.teamMessages[0].id != "m10" {
		t.Errorf("oldest id = %q, want m10", m.teamMessages[0].id)
	}
	// Dedup by message id.
	before := len(m.teamMessages)
	m.onAgentMessage(protocol.AgentMessage{
		From: "c1", To: "lead", Body: "dup", MessageID: "m10",
	})
	if len(m.teamMessages) != before {
		t.Errorf("dedup failed: len %d → %d", before, len(m.teamMessages))
	}
}

func TestApplyEventAcceptsChildCorrelatedTeamEvents(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "lead"
	// Child correlation would previously drop these (ParentSessionID set).
	_ = m.applyEvent(protocol.TeamRoster{
		Correlation: protocol.Correlation{SessionID: "c1", ParentSessionID: "lead", Depth: 1},
		LeadID:      "lead",
		Members: []protocol.TeamRosterMember{
			{SessionID: "c1", Role: "member", Name: "scout", State: "working", ParentSessionID: "lead"},
		},
	})
	if len(m.children) != 1 || m.children[0].name != "scout" {
		t.Fatalf("roster not applied: %+v", m.children)
	}
	_ = m.applyEvent(protocol.AgentMessage{
		Correlation: protocol.Correlation{SessionID: "c1", ParentSessionID: "lead", Depth: 1},
		From:        "c1",
		To:          "lead",
		Body:        "status update from child",
		Summary:     "status",
		MessageID:   "am-1",
		TeamID:      "lead",
	})
	if len(m.teamMessages) != 1 {
		t.Fatalf("messages = %d, want 1", len(m.teamMessages))
	}
	if m.teamMessages[0].summary != "status" {
		t.Errorf("summary = %q", m.teamMessages[0].summary)
	}
}

func TestSoftCoalesceTeamEvents(t *testing.T) {
	if !softCoalesceEvent(protocol.AgentMessage{Body: "x"}) {
		t.Error("AgentMessage should soft-coalesce")
	}
	if !softCoalesceEvent(protocol.TeamRoster{LeadID: "l"}) {
		t.Error("TeamRoster should soft-coalesce")
	}
	if softCoalesceEvent(protocol.TurnStarted{}) {
		t.Error("TurnStarted must not soft-coalesce")
	}
}

func TestTeamMsgActivityLabel(t *testing.T) {
	label := teamMsgActivityLabel(teamMessage{
		from: "aaa", to: "bbb", summary: "ping",
	}, func(id string) string {
		if id == "aaa" {
			return "researcher"
		}
		if id == "bbb" {
			return "lead"
		}
		return ""
	})
	if !strings.Contains(label, "researcher") || !strings.Contains(label, "lead") || !strings.Contains(label, "ping") {
		t.Fatalf("label = %q", label)
	}
}

func TestChildrenFromEventsAppliesRosterNames(t *testing.T) {
	events := []protocol.Event{
		protocol.ChildStarted{
			Correlation: protocol.Correlation{SessionID: "c1", ParentSessionID: "lead"},
			Agent:       "explore",
			Name:        "",
		},
		protocol.TeamRoster{
			Correlation: protocol.Correlation{SessionID: "lead"},
			LeadID:      "lead",
			Members: []protocol.TeamRosterMember{
				{SessionID: "lead", Role: "lead", State: "working"},
				{SessionID: "c1", Role: "member", Name: "researcher", State: "completed", ParentSessionID: "lead"},
			},
		},
		protocol.ChildCompleted{
			Correlation: protocol.Correlation{SessionID: "c1", ParentSessionID: "lead"},
			Status:      protocol.ChildStatusCompleted,
		},
	}
	got := childrenFromEvents(events)
	if len(got) != 1 {
		t.Fatalf("len = %d: %#v", len(got), got)
	}
	if got[0].name != "researcher" {
		t.Errorf("name = %q, want researcher", got[0].name)
	}
	if got[0].status != string(protocol.ChildStatusCompleted) {
		t.Errorf("status = %q", got[0].status)
	}
}

func TestTeamMessagesFromEventsBounded(t *testing.T) {
	events := make([]protocol.Event, 0, maxTeamMessages+5)
	for i := 0; i < maxTeamMessages+5; i++ {
		events = append(events, protocol.AgentMessage{
			From: "a", To: "b", Body: "b" + itoa(i), MessageID: "id" + itoa(i),
		})
	}
	got := teamMessagesFromEvents(events)
	if len(got) != maxTeamMessages {
		t.Fatalf("len = %d, want %d", len(got), maxTeamMessages)
	}
}
