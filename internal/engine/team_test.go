package engine

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tool"
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
	// Drain team.roster snapshot emitted by finishChild.
	select {
	case ev := <-e.Events():
		if _, ok := ev.(protocol.TeamRoster); !ok {
			t.Fatalf("expected TeamRoster, got %T", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for TeamRoster")
	}
	m, ok := e.team.Member("A")
	if !ok || m.State != protocol.TeamMemberCanceled {
		t.Fatalf("after cancel finish: %+v ok=%v", m, ok)
	}
	if m.Summary != "canceled" {
		t.Fatalf("summary = %q", m.Summary)
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
	select {
	case <-e.Events():
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for second TeamRoster")
	}
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
		{protocol.ChildStatusBlocked, protocol.TeamMemberBlocked},
		{protocol.ChildStatus("nope"), protocol.TeamMemberFailed},
	}
	for _, tc := range cases {
		if got := protocol.TeamMemberStateFromChild(tc.in); got != tc.want {
			t.Errorf("TeamMemberStateFromChild(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestAgentRosterChildSeesLeadAndSiblings(t *testing.T) {
	lead := New(Options{SessionID: "L", InitialAgent: "build", Agents: []Agent{{Name: "build"}}})
	tm := lead.team
	if tm == nil {
		t.Fatal("nil team")
	}
	if !tm.Enroll(TeamMember{SessionID: "A", ParentSessionID: "L", Persona: "explore", Depth: 1, StartedAt: time.Now()}) {
		t.Fatal("enroll A")
	}
	if !tm.Enroll(TeamMember{SessionID: "B", ParentSessionID: "L", Persona: "general", Depth: 1, StartedAt: time.Now()}) {
		t.Fatal("enroll B")
	}
	tm.SetTerminal("B", protocol.TeamMemberCompleted, "b done")

	// Child engine A shares the lead team pointer.
	childA := New(Options{
		SessionID:       "A",
		ParentSessionID: "L",
		Depth:           1,
		MaxChildDepth:   1,
		Team:            tm,
		InitialAgent:    "explore",
		Agents:          []Agent{{Name: "explore"}, {Name: "build"}},
	})
	res, err := childA.agentRoster(context.Background(), tool.AgentRosterRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if res.LeadID != "L" || len(res.Members) != 3 {
		t.Fatalf("roster = %+v", res)
	}
	var sawLead, sawSelf, sawSibling bool
	for _, m := range res.Members {
		switch m.SessionID {
		case "L":
			sawLead = m.Role == "lead" && !m.IsSelf && m.State == "working"
		case "A":
			sawSelf = m.IsSelf && m.Role == "member" && m.Agent == "explore"
		case "B":
			sawSibling = !m.IsSelf && m.State == "completed" && m.TerminalSummary == "b done"
		}
	}
	if !sawLead || !sawSelf || !sawSibling {
		t.Fatalf("lead=%v self=%v sibling=%v members=%+v", sawLead, sawSelf, sawSibling, res.Members)
	}
}

func TestAgentRosterNilTeam(t *testing.T) {
	e := &Engine{opts: Options{SessionID: "x"}}
	if _, err := e.agentRoster(context.Background(), tool.AgentRosterRequest{}); err == nil {
		t.Fatal("expected error for nil team")
	}
}

func TestTeamStateToTaskVocab(t *testing.T) {
	cases := []struct {
		in   protocol.TeamMemberState
		want string
	}{
		{protocol.TeamMemberRunning, "working"},
		{protocol.TeamMemberCompleted, "completed"},
		{protocol.TeamMemberFailed, "failed"},
		{protocol.TeamMemberCanceled, "canceled"},
		{"", "unknown"},
	}
	for _, tc := range cases {
		if got := teamStateToTaskVocab(tc.in); got != tc.want {
			t.Errorf("teamStateToTaskVocab(%q) = %q, want %q", tc.in, got, tc.want)
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

func TestValidateMemberName(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", "", false},
		{"  explorer  ", "explorer", false},
		{"a_b-1", "a_b-1", false},
		{"has space", "", true},
		{"bad!", "", true},
		{strings.Repeat("x", maxTeamMemberNameLen+1), "", true},
		{strings.Repeat("x", maxTeamMemberNameLen), strings.Repeat("x", maxTeamMemberNameLen), false},
	}
	for _, tc := range cases {
		got, err := ValidateMemberName(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ValidateMemberName(%q) err=nil, want error", tc.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("ValidateMemberName(%q) err=%v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ValidateMemberName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestTeamNameAliasUniqueAndResolve(t *testing.T) {
	tm := NewTeam("L", "build")
	if !tm.Enroll(TeamMember{SessionID: "A", ParentSessionID: "L", Name: "explorer", Depth: 1}) {
		t.Fatal("enroll A explorer")
	}
	if tm.Enroll(TeamMember{SessionID: "B", ParentSessionID: "L", Name: "explorer", Depth: 1}) {
		t.Fatal("duplicate name must be rejected")
	}
	if tm.Contains("B") {
		t.Fatal("B must not enroll after name collision")
	}
	if !tm.Enroll(TeamMember{SessionID: "B", ParentSessionID: "L", Name: "implementer", Depth: 1}) {
		t.Fatal("enroll B implementer")
	}

	id, ok := tm.Resolve("explorer")
	if !ok || id != "A" {
		t.Fatalf("Resolve(explorer) = %q ok=%v, want A", id, ok)
	}
	id, ok = tm.Resolve("A")
	if !ok || id != "A" {
		t.Fatalf("Resolve(session) = %q ok=%v", id, ok)
	}
	// Session id wins over a name that collides with another member's id.
	if !tm.Enroll(TeamMember{SessionID: "C", ParentSessionID: "L", Name: "B", Depth: 1}) {
		t.Fatal("enroll C named B (name equals another session id)")
	}
	id, ok = tm.Resolve("B")
	if !ok || id != "B" {
		t.Fatalf("Resolve prefers session id: got %q ok=%v", id, ok)
	}
	owner, ok := tm.NameOwner("implementer")
	if !ok || owner != "B" {
		t.Fatalf("NameOwner(implementer) = %q ok=%v", owner, ok)
	}
	// Re-enroll same id with same name is fine.
	if !tm.Enroll(TeamMember{SessionID: "A", ParentSessionID: "L", Name: "explorer", Persona: "explore", Depth: 1}) {
		t.Fatal("re-enroll A with same name")
	}
}

// TestTeamResolveSessionIDPrefix covers #650: agent_message to= short session
// id prefixes (tool shortID / UI fragments) must resolve when unique.
func TestTeamResolveSessionIDPrefix(t *testing.T) {
	const (
		leadID = "1f0d0c5d-lead-0000-0000-000000000001"
		aID    = "c0a9b0d4-aaaa-bbbb-cccc-ddddeeeeffff"
		bID    = "a1b2c3d4-1111-2222-3333-444455556666"
	)
	tm := NewTeam(leadID, "build")
	if !tm.Enroll(TeamMember{SessionID: aID, ParentSessionID: leadID, Name: "worker-a", Depth: 1}) {
		t.Fatal("enroll worker-a")
	}
	if !tm.Enroll(TeamMember{SessionID: bID, ParentSessionID: leadID, Name: "worker-b", Depth: 1}) {
		t.Fatal("enroll worker-b")
	}

	// Exact name still works.
	id, ok := tm.ResolveAddress("worker-a")
	if !ok || id != aID {
		t.Fatalf("name resolve = %q ok=%v", id, ok)
	}

	// Unique 8-char prefix (tool shortID of aID).
	id, ok = tm.ResolveAddress("c0a9b0d4")
	if !ok || id != aID {
		t.Fatalf("unique prefix resolve = %q ok=%v, want %s", id, ok, aID)
	}

	// Longer unique prefix.
	id, ok = tm.ResolveAddress("c0a9b0d4-aaaa")
	if !ok || id != aID {
		t.Fatalf("longer prefix = %q ok=%v", id, ok)
	}

	// Too short: must not prefix-match (avoids accidental collisions).
	if _, ok := tm.ResolveAddress("c0a9b0"); ok {
		t.Fatal("prefix shorter than minSessionIDPrefixLen must not resolve")
	}

	// Unknown prefix.
	_, detail, ok := tm.ResolveAddressDetail("deadbeef")
	if ok || detail != "recipient is not on this team" {
		t.Fatalf("unknown prefix detail = %q ok=%v", detail, ok)
	}

	// Ambiguous prefix when two members share the same head.
	tm2 := NewTeam("L", "build")
	if !tm2.Enroll(TeamMember{SessionID: "abcdef01-one", ParentSessionID: "L", Depth: 1}) {
		t.Fatal("enroll one")
	}
	if !tm2.Enroll(TeamMember{SessionID: "abcdef01-two", ParentSessionID: "L", Depth: 1}) {
		t.Fatal("enroll two")
	}
	_, detail, ok = tm2.ResolveAddressDetail("abcdef01")
	if ok || detail != "session id prefix is ambiguous" {
		t.Fatalf("ambiguous prefix = detail %q ok=%v", detail, ok)
	}

	// Exact full id still preferred over being a prefix of another id.
	id, ok = tm2.ResolveAddress("abcdef01-one")
	if !ok || id != "abcdef01-one" {
		t.Fatalf("exact id = %q ok=%v", id, ok)
	}
}
