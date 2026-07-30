package engine

import (
	"fmt"
	"sync"
	"testing"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

func TestMailboxEnqueuePeekTakeFIFO(t *testing.T) {
	m := &Mailbox{}
	if !m.Enqueue(MailboxMessage{From: "a", To: "b", Body: "one"}) {
		t.Fatal("enqueue one")
	}
	if !m.Enqueue(MailboxMessage{From: "a", To: "b", Body: "two"}) {
		t.Fatal("enqueue two")
	}
	if m.Len() != 2 {
		t.Fatalf("Len = %d, want 2", m.Len())
	}
	peek := m.PeekUnread()
	if len(peek) != 2 || peek[0].Body != "one" || peek[1].Body != "two" {
		t.Fatalf("peek = %#v", peek)
	}
	if m.Len() != 2 {
		t.Fatal("peek must not consume")
	}
	got := m.TakePending()
	if len(got) != 2 || got[0].Body != "one" || got[1].Body != "two" {
		t.Fatalf("take = %#v", got)
	}
	if m.Len() != 0 || m.TakePending() != nil {
		t.Fatal("take should drain")
	}
}

func TestMailboxRejectsEmptyBody(t *testing.T) {
	m := &Mailbox{}
	if m.Enqueue(MailboxMessage{Body: "  "}) {
		t.Fatal("whitespace body should reject")
	}
	if m.Enqueue(MailboxMessage{Body: ""}) {
		t.Fatal("empty body should reject")
	}
}

func TestMailboxCapDropsOldest(t *testing.T) {
	m := &Mailbox{}
	for i := 0; i < maxMailboxPending+3; i++ {
		if !m.Enqueue(MailboxMessage{Body: fmt.Sprintf("m%d", i)}) {
			t.Fatalf("enqueue %d", i)
		}
	}
	if m.Len() != maxMailboxPending {
		t.Fatalf("Len = %d, want %d", m.Len(), maxMailboxPending)
	}
	if m.Dropped() != 3 {
		t.Fatalf("Dropped = %d, want 3", m.Dropped())
	}
	got := m.TakePending()
	if got[0].Body != "m3" {
		t.Fatalf("oldest kept = %q, want m3", got[0].Body)
	}
	if got[len(got)-1].Body != fmt.Sprintf("m%d", maxMailboxPending+2) {
		t.Fatalf("newest = %q", got[len(got)-1].Body)
	}
}

func TestMailboxConcurrentSendersPreserveCount(t *testing.T) {
	m := &Mailbox{}
	// Stay under maxMailboxPending so the test asserts uniqueness, not drop policy.
	const n = 40
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			if !m.Enqueue(MailboxMessage{Body: fmt.Sprintf("c%d", i)}) {
				t.Errorf("enqueue %d failed", i)
			}
		}()
	}
	wg.Wait()
	if m.Len() != n {
		t.Fatalf("Len = %d, want %d", m.Len(), n)
	}
	got := m.TakePending()
	if len(got) != n {
		t.Fatalf("take len = %d", len(got))
	}
	// All bodies unique — no corruption.
	seen := make(map[string]bool, n)
	for _, msg := range got {
		if seen[msg.Body] {
			t.Fatalf("duplicate body %q", msg.Body)
		}
		seen[msg.Body] = true
	}
}

func TestFormatMailboxNotice(t *testing.T) {
	s := formatMailboxNotice(MailboxMessage{From: "abcdefghij", Body: "hello"})
	if s != "[agent.message from=abcdefgh]\nhello" {
		t.Fatalf("format = %q", s)
	}
}

func TestTeamDeliverRejects(t *testing.T) {
	tm := NewTeam("L", "build")
	if tm == nil {
		t.Fatal("nil team")
	}
	_ = tm.Enroll(TeamMember{SessionID: "A", ParentSessionID: "L", Depth: 1})
	_ = tm.Enroll(TeamMember{SessionID: "B", ParentSessionID: "L", Depth: 1})

	// No live engines attached.
	st := tm.Deliver("A", "B", "hi")
	if st.Status != "rejected" || st.Detail == "" {
		t.Fatalf("no live = %#v", st)
	}

	st = tm.Deliver("A", "A", "hi")
	if st.Status != "rejected" {
		t.Fatalf("self = %#v", st)
	}

	st = tm.Deliver("X", "B", "hi")
	if st.Status != "rejected" {
		t.Fatalf("foreign from = %#v", st)
	}

	st = tm.Deliver("A", "Z", "hi")
	if st.Status != "rejected" {
		t.Fatalf("foreign to = %#v", st)
	}

	st = tm.Deliver("A", "B", "")
	if st.Status != "rejected" {
		t.Fatalf("empty body = %#v", st)
	}

	tm.SetState("B", protocol.TeamMemberCompleted)
	// Attach a dummy so we hit state check before live (state is checked first).
	st = tm.Deliver("A", "B", "hi")
	if st.Status != "rejected" || st.Detail == "" {
		t.Fatalf("completed = %#v", st)
	}
}

func TestTeamDeliverToLiveIdleAccepted(t *testing.T) {
	lead := New(Options{SessionID: "L", InitialAgent: "build", Agents: []Agent{{Name: "build"}}})
	child := New(Options{
		SessionID:       "A",
		ParentSessionID: "L",
		Depth:           1,
		Team:            lead.Team(),
		Agents:          []Agent{{Name: "build"}},
	})
	tm := lead.Team()
	if !tm.Enroll(TeamMember{SessionID: "A", ParentSessionID: "L", Depth: 1}) {
		t.Fatal("enroll")
	}
	tm.AttachMailbox(lead)
	tm.AttachMailbox(child)
	t.Cleanup(func() {
		tm.DetachMailbox("L")
		tm.DetachMailbox("A")
	})

	st := tm.Deliver("L", "A", "peer hello")
	if st.Status != "accepted" {
		t.Fatalf("status = %#v", st)
	}
	if st.MessageID == "" {
		t.Fatal("missing message id")
	}
	box := child.Mailbox()
	if box == nil || box.Len() != 1 {
		t.Fatalf("mailbox len = %v", box)
	}
	unread := box.PeekUnread()
	if unread[0].Body != "peer hello" || unread[0].From != "L" || unread[0].To != "A" {
		t.Fatalf("unread = %#v", unread)
	}
	if unread[0].TeamID != "L" {
		t.Fatalf("team id = %q", unread[0].TeamID)
	}
}
