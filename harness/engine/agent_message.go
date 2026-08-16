package engine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jonathanung/strike-cli/harness/tool"
	"github.com/jonathanung/strike-cli/pkg/protocol"
)

// agentMessage sends one peer mailbox message to a teammate (session id or name).
// Available on lead and children whenever a Team is attached.
//
// Coordination contracts (optional):
//   - task_id binds the message to a team_task / delegation thread
//   - urgency orders delivery and surfaces in notices/UI
//   - kind=request (or require_ack) tracks ack TTL + escalation
//   - kind=ack settles a pending require-ack (in_reply_to required)
func (e *Engine) agentMessage(ctx context.Context, req tool.AgentMessageRequest) (tool.AgentMessageResult, error) {
	if err := ctx.Err(); err != nil {
		return tool.AgentMessageResult{}, err
	}
	if e == nil || e.team == nil {
		return tool.AgentMessageResult{}, fmt.Errorf("agent_message is not available (no team)")
	}

	kind := normalizeMessageKind(req.Kind)
	if kind == "" {
		if strings.TrimSpace(req.Kind) != "" {
			return tool.AgentMessageResult{}, fmt.Errorf("kind must be message, request, or ack")
		}
		kind = protocol.AgentMessageKindMessage
	}
	// Engine path only accepts user-facing kinds (not timeout/escalation).
	switch kind {
	case protocol.AgentMessageKindMessage, protocol.AgentMessageKindRequest, protocol.AgentMessageKindAck:
	default:
		return tool.AgentMessageResult{}, fmt.Errorf("kind must be message, request, or ack")
	}

	urgency := normalizeUrgency(req.Urgency)
	if urgency == "" {
		if strings.TrimSpace(req.Urgency) != "" {
			return tool.AgentMessageResult{}, fmt.Errorf("urgency must be normal, high, or blocker")
		}
		urgency = protocol.AgentUrgencyNormal
	}

	body := strings.TrimSpace(req.Body)
	summary := strings.TrimSpace(req.Summary)
	taskID := strings.TrimSpace(req.TaskID)
	inReplyTo := strings.TrimSpace(req.InReplyTo)
	requireAck := req.RequireAck
	if kind == protocol.AgentMessageKindRequest {
		requireAck = true
	}

	// kind=ack: settle pending and deliver ack notice to original sender.
	if kind == protocol.AgentMessageKindAck {
		if inReplyTo == "" {
			return tool.AgentMessageResult{}, fmt.Errorf("in_reply_to is required for kind=ack")
		}
		if body == "" {
			body = "ack"
		}
		orig, ok := e.team.AckMessage(inReplyTo, e.opts.SessionID)
		if !ok {
			return tool.AgentMessageResult{
				To:     strings.TrimSpace(req.To),
				Status: "rejected",
				Detail: "unknown or inaccessible pending ack (wrong recipient, already settled, or unknown message_id)",
			}, nil
		}
		toID := orig.From
		if taskID == "" {
			taskID = orig.TaskID
		}
		if urgency == protocol.AgentUrgencyNormal && orig.Urgency != "" {
			urgency = normalizeUrgency(orig.Urgency)
			if urgency == "" {
				urgency = protocol.AgentUrgencyNormal
			}
		}
		st := e.EnqueueTeamMail(MailboxMessage{
			From:      e.opts.SessionID,
			To:        toID,
			Body:      body,
			Summary:   summary,
			TaskID:    taskID,
			Urgency:   urgency,
			Kind:      protocol.AgentMessageKindAck,
			InReplyTo: inReplyTo,
			AckStatus: "acked",
		})
		// Pending ack is already settled; do not surface delivery failure as a
		// hard reject (retry would see "unknown pending"). Report accepted with
		// delivery detail when the original sender is not live.
		res := tool.AgentMessageResult{
			To:         toID,
			Status:     "accepted",
			Detail:     "ack recorded",
			MessageID:  st.MessageID,
			Dropped:    st.Dropped,
			TaskID:     taskID,
			Urgency:    urgency,
			Kind:       protocol.AgentMessageKindAck,
			InReplyTo:  inReplyTo,
			AckStatus:  "acked",
			RequireAck: false,
		}
		if st.Status != "accepted" && st.Status != "queued" {
			res.Detail = "ack recorded; delivery to sender failed: " + st.Detail
			res.To = toID
			if st.To != "" {
				res.To = st.To
			}
		} else {
			res.Detail = st.Detail
			if res.Detail == "" {
				res.Detail = "ack recorded"
			}
			res.To = st.To
		}
		return res, nil
	}

	toAddr := strings.TrimSpace(req.To)
	if toAddr == "" {
		return tool.AgentMessageResult{}, fmt.Errorf("to is required")
	}
	if body == "" {
		return tool.AgentMessageResult{}, fmt.Errorf("body is required")
	}

	toID, detail, ok := e.team.ResolveAddressDetail(toAddr)
	if !ok {
		if detail == "" {
			detail = "recipient is not on this team"
		}
		return tool.AgentMessageResult{
			To:     toAddr,
			Status: "rejected",
			Detail: detail,
		}, nil
	}

	escalateTo := strings.TrimSpace(req.EscalateTo)
	if escalateTo != "" {
		if id, ok := e.team.Resolve(escalateTo); ok {
			escalateTo = id
		} else if id, _, ok := e.team.ResolveAddressDetail(escalateTo); ok {
			escalateTo = id
		} else {
			return tool.AgentMessageResult{
				To:     toID,
				Status: "rejected",
				Detail: "escalate_to is not on this team",
			}, nil
		}
	}

	var deadline time.Time
	ackTimeout := req.AckTimeoutSeconds
	if requireAck {
		ackTimeout = clampAckTimeoutSec(ackTimeout)
		deadline = time.Now().UTC().Add(time.Duration(ackTimeout * float64(time.Second)))
	}

	st := e.EnqueueTeamMail(MailboxMessage{
		From:       e.opts.SessionID,
		To:         toID,
		Body:       body,
		Summary:    summary,
		TaskID:     taskID,
		Urgency:    urgency,
		Kind:       kind,
		RequireAck: requireAck,
		InReplyTo:  inReplyTo,
		EscalateTo: escalateTo,
		Deadline:   deadline,
	})
	res := tool.AgentMessageResult{
		To:         st.To,
		Status:     st.Status,
		Detail:     st.Detail,
		MessageID:  st.MessageID,
		Dropped:    st.Dropped,
		TaskID:     taskID,
		Urgency:    urgency,
		Kind:       kind,
		RequireAck: requireAck,
		InReplyTo:  inReplyTo,
		EscalateTo: escalateTo,
	}
	if requireAck && st.Status == "accepted" {
		res.AckStatus = "pending"
		res.AckTimeoutSeconds = ackTimeout
		if escalateTo == "" {
			res.EscalateTo = e.team.LeadID()
		}
	}
	return res, nil
}

