package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type delegateTool struct{}

// NewDelegate builds the delegate compatibility tool (lifecycle create/get/list/transition).
// Prefer the progressive task API; this tool remains for staged deprecation and
// depth-capped leaves (task is stripped at MaxChildDepth; delegate stays for
// get/list/transition self-report).
func NewDelegate() Tool { return delegateTool{} }

func (delegateTool) Name() string { return "delegate" }

func (delegateTool) Description() string {
	return `Compatibility shim for delegation lifecycle (create/get/list/transition).

Prefer the progressive task API:
  task({prompt})                         — simple spawn
  task({prompt, criteria, deps, …})      — advanced create
  task({action:"get"|"list"|"transition", id, …})

This tool forwards to the same lifecycle runtime. create accepts the full
advanced field set (route, budget, verify, context_bundle, …). At depth
ceiling, task is unavailable — use delegate get/list/transition for leaf
self-report (ownership-gated).

States: queued → working → blocked → review → done (+ failed / canceled).
CAS via expected_version on transition. Deprecated for new parent-side work;
usage is telemetry-counted toward removal.`
}

func (delegateTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"action": {
				"type": "string",
				"enum": ["create", "get", "list", "transition"],
				"description": "Lifecycle operation (prefer task progressive API)"
			},
			"id": {"type": "string", "description": "Delegation id, session id, or name (get/transition)"},
			"prompt": {"type": "string", "description": "Subtask instructions (create)"},
			"name": {"type": "string", "description": "Short unique teammate alias from the assigned task (create). If omitted, derived from the prompt first line"},
			"agent": {"type": "string", "description": "Optional agent persona (create)"},
			"model": {"type": "string", "description": "Optional model pin (create)"},
			"effort": {"type": "string", "description": "Optional effort pin (create)"},
			"route": {"type": "string", "description": "Optional routing mode: auto (create)"},
			"specialty": {"type": "string", "description": "Required specialty for route=auto (create)"},
			"capabilities": {
				"type": "array",
				"items": {"type": "string"},
				"description": "Required capability tags for route=auto (create)"
			},
			"max_cost_class": {"type": "string", "description": "Optional auto-route cost filter (create)"},
			"models": {
				"type": "array",
				"items": {"type": "string"},
				"description": "Optional model allow-list for auto-route (create)"
			},
			"max_concurrent": {"type": "integer", "description": "Optional per-persona concurrency before fallback (create)"},
			"assignee": {"type": "string", "description": "Optional assignee label (create)"},
			"criteria": {
				"type": "array",
				"items": {"type": "string"},
				"description": "Acceptance criteria; non-empty → completion enters review"
			},
			"deps": {
				"type": "array",
				"items": {"type": "string"},
				"description": "Upstream delegation or session ids that must reach done"
			},
			"subscribe": {
				"type": "array",
				"items": {"type": "string"},
				"description": "Notify owner on these states: blocked|review|done|failed|canceled|working|queued"
			},
			"verify": {
				"type": "array",
				"description": "Optional independent completion gates (cmd|schema|path)",
				"items": {
					"type": "object",
					"properties": {
						"kind": {"type": "string"},
						"value": {"type": "string"},
						"description": {"type": "string"}
					},
					"required": ["kind", "value"]
				}
			},
			"budget": {
				"type": "object",
				"description": "Optional per-child resource limits (same as task.budget)",
				"properties": {
					"max_wall_clock_s": {"type": "integer"},
					"max_tokens": {"type": "integer"},
					"max_cost_usd": {"type": "number"},
					"max_tool_calls": {"type": "integer"},
					"max_dangerous_tools": {"type": "integer"},
					"stall_after_s": {"type": "integer"},
					"loop_detect_n": {"type": "integer"}
				},
				"additionalProperties": false
			},
			"context_bundle": {
				"type": "object",
				"description": "Optional sealed context package (same shape as task.context_bundle)"
			},
			"force_delegate": {
				"type": "boolean",
				"description": "Override soft local-prefer policy on create (same as task.force_delegate)"
			},
			"state": {
				"type": "string",
				"enum": ["queued", "working", "blocked", "review", "done", "failed", "canceled"],
				"description": "Target lifecycle state (transition)"
			},
			"reason": {"type": "string", "description": "Optional block/cancel reason (transition)"},
			"expected_version": {
				"type": "integer",
				"description": "CAS token from a prior get/list/create; omit or 0 to skip"
			}
		},
		"required": ["action"]
	}`)
}

func (delegateTool) Execute(ctx context.Context, args json.RawMessage, tc *Context) (Result, error) {
	var a progressiveArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{}, fmt.Errorf("invalid arguments: %w", err)
	}
	action := strings.ToLower(strings.TrimSpace(a.Action))
	if action == "" {
		return Result{}, fmt.Errorf("action is required")
	}
	switch action {
	case ProgressiveCreate, ProgressiveGet, ProgressiveList, ProgressiveTransition:
		// ok
	default:
		return Result{}, fmt.Errorf("action must be create, get, list, or transition")
	}
	// Compat path keeps historical permission name "delegate".
	return executeProgressive(ctx, CompatToolDelegate, "delegate", a, tc)
}
