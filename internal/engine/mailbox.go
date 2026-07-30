package engine

import (
	"context"
	"crypto/rand"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/provider"
)

// maxMailboxPending caps unread messages per agent. Flood drops the oldest
// unread entry (same class of bound as child transcript / input queue caps).
const maxMailboxPending = 64

// MailboxMessage is one peer message in a per-agent mailbox.
type MailboxMessage struct {
	ID        string
	From      string
	To        string
	Body      string
	Summary   string // optional short UI label
	TeamID    string
	CreatedAt time.Time
}

// Mailbox is a bounded FIFO of unread peer messages for one session.
// Thread-safe for concurrent senders; TakePending is called from the recipient
// engine's turn worker or Run loop at safe boundaries only.
type Mailbox struct {
	mu      sync.Mutex
	pending []MailboxMessage
	// dropped counts oldest-evicted under capacity pressure.
	dropped int
}

// Len returns the number of unread messages.
func (m *Mailbox) Len() int {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.pending)
}

// Dropped returns how many messages were evicted by the capacity policy.
func (m *Mailbox) Dropped() int {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.dropped
}

// Enqueue appends msg. When at capacity, the oldest unread is dropped.
// Empty body is rejected (false).
func (m *Mailbox) Enqueue(msg MailboxMessage) bool {
	if m == nil {
		return false
	}
	body := strings.TrimSpace(msg.Body)
	if body == "" {
		return false
	}
	msg.Body = body
	if msg.ID == "" {
		msg.ID = rand.Text()
	}
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = time.Now().UTC()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for len(m.pending) >= maxMailboxPending {
		m.pending = m.pending[1:]
		m.dropped++
	}
	m.pending = append(m.pending, msg)
	return true
}

// PeekUnread returns a copy of unread messages without consuming them.
func (m *Mailbox) PeekUnread() []MailboxMessage {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.pending) == 0 {
		return nil
	}
	out := make([]MailboxMessage, len(m.pending))
	copy(out, m.pending)
	return out
}

// TakePending removes and returns all unread messages (may be nil).
func (m *Mailbox) TakePending() []MailboxMessage {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.pending) == 0 {
		return nil
	}
	out := m.pending
	m.pending = nil
	return out
}

// MailboxStatus is the result of EnqueueTeamMessage / Team.Deliver.
// Status is queued|accepted|rejected (same vocabulary as task_message).
type MailboxStatus struct {
	MessageID string
	From      string
	To        string
	Status    string // queued | accepted | rejected
	Detail    string
	Dropped   bool // true when this enqueue evicted an older message
}

// mailboxTarget is a live engine registered for team mailbox delivery.
type mailboxTarget struct {
	eng *Engine
	box *Mailbox
}

// AttachMailbox registers eng for peer delivery on this team. Called from
// Engine.Run start (lead and children). Idempotent for the same session id.
func (t *Team) AttachMailbox(eng *Engine) {
	if t == nil || eng == nil {
		return
	}
	id := strings.TrimSpace(eng.opts.SessionID)
	if id == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.live == nil {
		t.live = make(map[string]*mailboxTarget)
	}
	if existing, ok := t.live[id]; ok && existing != nil && existing.eng == eng {
		return
	}
	box := eng.mailbox
	if box == nil {
		box = &Mailbox{}
		eng.mailbox = box
	}
	t.live[id] = &mailboxTarget{eng: eng, box: box}
}

// DetachMailbox removes a session from live delivery (engine exit / child
// finish). Pending unread messages are discarded with the target.
func (t *Team) DetachMailbox(sessionID string) {
	if t == nil {
		return
	}
	id := strings.TrimSpace(sessionID)
	if id == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.live, id)
}

// Deliver enqueues a peer message to a live teammate's mailbox.
// Equivalent to DeliverMessage with From/To/Body only.
func (t *Team) Deliver(from, to, body string) MailboxStatus {
	return t.DeliverMessage(MailboxMessage{From: from, To: to, Body: body})
}

