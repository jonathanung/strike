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
	return `Delegate a bounded subtask to a foreground child agent with its own context.

- Blocks until the child finishes; returns one terminal result.
- Optional agent selects a persona (defaults to the current agent).
- Children cannot spawn further tasks (depth limit 1).
- Use for scoped work that benefits from a fresh message history.`
}

func (taskTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"prompt": {"type": "string", "description": "The subtask instructions for the child agent"},
			"agent": {"type": "string", "description": "Optional agent persona name (default: current agent)"}
		},
		"required": ["prompt"]
	}`)
}

type taskArgs struct {
	Prompt string `json:"prompt"`
	Agent  string `json:"agent"`
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
	res, err := tc.SpawnTask(ctx, TaskRequest{Prompt: a.Prompt, Agent: a.Agent})
	if err != nil {
		return Result{}, err
	}
	out := res.Output
	switch res.Status {
	case "completed":
		return Result{Title: "task", Output: out}, nil
	case "failed", "canceled":
		if out == "" {
			out = "task " + res.Status
		}
		return Result{Title: "task", Output: out}, fmt.Errorf("%s", out)
	default:
		if out == "" {
			out = "task failed"
		}
		return Result{Title: "task", Output: out}, fmt.Errorf("%s", out)
	}
}
