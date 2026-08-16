package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// MaxAgentMessageBodyRunes caps agent_message / agent_broadcast body size.
const MaxAgentMessageBodyRunes = 32 * 1024

// Ack timeout bounds for require_ack / kind=request.
const (
	DefaultAgentAckTimeoutSec = 60.0
	MaxAgentAckTimeoutSec     = 300.0
)

type agentMessageTool struct{}
type agentBroadcastTool struct{}

func NewAgentMessage() Tool   { return agentMessageTool{} }
func NewAgentBroadcast() Tool { return agentBroadcastTool{} }

func (agentMessageTool) Name() string { return "agent_message" }

func (agentMessageTool) Contract() Contract {
	return staticContract(SideEffectExternal, IdempotencyUnsafe)
}

func (agentMessageTool) Description() string {
	return `Send a message to one teammate on the implicit session team.

- to: teammate session_id (from agent_roster / task result), a unique session_id
  prefix (≥8 chars, e.g. UI short id), or a stable name alias when set on the roster.
- body: message text (required except kind=ack defaults to "ack"; size-capped).
- summary: optional short label for UI/debug (not a substitute for body).

Coordination contracts (prefer these over chatty status ping-pong):
- task_id: bind to a team_task or delegation id; readable via agent_thread.
- urgency: normal | high | blocker (orders delivery; surfaces in notices/UI).
- kind: message (default) | request | ack.
  - request implies require_ack and is the peer-request helper.
  - ack settles a pending require-ack (in_reply_to = original message_id;
    to is derived from the original sender).
- require_ack: ask the recipient to ack; un-acked after ack_timeout_seconds
  emits agent.contract.timeout and escalates to escalate_to (default: lead).
- ack_timeout_seconds: TTL when require_ack / kind=request (default 60, max 300).
- escalate_to: session_id or name for timeout escalation (default lead).
- in_reply_to: required for kind=ack.

Delivery is at a safe boundary on the recipient (tool-round / idle turn) —
never corrupts an in-flight tool call. Sender and recipient must share the
same session team; out-of-team fails closed.

Prefer contracts for mid-flight blockers/handoffs/questions that need ack or a
task thread. Prefer [child.completed] for finished work products from a child
you own. Children should message the lead early on blockers. Avoid chatty loops
(no status ping-pong the roster/completion already cover).

Available to lead and children (not stripped at depth ceiling).
task_message remains parent→owned-child guidance; use agent_message for any
teammate (including child→child and child→lead).`
}

func (agentMessageTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"to": {"type": "string", "description": "Teammate session_id or stable name (not required for kind=ack)"},
			"body": {"type": "string", "description": "Message text for the teammate"},
			"summary": {"type": "string", "description": "Optional short UI label"},
			"task_id": {"type": "string", "description": "Optional team_task or delegation id (thread key)"},
			"urgency": {"type": "string", "description": "normal | high | blocker (default normal)"},
			"kind": {"type": "string", "description": "message | request | ack (default message)"},
			"require_ack": {"type": "boolean", "description": "Require recipient ack; timeout escalates (implied by kind=request)"},
			"ack_timeout_seconds": {"type": "number", "description": "Ack TTL seconds when require_ack (default 60, max 300)"},
			"in_reply_to": {"type": "string", "description": "Original message_id when kind=ack"},
			"escalate_to": {"type": "string", "description": "Timeout escalation target session_id or name (default lead)"}
		}
	}`)
}

func (agentMessageTool) Execute(ctx context.Context, args json.RawMessage, tc *Context) (Result, error) {
	var a struct {
		To                string  `json:"to"`
		Body              string  `json:"body"`
		Summary           string  `json:"summary"`
		TaskID            string  `json:"task_id"`
		Urgency           string  `json:"urgency"`
		Kind              string  `json:"kind"`
		RequireAck        bool    `json:"require_ack"`
		AckTimeoutSeconds float64 `json:"ack_timeout_seconds"`
		InReplyTo         string  `json:"in_reply_to"`
		EscalateTo        string  `json:"escalate_to"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{}, fmt.Errorf("invalid arguments: %w", err)
	}
	to := strings.TrimSpace(a.To)
	body := strings.TrimSpace(a.Body)
	summary := clampAgentMessageSummary(a.Summary)
	kind := strings.ToLower(strings.TrimSpace(a.Kind))
	if kind == "" {
		kind = "message"
	}
	switch kind {
	case "message", "request", "ack":
	default:
		return Result{}, fmt.Errorf("kind must be message, request, or ack")
	}
	urgency := strings.ToLower(strings.TrimSpace(a.Urgency))
	if urgency != "" {
		switch urgency {
		case "normal", "high", "blocker":
		default:
			return Result{}, fmt.Errorf("urgency must be normal, high, or blocker")
		}
	}
	if kind != "ack" {
		if to == "" {
			return Result{}, fmt.Errorf("to is required")
		}
		if body == "" {
			return Result{}, fmt.Errorf("body is required")
		}
	} else {
		if strings.TrimSpace(a.InReplyTo) == "" {
			return Result{}, fmt.Errorf("in_reply_to is required for kind=ack")
		}
		// body optional for ack
	}
	if body != "" {
		if n := utf8.RuneCountInString(body); n > MaxAgentMessageBodyRunes {
			return Result{}, fmt.Errorf("body exceeds %d runes (%d)", MaxAgentMessageBodyRunes, n)
		}
	}
	if a.AckTimeoutSeconds < 0 || a.AckTimeoutSeconds > MaxAgentAckTimeoutSec {
		return Result{}, fmt.Errorf("ack_timeout_seconds must be in (0, %g]", MaxAgentAckTimeoutSec)
	}
	askPat := to
	if askPat == "" {
		askPat = strings.TrimSpace(a.InReplyTo)
	}
	if askPat == "" {
		askPat = "*"
	}
	if err := tc.Ask(ctx, AskRequest{Permission: "agent_message", Patterns: []string{askPat}, Always: []string{"*"}}); err != nil {
		return Result{}, err
	}
	if tc.AgentMessage == nil {
		return Result{}, fmt.Errorf("agent_message is not available")
	}
	res, err := tc.AgentMessage(ctx, AgentMessageRequest{
		To:                to,
		Body:              body,
		Summary:           summary,
		TaskID:            strings.TrimSpace(a.TaskID),
		Urgency:           urgency,
		Kind:              kind,
		RequireAck:        a.RequireAck,
		AckTimeoutSeconds: a.AckTimeoutSeconds,
		InReplyTo:         strings.TrimSpace(a.InReplyTo),
		EscalateTo:        strings.TrimSpace(a.EscalateTo),
	})
	if err != nil {
		return Result{}, err
	}
	out, _ := json.Marshal(res)
	title := "agent_message " + shortID(res.To) + " " + res.Status
	if res.To == "" {
		title = "agent_message " + shortID(to) + " " + res.Status
	}
	if res.Kind != "" && res.Kind != "message" {
		title += " " + res.Kind
	}
	if res.Urgency != "" && res.Urgency != "normal" {
		title += " " + res.Urgency
	}
	if res.Status == "rejected" {
		return Result{Title: title, Output: string(out)}, fmt.Errorf("%s", res.Detail)
	}
	return Result{Title: title, Output: string(out)}, nil
}