// DeliverMessage enqueues msg to a live teammate's mailbox.
//
// Rules:
//   - from and to must be on the team roster
//   - to must be live (running engine attached); completed/unknown → rejected
//   - empty body → rejected
//   - ordering per recipient is FIFO; concurrent senders serialize on the box
//
// Status is "accepted" when the message is in the recipient mailbox (delivery
// always happens at a tool-round / turn boundary — never mid-tool-call).
// "queued" is reserved for future back-pressure; callers should treat both as
// success. Do not call turnActive here (cross-goroutine race with Run).
func (t *Team) DeliverMessage(msg MailboxMessage) MailboxStatus {
	from := strings.TrimSpace(msg.From)
	to := strings.TrimSpace(msg.To)
	body := strings.TrimSpace(msg.Body)
	summary := strings.TrimSpace(msg.Summary)
	st := MailboxStatus{From: from, To: to}
	if t == nil {
		st.Status = "rejected"
		st.Detail = "no team"
		return st
	}
	if from == "" || to == "" {
		st.Status = "rejected"
		st.Detail = "from and to are required"
		return st
	}
	if body == "" {
		st.Status = "rejected"
		st.Detail = "body is required"
		return st
	}
	if from == to {
		st.Status = "rejected"
		st.Detail = "cannot message self"
		return st
	}

	msgID := strings.TrimSpace(msg.ID)
	if msgID == "" {
		msgID = rand.Text()
	}
	out := MailboxMessage{
		ID:        msgID,
		From:      from,
		To:        to,
		Body:      body,
		Summary:   summary,
		CreatedAt: msg.CreatedAt,
	}
	if out.CreatedAt.IsZero() {
		out.CreatedAt = time.Now().UTC()
	}

	// Hold team lock through live lookup + enqueue so DetachMailbox cannot
	// interleave (false "accepted" onto a detaching recipient).
	t.mu.Lock()
	if _, ok := t.members[from]; !ok {
		t.mu.Unlock()
		st.Status = "rejected"
		st.Detail = "sender is not on this team"
		return st
	}
	member, ok := t.members[to]
	if !ok {
		t.mu.Unlock()
		st.Status = "rejected"
		st.Detail = "recipient is not on this team"
		return st
	}
	if member.State != "" && member.State != protocol.TeamMemberRunning {
		t.mu.Unlock()
		st.Status = "rejected"
		st.Detail = "recipient is closed (" + string(member.State) + ")"
		return st
	}
	target := t.live[to]
	if target == nil || target.eng == nil || target.box == nil {
		t.mu.Unlock()
		st.Status = "rejected"
		st.Detail = "recipient is not live"
		return st
	}
	out.TeamID = t.leadID
	beforeDrop := target.box.Dropped()
	if !target.box.Enqueue(out) {
		t.mu.Unlock()
		st.Status = "rejected"
		st.Detail = "enqueue failed"
		return st
	}
	st.MessageID = out.ID
	st.Dropped = target.box.Dropped() > beforeDrop
	eng := target.eng
	t.mu.Unlock()

	// Wake recipient Run to idle-nudge or continue; AgentMessage is emitted
	// from the recipient turn/Run path on boundary inject (never from this
	// goroutine — avoids send-on-closed-events during recipient shutdown).
	eng.signalMailboxWake()

	st.Status = "accepted"
	st.Detail = "enqueued for boundary delivery"
	return st
}

// EnqueueTeamMessage is the engine-facing inject API for peer mailbox delivery.
// Used by agent_message / agent_broadcast. from defaults to this session.
func (e *Engine) EnqueueTeamMessage(from, to, body string) MailboxStatus {
	return e.EnqueueTeamMail(MailboxMessage{From: from, To: to, Body: body})
}

// EnqueueTeamMail delivers a full mailbox message (optional Summary).
// From defaults to this session when empty.
func (e *Engine) EnqueueTeamMail(msg MailboxMessage) MailboxStatus {
	if e == nil {
		return MailboxStatus{Status: "rejected", Detail: "no engine", To: strings.TrimSpace(msg.To)}
	}
	from := strings.TrimSpace(msg.From)
	if from == "" {
		from = e.opts.SessionID
	}
	msg.From = from
	if e.team == nil {
		return MailboxStatus{
			From:   from,
			To:     strings.TrimSpace(msg.To),
			Status: "rejected",
			Detail: "no team",
		}
	}
	return e.team.DeliverMessage(msg)
}

// Mailbox returns this engine's per-session mailbox (may be nil before attach).
func (e *Engine) Mailbox() *Mailbox {
	if e == nil {
		return nil
	}
	return e.mailbox
}

