package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	taskReadDefaultLimit = 20
	taskReadMaxLimit     = 100
)

type taskStatusTool struct{}
type taskReadTool struct{}
type taskMessageTool struct{}
type taskInterruptTool struct{}

func NewTaskStatus() Tool    { return taskStatusTool{} }
func NewTaskRead() Tool      { return taskReadTool{} }
func NewTaskMessage() Tool   { return taskMessageTool{} }
func NewTaskInterrupt() Tool { return taskInterruptTool{} }

func (taskStatusTool) Name() string { return "task_status" }

func (taskStatusTool) Description() string {
	return `Inspect live or terminal status of an owned child session started by task.

- Input session_id from a prior task result (or child.completed notice).
- Returns state (starting|working|needs_attention|completed|failed|canceled|unknown),
  elapsed time, current tool, optional recent activity, and terminal_summary when done.
- Prefer this only when you need an intermediate check. Do not poll every second —
  child.completed is pushed automatically when the child finishes.
- Cannot access sessions you do not own.`
}

func (taskStatusTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"session_id": {"type": "string", "description": "Child session id returned by task"},
			"include_recent": {"type": "boolean", "description": "Include latest_activity lines (default false)"}
		},
		"required": ["session_id"]
	}`)
}

func (taskStatusTool) Execute(ctx context.Context, args json.RawMessage, tc *Context) (Result, error) {
	var a struct {
		SessionID     string `json:"session_id"`
		IncludeRecent bool   `json:"include_recent"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{}, fmt.Errorf("invalid arguments: %w", err)
	}
	id := strings.TrimSpace(a.SessionID)
	if id == "" {
		return Result{}, fmt.Errorf("session_id is required")
	}
	if err := tc.Ask(ctx, AskRequest{Permission: "task_status", Patterns: []string{id}, Always: []string{"*"}}); err != nil {
		return Result{}, err
	}
	if tc.TaskStatus == nil {
		return Result{}, fmt.Errorf("task_status is not available")
	}
	res, err := tc.TaskStatus(ctx, TaskStatusRequest{SessionID: id, IncludeRecent: a.IncludeRecent})
	if err != nil {
		return Result{}, err
	}
	out, _ := json.Marshal(map[string]any{
		"session_id":       res.SessionID,
		"state":            res.State,
		"elapsed":          res.Elapsed,
		"current_tool":     res.CurrentTool,
		"latest_activity":  res.LatestActivity,
		"terminal_summary": nullIfEmpty(res.TerminalSummary),
	})
	return Result{
		Title:  "task_status " + shortID(id) + " " + res.State,
		Output: string(out),
	}, nil
}

func (taskReadTool) Name() string { return "task_read" }

func (taskReadTool) Description() string {
	return `Read a bounded recent slice of an owned child's transcript/events.

- Never dumps an unbounded JSONL log. Default last/limit is small (max 100).
- Use offset+limit for forward pages, or last=N for the newest N entries.
- include_tools (default true) and include_reasoning control row kinds.
- Prefer task_status for a quick pulse; use task_read when you need content.`
}

func (taskReadTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"session_id": {"type": "string", "description": "Child session id"},
			"offset": {"type": "integer", "description": "0-based start index into retained events (ignored when last > 0)"},
			"limit": {"type": "integer", "description": "Max entries to return (default 20, max 100)"},
			"last": {"type": "integer", "description": "When > 0, return the last N entries instead of offset paging"},
			"include_tools": {"type": "boolean", "description": "Include tool begin/end rows (default true)"},
			"include_reasoning": {"type": "boolean", "description": "Include reasoning deltas (default false)"}
		},
		"required": ["session_id"]
	}`)
}

func (taskReadTool) Execute(ctx context.Context, args json.RawMessage, tc *Context) (Result, error) {
	var a struct {
		SessionID        string `json:"session_id"`
		Offset           int    `json:"offset"`
		Limit            int    `json:"limit"`
		Last             int    `json:"last"`
		IncludeTools     *bool  `json:"include_tools"`
		IncludeReasoning bool   `json:"include_reasoning"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{}, fmt.Errorf("invalid arguments: %w", err)
	}
	id := strings.TrimSpace(a.SessionID)
	if id == "" {
		return Result{}, fmt.Errorf("session_id is required")
	}
	if err := tc.Ask(ctx, AskRequest{Permission: "task_read", Patterns: []string{id}, Always: []string{"*"}}); err != nil {
		return Result{}, err
	}
	if tc.TaskRead == nil {
		return Result{}, fmt.Errorf("task_read is not available")
	}
	includeTools := true
	if a.IncludeTools != nil {
		includeTools = *a.IncludeTools
	}
	res, err := tc.TaskRead(ctx, TaskReadRequest{
		SessionID:        id,
		Offset:           a.Offset,
		Limit:            a.Limit,
		Last:             a.Last,
		IncludeTools:     includeTools,
		IncludeReasoning: a.IncludeReasoning,
	})
	if err != nil {
		return Result{}, err
	}
	out, _ := json.Marshal(res)
	return Result{
		Title:  fmt.Sprintf("task_read %s %d/%d", shortID(id), len(res.Entries), res.Total),
		Output: string(out),
	}, nil
}

