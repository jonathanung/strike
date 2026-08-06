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
	return `Block until an owned child task event matches, or until timeout.

- Prefer wait over sleep-polling task_status for subagent completion or blockers.
- events: one or more of task.done, task.failed, task.canceled, task.blocked
  (aliases: task.completed→done, needs_attention→blocked). Wait-any: first match wins.
- optional session_id limits the wait to one owned child (session id or name alias).
  Omit to match any owned child.
- timeout_seconds is required (0 < t ≤ 300). Returns structured outcome:
  matched | timeout | canceled — never hangs past the timeout or parent interrupt.
- On matched terminal events, includes handoff when available (same schema as task_status).
- task.blocked fires when a child needs_attention (permission or user question).
- Only owned children are observable; unknown/foreign sessions are rejected.
- Emits wait.started / wait.resolved on the session event stream for UI/debug.
- Does not replace [child.completed] injection — that still arrives for the model;
  wait is the explicit orchestration primitive when you must synchronize mid-turn.`
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

type waitArgs struct {
	Events         []string `json:"events"`
	SessionID      string   `json:"session_id"`
	TimeoutSeconds float64  `json:"timeout_seconds"`
}

func (waitTool) Execute(ctx context.Context, args json.RawMessage, tc *Context) (Result, error) {
	var a waitArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{}, fmt.Errorf("invalid arguments: %w", err)
	}
	if a.TimeoutSeconds <= 0 || a.TimeoutSeconds > waitMaxSeconds {
		return Result{}, fmt.Errorf("timeout_seconds must be in (0, 300]")
	}
	canonical, err := NormalizeWaitEvents(a.Events)
	if err != nil {
		return Result{}, err
	}
	id := strings.TrimSpace(a.SessionID)
	pat := id
	if pat == "" {
		pat = "*"
	}
	if err := tc.Ask(ctx, AskRequest{Permission: "wait", Patterns: []string{pat}, Always: []string{"*"}}); err != nil {
		return Result{}, err
	}
	if tc.Wait == nil {
		return Result{}, fmt.Errorf("wait is not available")
	}
	res, err := tc.Wait(ctx, WaitRequest{
		Events:         canonical,
		SessionID:      id,
		TimeoutSeconds: a.TimeoutSeconds,
	})
	if err != nil {
		return Result{}, err
	}
	out, _ := json.Marshal(res)
	title := "wait " + res.Outcome
	if res.Event != "" {
		title += " " + res.Event
	}
	if res.SessionID != "" {
		title += " " + shortID(res.SessionID)
	}
	return Result{Title: title, Output: string(out)}, nil
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
