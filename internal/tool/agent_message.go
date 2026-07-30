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

type agentMessageTool struct{}
type agentBroadcastTool struct{}

func NewAgentMessage() Tool   { return agentMessageTool{} }
func NewAgentBroadcast() Tool { return agentBroadcastTool{} }

func (agentMessageTool) Name() string { return "agent_message" }

func (agentMessageTool) Description() string {
	return `Send a message to one teammate on the implicit session team.

- to: teammate session_id (from agent_roster / task result). Stable name aliases
  resolve when set on the roster.
- body: message text (required; size-capped).
- summary: optional short label for UI/debug (not a substitute for body).
- Delivered at a safe boundary on the recipient (tool-round / idle turn) —
  never corrupts an in-flight tool call.
- Sender and recipient must share the same session team; out-of-team fails closed.
- Prefer this for peer coordination while both are running. Prefer waiting for
  [child.completed] when you only need the final result of a child you own.
- Available to lead and children (not stripped at depth ceiling).
- task_message remains parent→owned-child guidance; use agent_message for any
  teammate (including child→child and child→lead).`
}

func (agentMessageTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"to": {"type": "string", "description": "Teammate session_id or stable name"},
			"body": {"type": "string", "description": "Message text for the teammate"},
			"summary": {"type": "string", "description": "Optional short UI label"}
		},
		"required": ["to", "body"]
	}`)
}

func (agentMessageTool) Execute(ctx context.Context, args json.RawMessage, tc *Context) (Result, error) {
	var a struct {
		To      string `json:"to"`
		Body    string `json:"body"`
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{}, fmt.Errorf("invalid arguments: %w", err)
	}
	to := strings.TrimSpace(a.To)
	body := strings.TrimSpace(a.Body)
	summary := clampAgentMessageSummary(a.Summary)
	if to == "" {
		return Result{}, fmt.Errorf("to is required")
	}
	if body == "" {
		return Result{}, fmt.Errorf("body is required")
	}
	if n := utf8.RuneCountInString(body); n > MaxAgentMessageBodyRunes {
		return Result{}, fmt.Errorf("body exceeds %d runes (%d)", MaxAgentMessageBodyRunes, n)
	}
	if err := tc.Ask(ctx, AskRequest{Permission: "agent_message", Patterns: []string{to}, Always: []string{"*"}}); err != nil {
		return Result{}, err
	}
	if tc.AgentMessage == nil {
		return Result{}, fmt.Errorf("agent_message is not available")
	}
	res, err := tc.AgentMessage(ctx, AgentMessageRequest{To: to, Body: body, Summary: summary})
	if err != nil {
		return Result{}, err
	}
	out, _ := json.Marshal(res)
	title := "agent_message " + shortID(res.To) + " " + res.Status
	if res.To == "" {
		title = "agent_message " + shortID(to) + " " + res.Status
	}
	if res.Status == "rejected" {
		return Result{Title: title, Output: string(out)}, fmt.Errorf("%s", res.Detail)
	}
	return Result{Title: title, Output: string(out)}, nil
}

func (agentBroadcastTool) Name() string { return "agent_broadcast" }

func (agentBroadcastTool) Description() string {
	return `Broadcast a message to every other teammate on the session team.

- body: message text (required; size-capped). Does not send to self.
- summary: optional short UI label.
- Delivers N-1 copies (one per other teammate) via the same mailbox path as
  agent_message. Per-recipient status is returned; some may reject (closed).
- Out-of-team is impossible by construction (roster-scoped).
- Prefer agent_message for a single known recipient. Prefer [child.completed]
  when you only need terminal results from owned children.
- Available to lead and children (not stripped at depth ceiling).`
}

func (agentBroadcastTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"body": {"type": "string", "description": "Message text for all other teammates"},
			"summary": {"type": "string", "description": "Optional short UI label"}
		},
		"required": ["body"]
	}`)
}

func (agentBroadcastTool) Execute(ctx context.Context, args json.RawMessage, tc *Context) (Result, error) {
	var a struct {
		Body    string `json:"body"`
		Summary string `json:"summary"`
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
	if err := tc.Ask(ctx, AskRequest{Permission: "agent_broadcast", Patterns: []string{"*"}, Always: []string{"*"}}); err != nil {
		return Result{}, err
	}
	if tc.AgentBroadcast == nil {
		return Result{}, fmt.Errorf("agent_broadcast is not available")
	}
	res, err := tc.AgentBroadcast(ctx, AgentBroadcastRequest{Body: body, Summary: summary})
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