func (taskMessageTool) Name() string { return "task_message" }

func (taskMessageTool) Description() string {
	return `Send additional guidance to a running owned child session.

- Delivered at a safe boundary: accepted immediately when the child is idle,
  or queued until the active child turn finishes (does not corrupt the live request).
- Returns status queued|accepted|rejected with the child's current state.
- Cannot widen child permissions. Unknown/closed children are rejected.`
}

func (taskMessageTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"session_id": {"type": "string", "description": "Child session id"},
			"text": {"type": "string", "description": "Guidance for the child agent"}
		},
		"required": ["session_id", "text"]
	}`)
}

func (taskMessageTool) Execute(ctx context.Context, args json.RawMessage, tc *Context) (Result, error) {
	var a struct {
		SessionID string `json:"session_id"`
		Text      string `json:"text"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{}, fmt.Errorf("invalid arguments: %w", err)
	}
	id := strings.TrimSpace(a.SessionID)
	text := strings.TrimSpace(a.Text)
	if id == "" {
		return Result{}, fmt.Errorf("session_id is required")
	}
	if text == "" {
		return Result{}, fmt.Errorf("text is required")
	}
	if err := tc.Ask(ctx, AskRequest{Permission: "task_message", Patterns: []string{id}, Always: []string{"*"}}); err != nil {
		return Result{}, err
	}
	if tc.TaskMessage == nil {
		return Result{}, fmt.Errorf("task_message is not available")
	}
	res, err := tc.TaskMessage(ctx, TaskMessageRequest{SessionID: id, Text: text})
	if err != nil {
		return Result{}, err
	}
	out, _ := json.Marshal(map[string]any{
		"session_id": res.SessionID,
		"status":     res.Status,
		"state":      res.State,
		"detail":     res.Detail,
	})
	title := "task_message " + shortID(id) + " " + res.Status
	if res.Status == "rejected" {
		return Result{Title: title, Output: string(out)}, fmt.Errorf("%s", res.Detail)
	}
	return Result{Title: title, Output: string(out)}, nil
}

func (taskInterruptTool) Name() string { return "task_interrupt" }

func (taskInterruptTool) Description() string {
	return `Cancel a running owned child session by id.

- Idempotent: repeating on an already finished child returns its terminal state.
- Only affects that child (not siblings or unrelated sessions).
- Prefer this over killing processes from bash.`
}

func (taskInterruptTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"session_id": {"type": "string", "description": "Child session id to cancel"}
		},
		"required": ["session_id"]
	}`)
}

func (taskInterruptTool) Execute(ctx context.Context, args json.RawMessage, tc *Context) (Result, error) {
	var a struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{}, fmt.Errorf("invalid arguments: %w", err)
	}
	id := strings.TrimSpace(a.SessionID)
	if id == "" {
		return Result{}, fmt.Errorf("session_id is required")
	}
	if err := tc.Ask(ctx, AskRequest{Permission: "task_interrupt", Patterns: []string{id}, Always: []string{"*"}}); err != nil {
		return Result{}, err
	}
	if tc.TaskInterrupt == nil {
		return Result{}, fmt.Errorf("task_interrupt is not available")
	}
	res, err := tc.TaskInterrupt(ctx, TaskInterruptRequest{SessionID: id})
	if err != nil {
		return Result{}, err
	}
	out, _ := json.Marshal(map[string]any{
		"session_id": res.SessionID,
		"state":      res.State,
		"detail":     res.Detail,
	})
	return Result{
		Title:  "task_interrupt " + shortID(id) + " " + res.State,
		Output: string(out),
	}, nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// ClampTaskReadLimit normalizes limit to (0, taskReadMaxLimit].
func ClampTaskReadLimit(limit int) int {
	if limit <= 0 {
		return taskReadDefaultLimit
	}
	if limit > taskReadMaxLimit {
		return taskReadMaxLimit
	}
	return limit
}
