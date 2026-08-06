package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tool"
)

func TestMailboxUrgencyOrdering(t *testing.T) {
	m := &Mailbox{}
	if !m.Enqueue(MailboxMessage{Body: "normal", Urgency: protocol.AgentUrgencyNormal}) {
		t.Fatal("enqueue normal")
	}
	if !m.Enqueue(MailboxMessage{Body: "blocker", Urgency: protocol.AgentUrgencyBlocker}) {
		t.Fatal("enqueue blocker")
	}
	if !m.Enqueue(MailboxMessage{Body: "high", Urgency: protocol.AgentUrgencyHigh}) {
		t.Fatal("enqueue high")
	}
	got := m.TakePending()
	if len(got) != 3 {
		t.Fatalf("len = %d", len(got))
	}
	if got[0].Body != "blocker" || got[1].Body != "high" || got[2].Body != "normal" {
		t.Fatalf("order = %q, %q, %q", got[0].Body, got[1].Body, got[2].Body)
	}
}

func TestThreadIsolationAcrossTasks(t *testing.T) {
	lead := New(Options{SessionID: "L", InitialAgent: "build", Agents: []Agent{{Name: "build"}}})
	child := New(Options{
		SessionID: "A", ParentSessionID: "L", Depth: 1,
		Team: lead.Team(), Agents: []Agent{{Name: "build"}},
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
		tm.Dissolve()
	})

	st1 := tm.DeliverMessage(MailboxMessage{From: "L", To: "A", Body: "about t1", TaskID: "t1"})
	st2 := tm.DeliverMessage(MailboxMessage{From: "L", To: "A", Body: "about t2", TaskID: "t2"})
	if st1.Status != "accepted" || st2.Status != "accepted" {
		t.Fatalf("deliver = %#v %#v", st1, st2)
	}
	th1 := tm.Thread("t1")
	th2 := tm.Thread("t2")
	if len(th1) != 1 || th1[0].Body != "about t1" {
		t.Fatalf("t1 thread = %#v", th1)
	}
	if len(th2) != 1 || th2[0].Body != "about t2" {
		t.Fatalf("t2 thread = %#v", th2)
	}
	if len(tm.Thread("missing")) != 0 {
		t.Fatal("missing task should be empty")
	}
}

