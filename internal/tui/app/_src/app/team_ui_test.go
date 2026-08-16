package tui

import (
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

func TestOnTeamRosterDoesNotReviveTerminal(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "lead"
	m.children = []childActivity{
		{sessionID: "c1", name: "done-one", status: string(protocol.ChildStatusCompleted)},
	}
	m.onTeamRoster(protocol.TeamRoster{
		LeadID: "lead",
		Members: []protocol.TeamRosterMember{
			{SessionID: "c1", Role: "member", Name: "done-one", State: "working"},
		},
	})
	if m.children[0].status != string(protocol.ChildStatusCompleted) {
		t.Fatalf("status revived to %q", m.children[0].status)
	}
	// Terminal→terminal still allowed (failed after completed is rare but ok).
	m.onTeamRoster(protocol.TeamRoster{
		LeadID: "lead",
		Members: []protocol.TeamRosterMember{
			{SessionID: "c1", Role: "member", Name: "done-one", State: "failed"},
		},
	})
	if m.children[0].status != string(protocol.ChildStatusFailed) {
		t.Fatalf("terminal update = %q, want failed", m.children[0].status)
	}
}

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

func TestOnTeamRosterMergesObservability(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "lead"
	m.children = []childActivity{
		{sessionID: "c1", agent: "explore", status: "running"},
	}
	tokRem := 100
	m.onTeamRoster(protocol.TeamRoster{
		LeadID: "lead",
		Members: []protocol.TeamRosterMember{
			{
				SessionID: "c1", Role: "member", Name: "scout", State: "working",
				Objective: "map auth", LastAction: "read main.go", BlockReason: "",
				FilesTouched: []string{"a.go", "b.go"},
				Budget: &protocol.AgentBudgetView{
					MaxTokens: 500, TokensUsed: 50, TokensRemaining: &tokRem,
				},
			},
		},
	})
	ch := m.children[0]
	if ch.objective != "map auth" || ch.lastAction != "read main.go" {
		t.Fatalf("obs strings = objective=%q lastAction=%q", ch.objective, ch.lastAction)
	}
	if len(ch.filesTouched) != 2 || ch.filesTouched[0] != "a.go" {
		t.Fatalf("filesTouched = %#v", ch.filesTouched)
	}
	if ch.budget == nil || ch.budget.MaxTokens != 500 || ch.budget.TokensUsed != 50 {
		t.Fatalf("budget = %#v", ch.budget)
	}
	if ch.budget.TokensRemaining == nil || *ch.budget.TokensRemaining != 100 {
		t.Fatalf("tokens remaining = %#v", ch.budget.TokensRemaining)
	}
	// Missing wire fields stay empty/unknown — no fabricated block reason.
	if ch.blockReason != "" {
		t.Fatalf("blockReason = %q, want empty", ch.blockReason)
	}
	// Terminal non-revive still holds when observability is present.
	m.children[0].status = string(protocol.ChildStatusCompleted)
	m.onTeamRoster(protocol.TeamRoster{
		LeadID: "lead",
		Members: []protocol.TeamRosterMember{
			{
				SessionID: "c1", Role: "member", State: "working",
				Objective: "should not revive",
			},
		},
	})
	if m.children[0].status != string(protocol.ChildStatusCompleted) {
		t.Fatalf("status revived to %q", m.children[0].status)
	}
	if m.children[0].objective != "should not revive" {
		t.Fatalf("objective not updated on terminal row: %q", m.children[0].objective)
	}
}

func TestOnChildEscalatedUpdatesBudget(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "lead"
	m.children = []childActivity{
		{sessionID: "c1", status: "running", name: "worker"},
	}
	m.onChildEscalated(protocol.ChildEscalated{
		Correlation: protocol.Correlation{SessionID: "c1", ParentSessionID: "lead", Depth: 1},
		Name:        "worker",
		Kind:        "stall",
		Reason:      "no progress for 60s",
		Action:      protocol.EscalateActionInterrupted,
		Budget: &protocol.AgentBudgetView{
			Stall: true, Escalated: true, EscalateKind: "stall",
		},
	})
	ch := m.children[0]
	if ch.escalateKind != "stall" || ch.escalateAction != protocol.EscalateActionInterrupted {
		t.Fatalf("escalate = kind=%q action=%q", ch.escalateKind, ch.escalateAction)
	}
	if ch.escalateReason != "no progress for 60s" {
		t.Fatalf("reason = %q", ch.escalateReason)
	}
	if ch.budget == nil || !ch.budget.Stall || !ch.budget.Escalated {
		t.Fatalf("budget = %#v", ch.budget)
	}
	if ch.blockReason != "no progress for 60s" {
		t.Fatalf("blockReason = %q", ch.blockReason)
	}
}

func TestOnPathOverlapBounded(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "lead"
	m.children = []childActivity{{sessionID: "c1", status: "running"}}
	for i := 0; i < maxChildPathOverlaps+3; i++ {
		m.onPathOverlap(protocol.PathOverlap{
			Correlation: protocol.Correlation{SessionID: "c1", ParentSessionID: "lead", Depth: 1},
			Path:        "f" + itoa(i) + ".go",
			Policy:      "warn",
			Warning:     "overlap " + itoa(i),
		})
	}
	if len(m.children[0].pathOverlaps) != maxChildPathOverlaps {
		t.Fatalf("pathOverlaps = %d, want %d", len(m.children[0].pathOverlaps), maxChildPathOverlaps)
	}
	// Newest retained.
	last := m.children[0].pathOverlaps[len(m.children[0].pathOverlaps)-1]
	if last.path != "f"+(itoa(maxChildPathOverlaps+2))+".go" {
		t.Fatalf("last path = %q", last.path)
	}
	// Dedup same path+policy updates warning.
	m.onPathOverlap(protocol.PathOverlap{
		Correlation: protocol.Correlation{SessionID: "c1", ParentSessionID: "lead", Depth: 1},
		Path:        last.path,
		Policy:      "warn",
		Warning:     "updated",
	})
	if len(m.children[0].pathOverlaps) != maxChildPathOverlaps {
		t.Fatalf("dedup grew list to %d", len(m.children[0].pathOverlaps))
	}
	found := false
	for _, po := range m.children[0].pathOverlaps {
		if po.path == last.path && po.warning == "updated" {
			found = true
		}
	}
	if !found {
		t.Fatal("dedup did not refresh warning")
	}
}

func TestOnPathOverlapRootDoesNotCreateChild(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "lead"
	m.children = nil
	m.onPathOverlap(protocol.PathOverlap{
		Correlation: protocol.Correlation{SessionID: "lead"},
		Path:        "root.go",
		Policy:      "warn",
		Warning:     "lead claim",
	})
	if len(m.children) != 0 {
		t.Fatalf("root PathOverlap created fake child: %#v", m.children)
	}
	if len(m.pathOverlaps) != 1 || m.pathOverlaps[0].path != "root.go" {
		t.Fatalf("root pathOverlaps = %#v", m.pathOverlaps)
	}
	m.vizFocusID = "lead"
	snap := m.visualizerStateSnapshot()
	if len(snap.PathOverlaps) != 1 || snap.PathOverlaps[0].Path != "root.go" {
		t.Fatalf("root snapshot overlaps = %#v", snap.PathOverlaps)
	}
}

func TestChildCompletedStoresVerification(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "lead"
	m.children = []childActivity{{sessionID: "c1", status: "running"}}
	rep := &protocol.VerificationReport{
		Claimed: true, Verified: true, Passed: true, Summary: "gates ok",
	}
	m.onChildCompleted(protocol.ChildCompleted{
		Correlation:  protocol.Correlation{SessionID: "c1", ParentSessionID: "lead"},
		Status:       protocol.ChildStatusCompleted,
		Verification: rep,
	})
	ch := m.children[0]
	if ch.verification == nil {
		t.Fatal("verification not stored on child")
	}
	if !ch.verification.claimed || !ch.verification.verified || !ch.verification.passed {
		t.Fatalf("verification = %#v", ch.verification)
	}
	if ch.verification.summary != "gates ok" {
		t.Fatalf("summary = %q", ch.verification.summary)
	}
	// No report → unknown stays nil (do not invent verified success).
	m2, _ := newAppTestModel(nil, nil)
	m2.children = []childActivity{{sessionID: "c2", status: "running"}}
	m2.onChildCompleted(protocol.ChildCompleted{
		Correlation: protocol.Correlation{SessionID: "c2"},
		Status:      protocol.ChildStatusCompleted,
	})
	if m2.children[0].verification != nil {
		t.Fatalf("fabricated verification: %#v", m2.children[0].verification)
	}
}

func TestApplyEventChildCorrelatedObservability(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "lead"
	m.children = []childActivity{{sessionID: "c1", status: "running"}}
	// Child correlation must not drop escalation / path overlap (#922).
	_ = m.applyEvent(protocol.ChildEscalated{
		Correlation: protocol.Correlation{SessionID: "c1", ParentSessionID: "lead", Depth: 1},
		Kind:        "tokens",
		Reason:      "token budget",
		Action:      "interrupted",
	})
	if m.children[0].escalateKind != "tokens" {
		t.Fatalf("escalation dropped: %#v", m.children[0])
	}
	_ = m.applyEvent(protocol.PathOverlap{
		Correlation: protocol.Correlation{SessionID: "c1", ParentSessionID: "lead", Depth: 1},
		Path:        "shared.go",
		Policy:      "block",
		Blocked:     true,
		Warning:     "held by peer",
	})
	if len(m.children[0].pathOverlaps) != 1 || m.children[0].pathOverlaps[0].path != "shared.go" {
		t.Fatalf("path overlap dropped: %#v", m.children[0].pathOverlaps)
	}
	rep := protocol.VerificationReport{Claimed: true, Verified: false, Passed: false}
	_ = m.applyEvent(protocol.VerificationCompleted{
		Correlation: protocol.Correlation{SessionID: "c1", ParentSessionID: "lead", Depth: 1},
		Scope:       protocol.VerificationScopeChild,
		Report:      rep,
	})
	if m.children[0].verification == nil || m.children[0].verification.verified {
		t.Fatalf("child verification = %#v", m.children[0].verification)
	}
}

func TestVisualizerStateSnapshotChildVsRoot(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "lead"
	tokRem := 40
	m.children = []childActivity{{
		sessionID:    "c1",
		agent:        "explore",
		name:         "scout",
		status:       "running",
		objective:    "find leaks",
		lastAction:   "grep TODO",
		blockReason:  "waiting on lock",
		filesTouched: []string{"x.go"},
		budget: &protocol.AgentBudgetView{
			MaxTokens: 200, TokensUsed: 160, TokensRemaining: &tokRem,
		},
		escalateKind:   "stall",
		escalateReason: "idle",
		escalateAction: "signaled",
		pathOverlaps: []childPathOverlap{{
			path: "x.go", policy: "warn", warning: "peer write",
		}},
		verification: &childVerificationSummary{
			claimed: true, verified: false, passed: false, summary: "unit failed",
		},
	}}
	m.usageInput = protocol.KnownTokens(10)
	m.usageOutput = protocol.KnownTokens(5)
	m.usageUsed = protocol.KnownTokens(15)

	// Child selection carries plumbed fields, not status-only.
	m.vizFocusID = "c1"
	childSnap := m.visualizerStateSnapshot()
	if childSnap.Kind != "child" {
		t.Fatalf("kind = %q", childSnap.Kind)
	}
	if childSnap.Objective != "find leaks" || childSnap.LastAction != "grep TODO" {
		t.Fatalf("child obs = %+v", childSnap)
	}
	if childSnap.BlockReason != "waiting on lock" {
		t.Fatalf("blockReason = %q", childSnap.BlockReason)
	}
	if len(childSnap.FilesTouched) != 1 || childSnap.FilesTouched[0] != "x.go" {
		t.Fatalf("files = %#v", childSnap.FilesTouched)
	}
	if childSnap.Budget == nil || childSnap.Budget.TokensUsed != 160 {
		t.Fatalf("budget = %#v", childSnap.Budget)
	}
	if childSnap.EscalateKind != "stall" || childSnap.EscalateAction != "signaled" {
		t.Fatalf("escalate = %q/%q", childSnap.EscalateKind, childSnap.EscalateAction)
	}
	if len(childSnap.PathOverlaps) != 1 || childSnap.PathOverlaps[0].Path != "x.go" {
		t.Fatalf("overlaps = %#v", childSnap.PathOverlaps)
	}
	if childSnap.Verification == nil || childSnap.Verification.Verified || !childSnap.Verification.Claimed {
		t.Fatalf("verification = %#v", childSnap.Verification)
	}
	// Child tokens stay unknown — no fabricated zeros.
	if childSnap.Input.Known || childSnap.Output.Known || childSnap.Used.Known {
		t.Fatalf("child fabricated token known flags: in=%v out=%v used=%v",
			childSnap.Input.Known, childSnap.Output.Known, childSnap.Used.Known)
	}

	// Root selection: usage known; child-only obs fields empty/unknown.
	m.vizFocusID = "lead"
	rootSnap := m.visualizerStateSnapshot()
	if rootSnap.Kind != "root" {
		t.Fatalf("root kind = %q", rootSnap.Kind)
	}
	if !rootSnap.Input.Known || rootSnap.Input.N != 10 {
		t.Fatalf("root input = %#v", rootSnap.Input)
	}
	if rootSnap.Objective != "" || rootSnap.Budget != nil || len(rootSnap.PathOverlaps) != 0 {
		t.Fatalf("root should not invent child obs: %+v", rootSnap)
	}
	// No lastVerification → verification unknown on root.
	if rootSnap.Verification != nil {
		t.Fatalf("root fabricated verification: %#v", rootSnap.Verification)
	}
}

func TestChildrenFromEventsPlumbsObservability(t *testing.T) {
	events := []protocol.Event{
		protocol.ChildStarted{
			Correlation: protocol.Correlation{SessionID: "c1", ParentSessionID: "lead"},
			Agent:       "general",
		},
		protocol.TeamRoster{
			LeadID: "lead",
			Members: []protocol.TeamRosterMember{
				{
					SessionID: "c1", Role: "member", State: "working",
					Objective: "ship fix", LastAction: "edit", FilesTouched: []string{"z.go"},
					Budget: &protocol.AgentBudgetView{MaxToolCalls: 20, ToolCalls: 3},
				},
			},
		},
		protocol.PathOverlap{
			Correlation: protocol.Correlation{SessionID: "c1"},
			Path:        "z.go", Policy: "warn", Warning: "overlap",
		},
		protocol.ChildEscalated{
			Correlation: protocol.Correlation{SessionID: "c1"},
			Kind:        "loop", Reason: "repeat", Action: "finalizing",
		},
		protocol.ChildCompleted{
			Correlation: protocol.Correlation{SessionID: "c1"},
			Status:      protocol.ChildStatusCompleted,
			Verification: &protocol.VerificationReport{
				Claimed: true, Verified: true, Passed: true,
			},
		},
	}
	got := childrenFromEvents(events)
	if len(got) != 1 {
		t.Fatalf("len = %d: %#v", len(got), got)
	}
	ch := got[0]
	if ch.objective != "ship fix" || ch.lastAction != "edit" {
		t.Fatalf("obs = %#v", ch)
	}
	if ch.budget == nil || ch.budget.ToolCalls != 3 {
		t.Fatalf("budget = %#v", ch.budget)
	}
	if len(ch.pathOverlaps) != 1 || ch.pathOverlaps[0].path != "z.go" {
		t.Fatalf("overlaps = %#v", ch.pathOverlaps)
	}
	if ch.escalateKind != "loop" {
		t.Fatalf("escalate = %q", ch.escalateKind)
	}
	if ch.verification == nil || !ch.verification.passed {
		t.Fatalf("verification = %#v", ch.verification)
	}
	// Resume marks incomplete running as canceled — completed stays completed.
	if ch.status != string(protocol.ChildStatusCompleted) {
		t.Fatalf("status = %q", ch.status)
	}
}
