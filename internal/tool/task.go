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
- Result includes the child session id; a later [child.completed] carries the terminal
  summary (finished work product). Mid-flight coordination uses peer messages, not polling.
- Do not sleep-poll or busy-loop task_status waiting for the child — continue other work
  or end the turn; completion and peer inbox traffic are event-driven.
- Optional name is a stable teammate alias unique on the session team (e.g. explorer).
  Addressable in agent_roster and messaging tools; session_id still works when omitted.
- Optional agent selects a persona (defaults to the current agent). Built-in names include:
  explore (read-only search), general (multi-step), commit (git commits only),
  reviewer (read-only review), tester (run make test/vet/build), debugger (root-cause),
  build (default coding), plan (read-only planning).
- Optional model pins the child's model (bare id on the current provider, or provider/model).
  Must be a catalog id for that provider (same list as /model). Omit to inherit the parent model.
- Optional effort pins the child's reasoning effort (off|low|medium|high|xhigh|max).
  Omit to inherit the parent dial (agent effort pins still apply). When set, wins over agent pins.
- Nested task depth is bounded by MaxChildDepth (default 1: children cannot nest). Bound fan-out.
- Parent→owned-child control: task_status / task_read / task_message / task_interrupt
  (session id or name). Peer/team chat (any teammate, including child→lead and child→child):
  agent_message / agent_broadcast — not a parent-only control plane.
- Prefer agent_message for mid-flight blockers/handoffs; prefer [child.completed] for the
  finished deliverable. Avoid chatty status ping-pong.
- Use for scoped work that benefits from a fresh message history.`
}

func (taskTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"prompt": {"type": "string", "description": "The subtask instructions for the child agent"},
			"name": {"type": "string", "description": "Optional stable teammate alias unique on this session team (e.g. explorer). Addressable in roster/messaging; omit to use session id only"},
			"agent": {"type": "string", "description": "Optional agent persona: explore, general, commit, reviewer, tester, debugger, build, plan, or a user-defined name (default: current agent)"},
			"model": {"type": "string", "description": "Optional model id for the child (bare id on the current provider, or provider/model). Must be in the shared model catalog; omit to inherit the parent model"},
			"effort": {"type": "string", "description": "Optional reasoning effort for the child: off, low, medium, high, xhigh, or max. Omit to inherit the parent dial"}
		},
		"required": ["prompt"]
	}`)
}

type taskArgs struct {
	Prompt string `json:"prompt"`
	Name   string `json:"name"`
	Agent  string `json:"agent"`
	Model  string `json:"model"`
	Effort string `json:"effort"`
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
	res, err := tc.SpawnTask(ctx, TaskRequest{Prompt: a.Prompt, Name: a.Name, Agent: a.Agent, Model: a.Model, Effort: a.Effort})
	if err != nil {
		return Result{}, err
	}
	out := res.Output
	title := "task"
	if n := strings.TrimSpace(res.Name); n != "" {
		title = "task " + n
	} else if res.SessionID != "" {
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
	if res.SessionID == "" && res.Status == "" && res.Name == "" {
		return nil
	}
	meta := map[string]string{
		"sessionId": res.SessionID,
		"status":    res.Status,
	}
	if n := strings.TrimSpace(res.Name); n != "" {
		meta["name"] = n
	}
	b, err := json.Marshal(meta)
	if err != nil {
		return nil
	}
	return b
}
