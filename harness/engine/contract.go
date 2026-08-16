package engine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jonathanung/strike-cli/harness/tool"
	"github.com/jonathanung/strike-cli/pkg/protocol"
)

// Coordination-contract bounds (ack TTL, thread ring).
const (
	defaultAckTimeoutSec = 60.0
	maxAckTimeoutSec     = 300.0
	maxThreadMessages    = 64
	maxPendingAcks       = 64
)

// threadEntry is one message recorded on a task/delegation thread.
type threadEntry struct {
	MessageID  string
	From       string
	To         string
	Body       string
	Summary    string
	TaskID     string
	Urgency    string
	Kind       string
	RequireAck bool
	InReplyTo  string
	EscalateTo string
	AckStatus  string
	CreatedAt  time.Time
}

// pendingAck tracks a require-ack delivery until ack or TTL.
type pendingAck struct {
	MessageID  string
	From       string
	To         string
	Body       string
	Summary    string
	TaskID     string
	Urgency    string
	EscalateTo string
	TeamID     string
	Deadline   time.Time
	timer      *time.Timer
}

func normalizeUrgency(u string) string {
	switch strings.ToLower(strings.TrimSpace(u)) {
	case "", protocol.AgentUrgencyNormal, "med", "medium", "low":
		return protocol.AgentUrgencyNormal
	case protocol.AgentUrgencyHigh, "urgent":
		return protocol.AgentUrgencyHigh
	case protocol.AgentUrgencyBlocker, "block", "blocking", "critical":
		return protocol.AgentUrgencyBlocker
	default:
		return ""
	}
}

func urgencyRank(u string) int {
	switch normalizeUrgency(u) {
	case protocol.AgentUrgencyBlocker:
		return 0
	case protocol.AgentUrgencyHigh:
		return 1
	default:
		return 2
	}
}

func normalizeMessageKind(k string) string {
	switch strings.ToLower(strings.TrimSpace(k)) {
	case "", protocol.AgentMessageKindMessage, "msg", "chat":
		return protocol.AgentMessageKindMessage
	case protocol.AgentMessageKindRequest, "req", "peer_request":
		return protocol.AgentMessageKindRequest
	case protocol.AgentMessageKindAck, "acknowledge", "acked":
		return protocol.AgentMessageKindAck
	case protocol.AgentMessageKindTimeout:
		return protocol.AgentMessageKindTimeout
	case protocol.AgentMessageKindEscalation, "escalate":
		return protocol.AgentMessageKindEscalation
	default:
		return ""
	}
}

func clampAckTimeoutSec(sec float64) float64 {
	if sec <= 0 {
		return defaultAckTimeoutSec
	}
	if sec > maxAckTimeoutSec {
		return maxAckTimeoutSec
	}
	return sec
}

// recordThreadLocked appends msg to the task thread (caller holds t.mu).
func (t *Team) recordThreadLocked(msg MailboxMessage) {
	taskID := strings.TrimSpace(msg.TaskID)
	if taskID == "" || t == nil {
		return
	}
	if t.threads == nil {
		t.threads = make(map[string][]threadEntry)
	}
	entry := threadEntry{
		MessageID:  msg.ID,
		From:       msg.From,
		To:         msg.To,
		Body:       msg.Body,
		Summary:    msg.Summary,
		TaskID:     taskID,
		Urgency:    msg.Urgency,
		Kind:       msg.Kind,
		RequireAck: msg.RequireAck,
		InReplyTo:  msg.InReplyTo,
		EscalateTo: msg.EscalateTo,
		AckStatus:  msg.AckStatus,
		CreatedAt:  msg.CreatedAt,
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now().UTC()
	}
	list := append(t.threads[taskID], entry)
	if len(list) > maxThreadMessages {
		list = append([]threadEntry(nil), list[len(list)-maxThreadMessages:]...)
	}
	t.threads[taskID] = list
}

// updateThreadAckLocked sets ack status on the original message in its thread.
func (t *Team) updateThreadAckLocked(messageID, ackStatus string) {
	if t == nil || messageID == "" {
		return
	}
	for taskID, list := range t.threads {
		for i := range list {
			if list[i].MessageID == messageID {
				list[i].AckStatus = ackStatus
				t.threads[taskID] = list
				return
			}
		}
	}
}