// agentBroadcast sends body to every other roster member via the mailbox.
// Optional task_id / urgency apply to each copy; require_ack is not supported
// on broadcast (would N-track acks — use agent_message for contracts).
func (e *Engine) agentBroadcast(ctx context.Context, req tool.AgentBroadcastRequest) (tool.AgentBroadcastResult, error) {
	if err := ctx.Err(); err != nil {
		return tool.AgentBroadcastResult{}, err
	}
	if e == nil || e.team == nil {
		return tool.AgentBroadcastResult{}, fmt.Errorf("agent_broadcast is not available (no team)")
	}
	body := strings.TrimSpace(req.Body)
	summary := strings.TrimSpace(req.Summary)
	if body == "" {
		return tool.AgentBroadcastResult{}, fmt.Errorf("body is required")
	}
	urgency := normalizeUrgency(req.Urgency)
	if urgency == "" {
		if strings.TrimSpace(req.Urgency) != "" {
			return tool.AgentBroadcastResult{}, fmt.Errorf("urgency must be normal, high, or blocker")
		}
		urgency = protocol.AgentUrgencyNormal
	}
	taskID := strings.TrimSpace(req.TaskID)

	self := strings.TrimSpace(e.opts.SessionID)
	members := e.team.Roster()
	out := tool.AgentBroadcastResult{
		Results: make([]tool.AgentBroadcastDelivery, 0, len(members)),
	}
	for _, m := range members {
		id := strings.TrimSpace(m.SessionID)
		if id == "" || id == self {
			continue
		}
		st := e.EnqueueTeamMail(MailboxMessage{
			From:    self,
			To:      id,
			Body:    body,
			Summary: summary,
			TaskID:  taskID,
			Urgency: urgency,
			Kind:    protocol.AgentMessageKindMessage,
		})
		d := tool.AgentBroadcastDelivery{
			To:        st.To,
			Status:    st.Status,
			Detail:    st.Detail,
			MessageID: st.MessageID,
			Dropped:   st.Dropped,
		}
		out.Results = append(out.Results, d)
		switch st.Status {
		case "accepted", "queued":
			out.Delivered++
		default:
			out.Rejected++
		}
	}
	if len(out.Results) == 0 {
		// Solo team — nothing to send; not an error at the engine layer.
		out.Results = []tool.AgentBroadcastDelivery{}
	}
	return out, nil
}
