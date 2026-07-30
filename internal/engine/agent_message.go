package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/jonathanung/strike-cli/internal/tool"
)

// agentMessage sends one peer mailbox message to a teammate (session id or name).
// Available on lead and children whenever a Team is attached.
func (e *Engine) agentMessage(ctx context.Context, req tool.AgentMessageRequest) (tool.AgentMessageResult, error) {
	if err := ctx.Err(); err != nil {
		return tool.AgentMessageResult{}, err
	}
	if e == nil || e.team == nil {
		return tool.AgentMessageResult{}, fmt.Errorf("agent_message is not available (no team)")
	}
	toAddr := strings.TrimSpace(req.To)
	body := strings.TrimSpace(req.Body)
	summary := strings.TrimSpace(req.Summary)
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

	st := e.EnqueueTeamMail(MailboxMessage{
		From:    e.opts.SessionID,
		To:      toID,
		Body:    body,
		Summary: summary,
	})
	return tool.AgentMessageResult{
		To:        st.To,
		Status:    st.Status,
		Detail:    st.Detail,
		MessageID: st.MessageID,
		Dropped:   st.Dropped,
	}, nil
}

// agentBroadcast sends body to every other roster member via the mailbox.
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