// Thread returns a copy of messages bound to taskID (team members only).
func (t *Team) Thread(taskID string) []threadEntry {
	if t == nil {
		return nil
	}
	id := strings.TrimSpace(taskID)
	if id == "" {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	list := t.threads[id]
	if len(list) == 0 {
		return nil
	}
	out := make([]threadEntry, len(list))
	copy(out, list)
	return out
}

// registerPendingAckLocked schedules TTL for a require-ack message.
// Caller holds t.mu. msg must already have ID and Deadline.
func (t *Team) registerPendingAckLocked(msg MailboxMessage) {
	if t == nil || !msg.RequireAck || strings.TrimSpace(msg.ID) == "" {
		return
	}
	// Acks / system kinds never open a new pending ack.
	switch normalizeMessageKind(msg.Kind) {
	case protocol.AgentMessageKindAck, protocol.AgentMessageKindTimeout, protocol.AgentMessageKindEscalation:
		return
	}
	if t.pendingAcks == nil {
		t.pendingAcks = make(map[string]*pendingAck)
	}
	// Cap: drop oldest by deadline when full.
	for len(t.pendingAcks) >= maxPendingAcks {
		var oldestID string
		var oldest time.Time
		for id, p := range t.pendingAcks {
			if oldestID == "" || p.Deadline.Before(oldest) {
				oldestID = id
				oldest = p.Deadline
			}
		}
		if oldestID == "" {
			break
		}
		t.cancelPendingAckLocked(oldestID)
	}
	// Replace any prior timer for same id.
	t.cancelPendingAckLocked(msg.ID)

	deadline := msg.Deadline
	if deadline.IsZero() {
		deadline = time.Now().UTC().Add(time.Duration(defaultAckTimeoutSec * float64(time.Second)))
	}
	p := &pendingAck{
		MessageID:  msg.ID,
		From:       msg.From,
		To:         msg.To,
		Body:       msg.Body,
		Summary:    msg.Summary,
		TaskID:     msg.TaskID,
		Urgency:    msg.Urgency,
		EscalateTo: msg.EscalateTo,
		TeamID:     msg.TeamID,
		Deadline:   deadline,
	}
	msgID := msg.ID
	delay := time.Until(deadline)
	if delay < 0 {
		delay = 0
	}
	p.timer = time.AfterFunc(delay, func() {
		t.fireAckTimeout(msgID)
	})
	t.pendingAcks[msgID] = p
}

func (t *Team) cancelPendingAckLocked(messageID string) {
	if t == nil || t.pendingAcks == nil {
		return
	}
	p, ok := t.pendingAcks[messageID]
	if !ok {
		return
	}
	if p.timer != nil {
		p.timer.Stop()
	}
	delete(t.pendingAcks, messageID)
}

// AckMessage marks a pending require-ack as acked. Returns false when unknown
// or already settled. Caller must be the original recipient (to).
func (t *Team) AckMessage(messageID, fromSession string) (pendingAck, bool) {
	if t == nil {
		return pendingAck{}, false
	}
	id := strings.TrimSpace(messageID)
	from := strings.TrimSpace(fromSession)
	if id == "" || from == "" {
		return pendingAck{}, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	p, ok := t.pendingAcks[id]
	if !ok || p == nil {
		return pendingAck{}, false
	}
	if p.To != from {
		return pendingAck{}, false
	}
	out := *p
	if p.timer != nil {
		p.timer.Stop()
	}
	delete(t.pendingAcks, id)
	t.updateThreadAckLocked(id, "acked")
	return out, true
}

// PendingAck returns a copy of the pending ack for messageID, if any.
func (t *Team) PendingAck(messageID string) (pendingAck, bool) {
	if t == nil {
		return pendingAck{}, false
	}
	id := strings.TrimSpace(messageID)
	t.mu.Lock()
	defer t.mu.Unlock()
	p, ok := t.pendingAcks[id]
	if !ok || p == nil {
		return pendingAck{}, false
	}
	return *p, true
}

func (t *Team) fireAckTimeout(messageID string) {
	if t == nil {
		return
	}
	id := strings.TrimSpace(messageID)
	t.mu.Lock()
	p, ok := t.pendingAcks[id]
	if !ok || p == nil {
		t.mu.Unlock()
		return
	}
	// Timer may fire slightly early; reschedule remaining TTL so we never
	// drop a pending ack without a future fire (Stop+return would stick).
	if rem := time.Until(p.Deadline); rem > 5*time.Millisecond {
		if p.timer != nil {
			p.timer.Stop()
		}
		msgID := id
		p.timer = time.AfterFunc(rem, func() {
			t.fireAckTimeout(msgID)
		})
		t.mu.Unlock()
		return
	}
	out := *p
	if p.timer != nil {
		p.timer.Stop()
	}
	delete(t.pendingAcks, id)
	t.updateThreadAckLocked(id, "timed_out")
	// Snapshot live engines for emit + escalation under lock.
	var senderEng, escalateEng *Engine
	if tgt := t.live[out.From]; tgt != nil {
		senderEng = tgt.eng
	}
	escTo := strings.TrimSpace(out.EscalateTo)
	if escTo == "" {
		escTo = t.leadID
	}
	out.EscalateTo = escTo
	if tgt := t.live[escTo]; tgt != nil {
		escalateEng = tgt.eng
	}
	// Record timeout on thread as a synthetic entry.
	timeoutMsg := MailboxMessage{
		ID:        "timeout-" + id,
		From:      out.From,
		To:        out.To,
		Body:      "ack timed out for message " + id,
		Summary:   "ack timeout",
		TeamID:    out.TeamID,
		TaskID:    out.TaskID,
		Urgency:   out.Urgency,
		Kind:      protocol.AgentMessageKindTimeout,
		InReplyTo: id,
		AckStatus: "timed_out",
		CreatedAt: time.Now().UTC(),
	}
	t.recordThreadLocked(timeoutMsg)
	t.mu.Unlock()

	// Emit structured timeout on the original sender (who required the ack).
	if senderEng != nil {
		senderEng.emit(protocol.AgentContractTimeout{
			Correlation: senderEng.sessionCorr(),
			MessageID:   out.MessageID,
			From:        out.From,
			To:          out.To,
			TaskID:      out.TaskID,
			TeamID:      out.TeamID,
			Urgency:     out.Urgency,
			EscalateTo:  out.EscalateTo,
			Detail:      "ack timed out",
		})
	}

	// Escalate to lead (or configured target) via mailbox when live and not self.
	if escalateEng != nil && out.EscalateTo != "" && out.EscalateTo != out.From {
		body := fmt.Sprintf(
			"[agent.contract.timeout] un-acked message %s from %s to %s",
			out.MessageID, shortSessionPrefix(out.From), shortSessionPrefix(out.To),
		)
		if s := strings.TrimSpace(out.Summary); s != "" {
			body += " summary=" + s
		}
		if b := strings.TrimSpace(out.Body); b != "" {
			body += "\n" + b
		}
		_ = t.DeliverMessage(MailboxMessage{
			From:      out.From,
			To:        out.EscalateTo,
			Body:      body,
			Summary:   "ack timeout escalation",
			TaskID:    out.TaskID,
			Urgency:   preferBlocker(out.Urgency),
			Kind:      protocol.AgentMessageKindEscalation,
			InReplyTo: out.MessageID,
			AckStatus: "timed_out",
		})
	}
}

func preferBlocker(u string) string {
	if normalizeUrgency(u) == protocol.AgentUrgencyBlocker {
		return protocol.AgentUrgencyBlocker
	}
	return protocol.AgentUrgencyHigh
}

func shortSessionPrefix(id string) string {
	id = strings.TrimSpace(id)
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

func (t *Team) clearContractsLocked() {
	if t == nil {
		return
	}
	for id, p := range t.pendingAcks {
		if p != nil && p.timer != nil {
			p.timer.Stop()
		}
		delete(t.pendingAcks, id)
	}
	t.pendingAcks = nil
	t.threads = nil
}

// agentThread lists a task/delegation-bound message thread for team members.
func (e *Engine) agentThread(ctx context.Context, req tool.AgentThreadRequest) (tool.AgentThreadResult, error) {
	if err := ctx.Err(); err != nil {
		return tool.AgentThreadResult{}, err
	}
	if e == nil || e.team == nil {
		return tool.AgentThreadResult{}, fmt.Errorf("agent_thread is not available (no team)")
	}
	taskID := strings.TrimSpace(req.TaskID)
	if taskID == "" {
		return tool.AgentThreadResult{}, fmt.Errorf("task_id is required")
	}
	// Fail closed: caller must be on the team (always true when e.team is set
	// and session enrolled; double-check for detached edge cases).
	self := strings.TrimSpace(e.opts.SessionID)
	if self == "" || !e.team.Contains(self) {
		return tool.AgentThreadResult{}, fmt.Errorf("agent_thread is not available (not on team)")
	}
	entries := e.team.Thread(taskID)
	limit := req.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > maxThreadMessages {
		limit = maxThreadMessages
	}
	if len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	out := tool.AgentThreadResult{
		TaskID:   taskID,
		Messages: make([]tool.AgentThreadMessage, 0, len(entries)),
	}
	for _, ent := range entries {
		out.Messages = append(out.Messages, tool.AgentThreadMessage{
			MessageID:  ent.MessageID,
			From:       ent.From,
			To:         ent.To,
			Body:       ent.Body,
			Summary:    ent.Summary,
			TaskID:     ent.TaskID,
			Urgency:    ent.Urgency,
			Kind:       ent.Kind,
			RequireAck: ent.RequireAck,
			InReplyTo:  ent.InReplyTo,
			EscalateTo: ent.EscalateTo,
			AckStatus:  ent.AckStatus,
			CreatedAt:  ent.CreatedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	return out, nil
}