func (agentBroadcastTool) Name() string { return "agent_broadcast" }

func (agentBroadcastTool) Contract() Contract {
	return staticContract(SideEffectExternal, IdempotencyUnsafe)
}

func (agentBroadcastTool) Description() string {
	return `Broadcast a message to every other teammate on the session team.

- body: message text (required; size-capped). Does not send to self.
- summary: optional short UI label.
- task_id / urgency: optional contract fields applied to each copy (no require_ack
  on broadcast — use agent_message for ack/request contracts).
- Delivers N-1 copies (one per other teammate) via the same mailbox path as
  agent_message. Per-recipient status is returned; some may reject (closed).
- Out-of-team is impossible by construction (roster-scoped).
- Prefer agent_message for a single known recipient. Use broadcast sparingly for
  true team-wide facts — avoid chatty fan-out. Prefer [child.completed] when you
  only need terminal results from owned children.
- Available to lead and children (not stripped at depth ceiling).`
}

func (agentBroadcastTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"body": {"type": "string", "description": "Message text for all other teammates"},
			"summary": {"type": "string", "description": "Optional short UI label"},
			"task_id": {"type": "string", "description": "Optional team_task or delegation id (thread key)"},
			"urgency": {"type": "string", "description": "normal | high | blocker (default normal)"}
		},
		"required": ["body"]
	}`)
}

func (agentBroadcastTool) Execute(ctx context.Context, args json.RawMessage, tc *Context) (Result, error) {
	var a struct {
		Body    string `json:"body"`
		Summary string `json:"summary"`
		TaskID  string `json:"task_id"`
		Urgency string `json:"urgency"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{}, fmt.Errorf("invalid arguments: %w", err)
	}
	body := strings.TrimSpace(a.Body)
	summary := clampAgentMessageSummary(a.Summary)
	if body == "" {
		return Result{}, fmt.Errorf("body is required")
	}
	if n := utf8.RuneCountInString(body); n > MaxAgentMessageBodyRunes {
		return Result{}, fmt.Errorf("body exceeds %d runes (%d)", MaxAgentMessageBodyRunes, n)
	}
	urgency := strings.ToLower(strings.TrimSpace(a.Urgency))
	if urgency != "" {
		switch urgency {
		case "normal", "high", "blocker":
		default:
			return Result{}, fmt.Errorf("urgency must be normal, high, or blocker")
		}
	}
	if err := tc.Ask(ctx, AskRequest{Permission: "agent_broadcast", Patterns: []string{"*"}, Always: []string{"*"}}); err != nil {
		return Result{}, err
	}
	if tc.AgentBroadcast == nil {
		return Result{}, fmt.Errorf("agent_broadcast is not available")
	}
	res, err := tc.AgentBroadcast(ctx, AgentBroadcastRequest{
		Body: body, Summary: summary,
		TaskID: strings.TrimSpace(a.TaskID), Urgency: urgency,
	})
	if err != nil {
		return Result{}, err
	}
	out, _ := json.Marshal(res)
	title := fmt.Sprintf("agent_broadcast %d/%d", res.Delivered, res.Delivered+res.Rejected)
	if res.Delivered == 0 && res.Rejected > 0 {
		return Result{Title: title, Output: string(out)}, fmt.Errorf("broadcast delivered to 0 teammates (%d rejected)", res.Rejected)
	}
	return Result{Title: title, Output: string(out)}, nil
}

// MaxAgentMessageSummaryRunes caps optional summary labels.
const MaxAgentMessageSummaryRunes = 120

func clampAgentMessageSummary(s string) string {
	s = strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
	if s == "" {
		return ""
	}
	if utf8.RuneCountInString(s) <= MaxAgentMessageSummaryRunes {
		return s
	}
	runes := []rune(s)
	return string(runes[:MaxAgentMessageSummaryRunes])
}
