package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type agentThreadTool struct{}

func NewAgentThread() Tool { return agentThreadTool{} }

func (agentThreadTool) Name() string { return "agent_thread" }

func (agentThreadTool) Contract() Contract {
	return staticContract(SideEffectRead, IdempotencySafeRetry)
}

func (agentThreadTool) Description() string {
	return `Read the coordination-contract message thread for a task_id or delegation id.

- task_id: team_task board id or delegation id used when sending agent_message
  with task_id (required).
- limit: max messages to return (default 20, max 64); returns the newest slice.
- Team-scoped: only members of the implicit session team can read. Threads are
  isolated per task_id (no cross-task leakage). Cross-team is impossible.
- Prefer agent_thread + require_ack contracts over chatty status loops.
- Available to lead and children (not stripped at depth ceiling).`
}

func (agentThreadTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"task_id": {"type": "string", "description": "team_task or delegation id (thread key)"},
			"limit": {"type": "integer", "description": "Max messages (default 20, max 64)"}
		},
		"required": ["task_id"]
	}`)
}

func (agentThreadTool) Execute(ctx context.Context, args json.RawMessage, tc *Context) (Result, error) {
	var a struct {
		TaskID string `json:"task_id"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{}, fmt.Errorf("invalid arguments: %w", err)
	}
	taskID := strings.TrimSpace(a.TaskID)
	if taskID == "" {
		return Result{}, fmt.Errorf("task_id is required")
	}
	if a.Limit < 0 {
		return Result{}, fmt.Errorf("limit must be >= 0")
	}
	if err := tc.Ask(ctx, AskRequest{Permission: "agent_thread", Patterns: []string{taskID}, Always: []string{"*"}}); err != nil {
		return Result{}, err
	}
	if tc.AgentThread == nil {
		return Result{}, fmt.Errorf("agent_thread is not available")
	}
	res, err := tc.AgentThread(ctx, AgentThreadRequest{TaskID: taskID, Limit: a.Limit})
	if err != nil {
		return Result{}, err
	}
	out, _ := json.Marshal(res)
	return Result{
		Title:  fmt.Sprintf("agent_thread %s %d", shortID(taskID), len(res.Messages)),
		Output: string(out),
	}, nil
}