func (e *Engine) hasPendingMailbox() bool {
	if e == nil || e.mailbox == nil {
		return false
	}
	return e.mailbox.Len() > 0
}

func (e *Engine) mailboxWakeCh() <-chan struct{} {
	e.mailboxMu.Lock()
	defer e.mailboxMu.Unlock()
	if e.mailboxWake == nil {
		e.mailboxWake = make(chan struct{})
	}
	return e.mailboxWake
}

func (e *Engine) signalMailboxWake() {
	if e == nil {
		return
	}
	e.mailboxMu.Lock()
	defer e.mailboxMu.Unlock()
	if e.mailboxWake == nil {
		e.mailboxWake = make(chan struct{})
	}
	select {
	case <-e.mailboxWake:
		// already closed
	default:
		close(e.mailboxWake)
	}
	e.mailboxWake = make(chan struct{})
}

// injectPendingMailbox appends queued peer messages into model history before
// the next Stream (tool-round boundary). Safe only from the turn worker.
// Never called mid-tool Execute — only at the top of the stream loop.
func (e *Engine) injectPendingMailbox() {
	if e == nil || e.mailbox == nil {
		return
	}
	msgs := e.mailbox.TakePending()
	if len(msgs) == 0 {
		return
	}
	e.emitAgentMessages(msgs)
	text := formatMailboxNotices(msgs)
	e.emit(protocol.UserMessage{Correlation: e.sessionCorr(), Text: text})
	e.messages = append(e.messages, provider.Message{
		Role: provider.RoleUser,
		Text: text,
	})
}

// flushPendingMailbox starts an idle turn with queued peer messages when the
// engine is idle and a provider is selected (same class as child.completed
// auto-nudge). Mid-turn injectPendingMailbox may already have drained.
func (e *Engine) flushPendingMailbox(ctx context.Context) {
	if !e.hasPendingMailbox() {
		return
	}
	e.joinFinishingTurn()
	if e.turnActive() {
		return
	}
	if e.prov == nil || ctx.Err() != nil {
		return
	}
	msgs := e.mailbox.TakePending()
	if len(msgs) == 0 {
		return
	}
	e.emitAgentMessages(msgs)
	e.startTurn(ctx, formatMailboxNotices(msgs), nil)
}

// emitAgentMessages emits structured AgentMessage events (recipient Run/turn
// only). Parent child-drain re-emits child AgentMessage for TUI/debug.
func (e *Engine) emitAgentMessages(msgs []MailboxMessage) {
	if e == nil || len(msgs) == 0 {
		return
	}
	corr := e.sessionCorr()
	for _, m := range msgs {
		e.emit(protocol.AgentMessage{
			Correlation: corr,
			From:        m.From,
			To:          m.To,
			Body:        m.Body,
			Summary:     m.Summary,
			TeamID:      m.TeamID,
			MessageID:   m.ID,
		})
	}
}

func formatMailboxNotices(msgs []MailboxMessage) string {
	parts := make([]string, 0, len(msgs))
	for _, m := range msgs {
		parts = append(parts, formatMailboxNotice(m))
	}
	return strings.Join(parts, "\n\n")
}

func formatMailboxNotice(m MailboxMessage) string {
	from := strings.TrimSpace(m.From)
	short := from
	if len(short) > 8 {
		short = short[:8]
	}
	var b strings.Builder
	if short != "" {
		fmt.Fprintf(&b, "[agent.message from=%s]", short)
	} else {
		b.WriteString("[agent.message]")
	}
	if s := compactMailboxSummary(m.Summary); s != "" {
		fmt.Fprintf(&b, " summary=%s", s)
	}
	if body := strings.TrimSpace(m.Body); body != "" {
		b.WriteByte('\n')
		b.WriteString(body)
	}
	return b.String()
}

// compactMailboxSummary flattens whitespace and caps length so the notice
// header stays single-line.
func compactMailboxSummary(s string) string {
	s = strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
	const max = 120
	if len(s) <= max {
		return s
	}
	// Cap by runes approximately via byte cut then trim incomplete tail.
	if max < len(s) {
		s = s[:max]
	}
	return strings.TrimSpace(s) + "…"
}
