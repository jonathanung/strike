package tool

import (
	"context"
	"encoding/json"
	"fmt"
)

type agentRosterTool struct{}

func NewAgentRoster() Tool { return agentRosterTool{} }

func (agentRosterTool) Name() string { return "agent_roster" }

func (agentRosterTool) Description() string {
	return `List teammates on the implicit session team (lead + children).

- Available on the lead and on children (children see lead + siblings).
- Each row includes session_id, optional name, agent persona, state, role,
  started_at, and terminal_summary when finished.
- State matches task_status vocabulary where possible
  (starting|working|needs_attention|completed|failed|canceled|unknown).
- Solo lead returns a single self row. Not available outside a team session.`
}

func (agentRosterTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {},
		"additionalProperties": false
	}`)
}

func (agentRosterTool) Execute(ctx context.Context, args json.RawMessage, tc *Context) (Result, error) {
	if len(args) > 0 && string(args) != "null" && string(args) != "{}" {
		// Allow empty object or null; reject unexpected payloads softly via unmarshal.
		var probe map[string]any
		if err := json.Unmarshal(args, &probe); err != nil {
			return Result{}, fmt.Errorf("invalid arguments: %w", err)
		}
	}
	if err := tc.Ask(ctx, AskRequest{Permission: "agent_roster", Patterns: []string{"*"}, Always: []string{"*"}}); err != nil {
		return Result{}, err
	}
	if tc.AgentRoster == nil {
		return Result{}, fmt.Errorf("agent_roster is not available")
	}
	res, err := tc.AgentRoster(ctx, AgentRosterRequest{})
	if err != nil {
		return Result{}, err
	}
	out, err := json.Marshal(res)
	if err != nil {
		return Result{}, err
	}
	n := len(res.Members)
	title := fmt.Sprintf("agent_roster %d", n)
	if res.LeadID != "" {
		title = fmt.Sprintf("agent_roster %s %d", shortID(res.LeadID), n)
	}
	return Result{Title: title, Output: string(out)}, nil
}
