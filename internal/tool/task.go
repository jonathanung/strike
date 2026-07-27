package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type taskTool struct{}

func NewTask() Tool { return taskTool{} }

func (taskTool) Name() string { return "task" }

func (taskTool) Description() string {
	return `Delegate a bounded subtask to a child agent with its own context.

- Returns immediately after the child starts (does not block this turn).
- Result includes the child session id; a later [child.completed] message carries the terminal summary automatically.
- Do not sleep-poll waiting for the child — continue other work or end the turn; completion is event-driven.
- Optional agent selects a persona (defaults to the current agent). Built-in names include:
  explore (read-only search), general (multi-step), commit (git commits only),
  reviewer (read-only review), tester (run make test/vet/build), debugger (root-cause),
  build (default coding), plan (read-only planning).
- Optional model pins the child's model (bare id on the current provider, or provider/model).
  Must be a catalog id for that provider (same list as /model). Omit to inherit the parent model.
- Nested task depth is bounded by MaxChildDepth (default 1: children cannot nest).
- Use task_status/task_read/task_message/task_interrupt with the session id for
  intermediate control — do not sleep-poll for completion.
- Use for scoped work that benefits from a fresh message history.`
}

func (taskTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"prompt": {"type": "string", "description": "The subtask instructions for the child agent"},
			"agent": {"type": "string", "description": "Optional agent persona: explore, general, commit, reviewer, tester, debugger, build, plan, or a user-defined name (default: current agent)"},
			"model": {"type": "string", "description": "Optional model id for the child (bare id on the current provider, or provider/model). Must be in the shared model catalog; omit to inherit the parent model"}
		},
		"required": ["prompt"]
	}`)
}

type taskArgs struct {
	Prompt string `json:"prompt"`
	Agent  string `json:"agent"`
	Model  string `json:"model"`
}

func (taskTool) Execute(ctx context.Context, args json.RawMessage, tc *Context) (Result, error) {
	var a taskArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{}, fmt.Errorf("invalid arguments: %w", err)
	}
	if strings.TrimSpace(a.Prompt) == "" {
		return Result{}, fmt.Errorf("prompt is empty")
	}
	if err := tc.Ask(ctx, AskRequest{Permission: "task", Patterns: []string{"*"}, Always: []string{"*"}}); err != nil {
		return Result{}, err
	}
	if tc.SpawnTask == nil {
		return Result{}, fmt.Errorf("task is not available")
	}
	res, err := tc.SpawnTask(ctx, TaskRequest{Prompt: a.Prompt, Agent: a.Agent, Model: a.Model})
	if err != nil {
		return Result{}, err
	}
	out := res.Output
	title := "task"
	if res.SessionID != "" {
		title = "task " + shortID(res.SessionID)
	}
	meta := taskMetadata(res)
	switch res.Status {
	case "started", "completed":
		return Result{Title: title, Output: out, Metadata: meta}, nil
	case "failed", "canceled":
		if out == "" {
			out = "task " + res.Status
		}
		return Result{Title: title, Output: out, Metadata: meta}, fmt.Errorf("%s", out)
	default:
		if out == "" {
			out = "task failed"
		}
		return Result{Title: title, Output: out, Metadata: meta}, fmt.Errorf("%s", out)
	}
}

func shortID(id string) string {
	id = strings.TrimSpace(id)
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

func taskMetadata(res TaskResult) json.RawMessage {
	if res.SessionID == "" && res.Status == "" {
		return nil
	}
	b, err := json.Marshal(map[string]string{
		"sessionId": res.SessionID,
		"status":    res.Status,
	})
	if err != nil {
		return nil
	}
	return b
}
