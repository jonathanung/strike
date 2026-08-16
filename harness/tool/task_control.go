package tool

import (
	"context"
	"encoding/json"
	"fmt"
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

func (taskStatusTool) Contract() Contract {
	return staticContract(SideEffectNone, IdempotencySafeRetry)
}

func (taskStatusTool) Description() string {
	return `Compatibility shim: inspect live or terminal status of an owned child.

Prefer progressive task:
  task({action:"status", id:"…" | session_id:"…", include_recent?: bool})

Same payload (state, handoff, verification, lifecycle, budget, …). One-off
pulse only — do not busy-poll; prefer task action=wait or [child.completed].
Usage is telemetry-counted toward deprecation.`
}

func (taskStatusTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"session_id": {"type": "string", "description": "Child session id or stable name alias from task"},
			"include_recent": {"type": "boolean", "description": "Include latest_activity lines (default false)"}
		},
		"required": ["session_id"]
	}`)
}

func (taskStatusTool) Execute(ctx context.Context, args json.RawMessage, tc *Context) (Result, error) {
	var a progressiveArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{}, fmt.Errorf("invalid arguments: %w", err)
	}
	a.Action = ProgressiveStatus
	// Keep historical permission name.
	return executeProgressive(ctx, CompatToolTaskStatus, "task_status", a, tc)
}

func (taskReadTool) Name() string { return "task_read" }

func (taskReadTool) Contract() Contract {
	return staticContract(SideEffectRead, IdempotencySafeRetry)
}

func (taskReadTool) Description() string {
	return `Compatibility shim: read a bounded recent slice of an owned child's transcript.

Prefer progressive task:
  task({action:"read", id:"…", offset?, limit?, last?, include_tools?, include_reasoning?})

Never dumps an unbounded JSONL log. Default last/limit is small (max 100).
Usage is telemetry-counted toward deprecation.`
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
	var a progressiveArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{}, fmt.Errorf("invalid arguments: %w", err)
	}
	a.Action = ProgressiveRead
	return executeProgressive(ctx, CompatToolTaskRead, "task_read", a, tc)
}

func (taskMessageTool) Name() string { return "task_message" }

func (taskMessageTool) Contract() Contract {
	return staticContract(SideEffectExternal, IdempotencyUnsafe)
}

func (taskMessageTool) Description() string {
	return `Compatibility shim: send guidance to a running owned child (parent→child steer).

Prefer progressive task:
  task({action:"message", id:"…", text:"…"})

Delivered at a safe boundary. Parent→owned-child only — for peer messaging use
agent_message / agent_broadcast. Usage is telemetry-counted toward deprecation.`
}

func (taskMessageTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"session_id": {"type": "string", "description": "Child session id or stable name alias"},
			"text": {"type": "string", "description": "Guidance for the child agent"}
		},
		"required": ["session_id", "text"]
	}`)
}

func (taskMessageTool) Execute(ctx context.Context, args json.RawMessage, tc *Context) (Result, error) {
	var a progressiveArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{}, fmt.Errorf("invalid arguments: %w", err)
	}
	a.Action = ProgressiveMessage
	return executeProgressive(ctx, CompatToolTaskMessage, "task_message", a, tc)
}

func (taskInterruptTool) Name() string { return "task_interrupt" }

func (taskInterruptTool) Contract() Contract {
	// Interrupt is idempotent once the child is already stopping/stopped.
	return staticContract(SideEffectProcess, IdempotencyConditional)
}

func (taskInterruptTool) Description() string {
	return `Compatibility shim: cancel a running owned child session by id.

Prefer progressive task:
  task({action:"cancel", id:"…"})

Idempotent. Only affects that child. Usage is telemetry-counted toward deprecation.`
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
	var a progressiveArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{}, fmt.Errorf("invalid arguments: %w", err)
	}
	a.Action = ProgressiveCancel
	return executeProgressive(ctx, CompatToolTaskInterrupt, "task_interrupt", a, tc)
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
