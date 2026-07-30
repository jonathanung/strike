package engine

import (
	"slices"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

func TestTeamLeadAndChildrenRoster(t *testing.T) {
	tm := NewTeam("L", "build")
	if tm == nil {
		t.Fatal("NewTeam returned nil")
	}
	if tm.LeadID() != "L" {
		t.Fatalf("LeadID = %q", tm.LeadID())
	}
	if !tm.Enroll(TeamMember{SessionID: "A", ParentSessionID: "L", Persona: "explore", Depth: 1}) {
		t.Fatal("enroll A")
	}
	if !tm.Enroll(TeamMember{SessionID: "B", ParentSessionID: "L", Persona: "general", Depth: 1}) {
		t.Fatal("enroll B")
	}

	ids := rosterIDs(tm)
	want := []string{"L", "A", "B"}
	if !slices.Equal(ids, want) {
		t.Fatalf("roster = %v, want %v", ids, want)
	}
	for _, id := range want {
		if !tm.Contains(id) {
			t.Errorf("Contains(%q) = false", id)
		}
	}
}

func TestTeamUnrelatedRootExcluded(t *testing.T) {
	lead := NewTeam("L", "build")
	other := NewTeam("R", "build")
	if !lead.Enroll(TeamMember{SessionID: "A", ParentSessionID: "L", Depth: 1}) {
		t.Fatal("enroll A under L")
	}
	if !other.Enroll(TeamMember{SessionID: "X", ParentSessionID: "R", Depth: 1}) {
		t.Fatal("enroll X under R")
	}
	if lead.Contains("X") {
		t.Fatal("unrelated child X must not be on L's team")
	}
	if other.Contains("A") {
		t.Fatal("L's child A must not be on R's team")
	}
	// Parent not on this team → reject
	if lead.Enroll(TeamMember{SessionID: "Y", ParentSessionID: "R", Depth: 1}) {
		t.Fatal("enroll under foreign parent must fail")
	}
	if lead.Contains("Y") {
		t.Fatal("Y must not appear after rejected enroll")
	}
}

func TestTeamNestedDescendantInLeadTree(t *testing.T) {
	// Nested policy: grandchildren enroll when parent is already a member.
	tm := NewTeam("L", "orchestrator")
	if !tm.Enroll(TeamMember{SessionID: "A", ParentSessionID: "L", Persona: "general", Depth: 1}) {
		t.Fatal("enroll A")
	}
	if !tm.Enroll(TeamMember{SessionID: "G", ParentSessionID: "A", Persona: "explore", Depth: 2}) {
		t.Fatal("enroll nested G under A")
	}
	ids := rosterIDs(tm)
	want := []string{"L", "A", "G"}
	if !slices.Equal(ids, want) {
		t.Fatalf("roster = %v, want %v (nested in-team via lead tree)", ids, want)
	}
	g, ok := tm.Member("G")
	if !ok || g.ParentSessionID != "A" || g.Depth != 2 {
		t.Fatalf("Member G = %+v ok=%v", g, ok)
	}
}

func TestTeamMembershipSpawnCompleteCancel(t *testing.T) {
	tm := NewTeam("L", "build")
	if !tm.Enroll(TeamMember{SessionID: "A", ParentSessionID: "L", Depth: 1}) {
		t.Fatal("enroll A")
	}
	m, ok := tm.Member("A")
	if !ok || m.State != protocol.TeamMemberRunning {
		t.Fatalf("after spawn: %+v ok=%v", m, ok)
	}

	if !tm.SetState("A", protocol.TeamMemberCompleted) {
		t.Fatal("complete A")
	}
	m, _ = tm.Member("A")
	if m.State != protocol.TeamMemberCompleted {
		t.Fatalf("after complete: state=%q", m.State)
	}
	// Terminal remains listable
	if !tm.Contains("A") {
		t.Fatal("terminal A must remain listable")
	}

	if !tm.Enroll(TeamMember{SessionID: "B", ParentSessionID: "L", Depth: 1}) {
		t.Fatal("enroll B")
	}
	if !tm.SetState("B", protocol.TeamMemberCanceled) {
		t.Fatal("cancel B")
	}
	m, _ = tm.Member("B")
	if m.State != protocol.TeamMemberCanceled {
		t.Fatalf("after cancel: state=%q", m.State)
	}

	if !tm.Enroll(TeamMember{SessionID: "C", ParentSessionID: "L", Depth: 1}) {
		t.Fatal("enroll C")
	}
	if !tm.SetState("C", protocol.TeamMemberStateFromChild(protocol.ChildStatusFailed)) {
		t.Fatal("fail C")
	}
	m, _ = tm.Member("C")
	if m.State != protocol.TeamMemberFailed {
		t.Fatalf("after fail: state=%q", m.State)
	}

	// Still {L,A,B,C}
	if tm.Len() != 4 {
		t.Fatalf("Len = %d, want 4", tm.Len())
	}
}

// TestFinishChildUpdatesTeamState covers spawn→terminal wiring on finishChild
// without a full multi-engine turn (cancel/fail/complete).
func TestFinishChildUpdatesTeamState(t *testing.T) {
	e := New(Options{SessionID: "L", Agents: []Agent{{Name: "build"}}})
	if e.team == nil {
		t.Fatal("nil team")
	}
	h := &childHandle{id: "A", agent: "explore", startedAt: time.Now()}
	e.childMu.Lock()
	e.children["A"] = h
	e.childMu.Unlock()
	if !e.team.Enroll(TeamMember{SessionID: "A", ParentSessionID: "L", Persona: "explore", Depth: 1}) {
		t.Fatal("enroll")
	}

	e.finishChild(h, protocol.ChildCompleted{
		Correlation: protocol.Correlation{SessionID: "A", ParentSessionID: "L", Depth: 1},
		Status:      protocol.ChildStatusCanceled,
		Summary:     "canceled",
	})
	m, ok := e.team.Member("A")
	if !ok || m.State != protocol.TeamMemberCanceled {
		t.Fatalf("after cancel finish: %+v ok=%v", m, ok)
	}
	if e.children["A"] != nil {
		t.Fatal("live handle should be removed")
	}
	if e.childHistory["A"] == nil {
		t.Fatal("history missing")
	}

	h2 := &childHandle{id: "B", agent: "general", startedAt: time.Now()}
	_ = e.team.Enroll(TeamMember{SessionID: "B", ParentSessionID: "L", Depth: 1})
	e.finishChild(h2, protocol.ChildCompleted{
		Status: protocol.ChildStatusFailed,
	})
	m, _ = e.team.Member("B")
	if m.State != protocol.TeamMemberFailed {
		t.Fatalf("fail state = %q", m.State)
	}
}

func TestTeamDissolve(t *testing.T) {
	tm := NewTeam("L", "build")
	_ = tm.Enroll(TeamMember{SessionID: "A", ParentSessionID: "L", Depth: 1})
	tm.Dissolve()
	if tm.Len() != 0 {
		t.Fatalf("Len after dissolve = %d", tm.Len())
	}
	if tm.Contains("L") || tm.Contains("A") {
		t.Fatal("dissolve must clear membership")
	}
	if got := tm.Roster(); len(got) != 0 {
		t.Fatalf("Roster after dissolve = %v", got)
	}
	// LeadID identity retained for diagnostics; enroll still uses lead id key
	// but members map is empty so parent check fails until reconstructed.
	if tm.LeadID() != "L" {
		t.Fatalf("LeadID after dissolve = %q", tm.LeadID())
	}
	if tm.Enroll(TeamMember{SessionID: "B", ParentSessionID: "L", Depth: 1}) {
		t.Fatal("enroll after dissolve should fail (lead not in members)")
	}
}

func TestNewTeamEmpty(t *testing.T) {
	if NewTeam("", "build") != nil {
		t.Fatal("empty lead must yield nil team")
	}
	if NewTeam("  ", "") != nil {
		t.Fatal("whitespace lead must yield nil team")
	}
}

func TestTeamMemberStateFromChild(t *testing.T) {
	cases := []struct {
		in   protocol.ChildStatus
		want protocol.TeamMemberState
	}{
		{protocol.ChildStatusCompleted, protocol.TeamMemberCompleted},
		{protocol.ChildStatusFailed, protocol.TeamMemberFailed},
		{protocol.ChildStatusCanceled, protocol.TeamMemberCanceled},
		{protocol.ChildStatus("nope"), protocol.TeamMemberFailed},
	}
	for _, tc := range cases {
		if got := protocol.TeamMemberStateFromChild(tc.in); got != tc.want {
			t.Errorf("TeamMemberStateFromChild(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func rosterIDs(tm *Team) []string {
	roster := tm.Roster()
	ids := make([]string, len(roster))
	for i, m := range roster {
		ids[i] = m.SessionID
	}
	return ids
}