func TestAckTimeoutEmitsEventAndEscalates(t *testing.T) {
	lead := New(Options{SessionID: "L-ack", InitialAgent: "build", Agents: []Agent{{Name: "build"}}})
	child := New(Options{
		SessionID: "A-ack", ParentSessionID: "L-ack", Depth: 1,
		Team: lead.Team(), Agents: []Agent{{Name: "build"}},
	})
	tm := lead.Team()
	if !tm.Enroll(TeamMember{SessionID: "A-ack", ParentSessionID: "L-ack", Depth: 1}) {
		t.Fatal("enroll")
	}
	tm.AttachMailbox(lead)
	tm.AttachMailbox(child)
	t.Cleanup(func() {
		tm.DetachMailbox("L-ack")
		tm.DetachMailbox("A-ack")
		tm.Dissolve()
	})

	// Drain lead events so timeout emit does not block.
	done := make(chan struct{})
	var timeouts []protocol.AgentContractTimeout
	go func() {
		defer close(done)
		for ev := range lead.Events() {
			if to, ok := ev.(protocol.AgentContractTimeout); ok {
				timeouts = append(timeouts, to)
			}
		}
	}()

	st := tm.DeliverMessage(MailboxMessage{
		From:       "L-ack",
		To:         "A-ack",
		Body:       "please confirm",
		TaskID:     "d-ack",
		Urgency:    protocol.AgentUrgencyHigh,
		Kind:       protocol.AgentMessageKindRequest,
		RequireAck: true,
		EscalateTo: "L-ack",
		Deadline:   time.Now().UTC().Add(30 * time.Millisecond),
	})
	if st.Status != "accepted" || st.MessageID == "" {
		t.Fatalf("deliver = %#v", st)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := tm.PendingAck(st.MessageID); !ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, ok := tm.PendingAck(st.MessageID); ok {
		t.Fatal("pending ack still present after TTL")
	}

	// Escalation should land on lead mailbox (from == escalate target is skipped
	// when From==EscalateTo; here From is L-ack and EscalateTo is L-ack so no
	// self-escalation mail — but timeout event must still fire on sender).
	// Re-send with child as sender so escalation hits lead.
	st2 := tm.DeliverMessage(MailboxMessage{
		From:       "A-ack",
		To:         "L-ack",
		Body:       "need info",
		TaskID:     "d-ack2",
		Kind:       protocol.AgentMessageKindRequest,
		RequireAck: true,
		EscalateTo: "L-ack", // same as To — still timeout event on A-ack
		Deadline:   time.Now().UTC().Add(30 * time.Millisecond),
	})
	if st2.Status != "accepted" {
		t.Fatalf("st2 = %#v", st2)
	}
	// Wait for second timeout
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := tm.PendingAck(st2.MessageID); !ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Child-as-sender timeout with escalate to lead (different from To).
	other := New(Options{
		SessionID: "B-ack", ParentSessionID: "L-ack", Depth: 1,
		Team: lead.Team(), Agents: []Agent{{Name: "build"}},
	})
	if !tm.Enroll(TeamMember{SessionID: "B-ack", ParentSessionID: "L-ack", Depth: 1}) {
		t.Fatal("enroll B")
	}
	tm.AttachMailbox(other)
	t.Cleanup(func() { tm.DetachMailbox("B-ack") })

	// Drain B events
	go func() {
		for range other.Events() {
		}
	}()

	beforeLead := lead.Mailbox().Len()
	st3 := tm.DeliverMessage(MailboxMessage{
		From:       "A-ack",
		To:         "B-ack",
		Body:       "peer request",
		TaskID:     "d-peer",
		Kind:       protocol.AgentMessageKindRequest,
		RequireAck: true,
		EscalateTo: "L-ack",
		Deadline:   time.Now().UTC().Add(40 * time.Millisecond),
	})
	if st3.Status != "accepted" {
		t.Fatalf("st3 = %#v", st3)
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if lead.Mailbox().Len() > beforeLead {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if lead.Mailbox().Len() <= beforeLead {
		t.Fatal("expected escalation message on lead mailbox")
	}
	esc := lead.Mailbox().PeekUnread()
	foundEsc := false
	for _, m := range esc {
		if m.Kind == protocol.AgentMessageKindEscalation && m.InReplyTo == st3.MessageID {
			foundEsc = true
			if m.TaskID != "d-peer" {
				t.Fatalf("escalation task = %q", m.TaskID)
			}
		}
	}
	if !foundEsc {
		t.Fatalf("escalation not found in %#v", esc)
	}

	// Thread should record timeout entries for d-peer.
	th := tm.Thread("d-peer")
	if len(th) < 2 {
		t.Fatalf("thread = %#v, want request + timeout", th)
	}

	// Close lead events consumer.
	lead.closeEvents()
	<-done
	if len(timeouts) < 1 {
		t.Fatalf("expected at least one AgentContractTimeout, got %d", len(timeouts))
	}
}

func TestAckSettlesPendingAndDelivers(t *testing.T) {
	lead := New(Options{SessionID: "L-ok", InitialAgent: "build", Agents: []Agent{{Name: "build"}}})
	child := New(Options{
		SessionID: "A-ok", ParentSessionID: "L-ok", Depth: 1,
		Team: lead.Team(), Agents: []Agent{{Name: "build"}},
	})
	tm := lead.Team()
	if !tm.Enroll(TeamMember{SessionID: "A-ok", ParentSessionID: "L-ok", Depth: 1}) {
		t.Fatal("enroll")
	}
	tm.AttachMailbox(lead)
	tm.AttachMailbox(child)
	t.Cleanup(func() {
		tm.DetachMailbox("L-ok")
		tm.DetachMailbox("A-ok")
		tm.Dissolve()
	})

	st := tm.DeliverMessage(MailboxMessage{
		From:       "L-ok",
		To:         "A-ok",
		Body:       "need ack",
		TaskID:     "t-ack",
		Kind:       protocol.AgentMessageKindRequest,
		RequireAck: true,
		Deadline:   time.Now().UTC().Add(5 * time.Second),
	})
	if st.Status != "accepted" {
		t.Fatalf("deliver = %#v", st)
	}
	if _, ok := tm.PendingAck(st.MessageID); !ok {
		t.Fatal("expected pending ack")
	}

	// Wrong recipient cannot ack.
	if _, ok := tm.AckMessage(st.MessageID, "L-ok"); ok {
		t.Fatal("sender should not ack own message")
	}

	res, err := child.agentMessage(context.Background(), tool.AgentMessageRequest{
		Kind:      protocol.AgentMessageKindAck,
		InReplyTo: st.MessageID,
		Body:      "got it",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "accepted" || res.AckStatus != "acked" {
		t.Fatalf("ack res = %+v", res)
	}
	if _, ok := tm.PendingAck(st.MessageID); ok {
		t.Fatal("pending should be cleared")
	}
	// Ack delivered to original sender (lead).
	if lead.Mailbox().Len() < 1 {
		t.Fatal("lead should receive ack")
	}
	ackMail := lead.Mailbox().PeekUnread()
	found := false
	for _, m := range ackMail {
		if m.Kind == protocol.AgentMessageKindAck && m.InReplyTo == st.MessageID {
			found = true
			if m.Body != "got it" {
				t.Fatalf("ack body = %q", m.Body)
			}
		}
	}
	if !found {
		t.Fatalf("ack mail missing: %#v", ackMail)
	}
	th := tm.Thread("t-ack")
	if len(th) < 2 {
		t.Fatalf("thread = %#v", th)
	}
	// Original should show acked status.
	if th[0].AckStatus != "acked" {
		t.Fatalf("orig ack status = %q", th[0].AckStatus)
	}
}

func TestAgentThreadPermissionBoundary(t *testing.T) {
	// Team A
	leadA := New(Options{SessionID: "root-A", InitialAgent: "build", Agents: []Agent{{Name: "build"}}})
	childA := New(Options{
		SessionID: "child-A", ParentSessionID: "root-A", Depth: 1,
		Team: leadA.Team(), Agents: []Agent{{Name: "build"}},
	})
	tmA := leadA.Team()
	_ = tmA.Enroll(TeamMember{SessionID: "child-A", ParentSessionID: "root-A", Depth: 1})
	tmA.AttachMailbox(leadA)
	tmA.AttachMailbox(childA)

	// Team B (foreign)
	leadB := New(Options{SessionID: "root-B", InitialAgent: "build", Agents: []Agent{{Name: "build"}}})
	tmB := leadB.Team()
	tmB.AttachMailbox(leadB)

	t.Cleanup(func() {
		tmA.DetachMailbox("root-A")
		tmA.DetachMailbox("child-A")
		tmA.Dissolve()
		tmB.DetachMailbox("root-B")
		tmB.Dissolve()
	})

	st := tmA.DeliverMessage(MailboxMessage{
		From: "root-A", To: "child-A", Body: "secret thread", TaskID: "secret-1",
	})
	if st.Status != "accepted" {
		t.Fatalf("deliver = %#v", st)
	}

	// In-team read works.
	res, err := childA.agentThread(context.Background(), tool.AgentThreadRequest{TaskID: "secret-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Messages) != 1 || res.Messages[0].Body != "secret thread" {
		t.Fatalf("in-team thread = %+v", res)
	}

	// Foreign team has empty thread for same id (isolation).
	resB, err := leadB.agentThread(context.Background(), tool.AgentThreadRequest{TaskID: "secret-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resB.Messages) != 0 {
		t.Fatalf("cross-team leak: %+v", resB)
	}

	// Cross-team deliver rejected.
	if st := tmA.DeliverMessage(MailboxMessage{
		From: "root-A", To: "root-B", Body: "nope", TaskID: "secret-1",
	}); st.Status != "rejected" {
		t.Fatalf("cross-team deliver = %#v", st)
	}
}

func TestAgentMessageRequestContractViaEngine(t *testing.T) {
	lead := New(Options{SessionID: "L-req", InitialAgent: "build", Agents: []Agent{{Name: "build"}}})
	child := New(Options{
		SessionID: "A-req", ParentSessionID: "L-req", Depth: 1,
		Team: lead.Team(), Agents: []Agent{{Name: "build"}},
	})
	tm := lead.Team()
	_ = tm.Enroll(TeamMember{SessionID: "A-req", ParentSessionID: "L-req", Depth: 1})
	tm.AttachMailbox(lead)
	tm.AttachMailbox(child)
	t.Cleanup(func() {
		tm.DetachMailbox("L-req")
		tm.DetachMailbox("A-req")
		tm.Dissolve()
	})

	res, err := lead.agentMessage(context.Background(), tool.AgentMessageRequest{
		To:                "A-req",
		Body:              "peer request please",
		TaskID:            "d1",
		Kind:              "request",
		Urgency:           "blocker",
		AckTimeoutSeconds: 120,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "accepted" || !res.RequireAck || res.AckStatus != "pending" {
		t.Fatalf("res = %+v", res)
	}
	if res.Urgency != protocol.AgentUrgencyBlocker || res.Kind != protocol.AgentMessageKindRequest {
		t.Fatalf("contract fields = %+v", res)
	}
	if res.EscalateTo != "L-req" {
		t.Fatalf("escalate default = %q", res.EscalateTo)
	}
	unread := child.Mailbox().PeekUnread()
	if len(unread) != 1 {
		t.Fatalf("mailbox = %#v", unread)
	}
	if !unread[0].RequireAck || unread[0].TaskID != "d1" || unread[0].Urgency != protocol.AgentUrgencyBlocker {
		t.Fatalf("mail contract = %#v", unread[0])
	}
	notice := formatMailboxNotice(unread[0])
	if !strings.Contains(notice, "urgency=blocker") || !strings.Contains(notice, "require_ack=true") {
		t.Fatalf("notice = %q", notice)
	}
}

func TestDissolveStopsPendingAckTimers(t *testing.T) {
	lead := New(Options{SessionID: "L-dis", Agents: []Agent{{Name: "build"}}})
	child := New(Options{
		SessionID: "A-dis", ParentSessionID: "L-dis", Depth: 1,
		Team: lead.Team(), Agents: []Agent{{Name: "build"}},
	})
	tm := lead.Team()
	_ = tm.Enroll(TeamMember{SessionID: "A-dis", ParentSessionID: "L-dis", Depth: 1})
	tm.AttachMailbox(lead)
	tm.AttachMailbox(child)

	st := tm.DeliverMessage(MailboxMessage{
		From: "L-dis", To: "A-dis", Body: "x",
		RequireAck: true,
		Deadline:   time.Now().UTC().Add(time.Hour),
	})
	if st.Status != "accepted" {
		t.Fatalf("%#v", st)
	}
	tm.Dissolve()
	if _, ok := tm.PendingAck(st.MessageID); ok {
		t.Fatal("pending should clear on dissolve")
	}
	if len(tm.Thread("anything")) != 0 {
		t.Fatal("threads cleared")
	}
}
