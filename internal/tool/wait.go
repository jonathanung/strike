package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const waitMaxSeconds = 300.0

// Canonical wait event kinds (orchestration predicates).
const (
	WaitEventTaskDone     = "task.done"
	WaitEventTaskFailed   = "task.failed"
	WaitEventTaskCanceled = "task.canceled"
	WaitEventTaskBlocked  = "task.blocked"
)

// Wait outcome labels (mirror protocol.WaitOutcome*).
const (
	WaitOutcomeMatched  = "matched"
	WaitOutcomeTimeout  = "timeout"
	WaitOutcomeCanceled = "canceled"
)

type waitTool struct{}

func NewWait() Tool { return waitTool{} }

func (waitTool) Name() string { return "wait" }

func (waitTool) Description() string {
	return `Compatibility shim: block until an owned child task event matches, or until timeout.

Prefer progressive task:
  task({action:"wait", events:[…], timeout_seconds:N, id?: "…"})

- events: one or more of task.done, task.failed, task.canceled, task.blocked
  (aliases: task.completed→done, needs_attention→blocked). Wait-any: first match wins.
- optional session_id limits the wait to one owned child (session id or name alias).
- timeout_seconds is required (0 < t ≤ 300). Outcomes: matched | timeout | canceled.
- On matched terminal events, includes handoff when available.
- Only owned children; emits wait.started / wait.resolved.
- Does not replace [child.completed] injection. Usage is telemetry-counted toward deprecation.`
}

func (waitTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"events": {
				"type": "array",
				"items": {"type": "string"},
				"description": "Event kinds to wait for (wait-any): task.done, task.failed, task.canceled, task.blocked"
			},
			"session_id": {
				"type": "string",
				"description": "Optional owned child session id or name alias; omit for any owned child"
			},
			"timeout_seconds": {
				"type": "number",
				"description": "Max seconds to wait (greater than 0, maximum 300)"
			}
		},
		"required": ["events", "timeout_seconds"]
	}`)
}

func (waitTool) Execute(ctx context.Context, args json.RawMessage, tc *Context) (Result, error) {
	var a progressiveArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{}, fmt.Errorf("invalid arguments: %w", err)
	}
	a.Action = ProgressiveWait
	// Keep historical permission name "wait".
	return executeProgressive(ctx, CompatToolWait, "wait", a, tc)
}

// NormalizeWaitEvents maps aliases to canonical kinds and rejects unknowns.
// Empty input is an error. Order is preserved; duplicates are dropped.
func NormalizeWaitEvents(events []string) ([]string, error) {
	if len(events) == 0 {
		return nil, fmt.Errorf("events is required (at least one of task.done, task.failed, task.canceled, task.blocked)")
	}
	seen := make(map[string]struct{}, len(events))
	out := make([]string, 0, len(events))
	for _, raw := range events {
		kind, ok := canonicalizeWaitEvent(raw)
		if !ok {
			return nil, fmt.Errorf("unknown wait event %q (want task.done, task.failed, task.canceled, task.blocked)", strings.TrimSpace(raw))
		}
		if _, dup := seen[kind]; dup {
			continue
		}
		seen[kind] = struct{}{}
		out = append(out, kind)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("events is required (at least one of task.done, task.failed, task.canceled, task.blocked)")
	}
	return out, nil
}

func canonicalizeWaitEvent(raw string) (string, bool) {
	s := strings.ToLower(strings.TrimSpace(raw))
	switch s {
	case WaitEventTaskDone, "task.completed", "done", "completed":
		return WaitEventTaskDone, true
	case WaitEventTaskFailed, "failed":
		return WaitEventTaskFailed, true
	case WaitEventTaskCanceled, "task.cancelled", "canceled", "cancelled":
		return WaitEventTaskCanceled, true
	case WaitEventTaskBlocked, "task.needs_attention", "needs_attention", "blocked":
		return WaitEventTaskBlocked, true
	default:
		return "", false
	}
}
