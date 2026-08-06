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

func (taskTool) Contract() Contract {
	return staticContract(SideEffectProcess, IdempotencyUnsafe)
}

func (taskTool) Description() string {
	return `Progressive delegation API: spawn a child agent and manage its lifecycle.

Simple path (prompt only):
  task({prompt: "…"})
  Returns immediately after the child starts. A later [child.completed] carries
  structured handoff JSON (summary, files_changed, verification, findings,
  blockers, recommended_next_action). Mid-flight coordination uses peer messages.

Advanced create fields (all optional): name, agent, model, effort, route/specialty/
capabilities, criteria[], deps[], subscribe[], assignee, verify[], budget,
context_bundle (goal/paths/artifacts/constraints). Same lifecycle runtime as
plain spawn — no second path.

Actions (optional action=; omit + prompt ⇒ create):
  create     — spawn (default). Nested depth bounded by MaxChildDepth.
  get        — lifecycle snapshot by id (delegation id, session id, or name)
  list       — all delegations on this session team
  status     — live/terminal pulse + handoff/budget/lifecycle fields
  read       — bounded child transcript slice
  message    — parent→owned-child steer (not peer chat; use agent_message)
  transition — lifecycle move with optional expected_version CAS
  cancel     — interrupt owned child (idempotent)
  wait       — block on task.done/failed/canceled/blocked with timeout

States: queued → working → blocked → review → done (+ failed / canceled).
When criteria or verify gates are set, implementer-done is not final completed
until gates pass (else blocked/review). Do not busy-poll status — prefer wait
or [child.completed]. Prefer agent_message for mid-flight blockers.

Identity: id or session_id (delegation id, session id, or stable name alias).
team_task remains the shared claim board; plan_delegate is the plan-section
wrapper. Legacy tools (delegate, task_status, task_read, task_message,
task_interrupt, wait) are compatibility shims over this API.`
}

func (taskTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"action": {
				"type": "string",
				"enum": ["create", "get", "list", "status", "read", "message", "transition", "cancel", "wait"],
				"description": "Operation; omit with prompt for create (progressive default)"
			},
			"prompt": {"type": "string", "description": "Subtask instructions (create; required when action omitted)"},
			"id": {"type": "string", "description": "Delegation id, session id, or name (get/status/read/message/transition/cancel/wait)"},
			"session_id": {"type": "string", "description": "Alias for id (compat with task_* tools)"},
			"name": {"type": "string", "description": "Optional stable teammate alias unique on this session team (e.g. explorer)"},
			"agent": {"type": "string", "description": "Optional agent persona pin: explore, general, commit, reviewer, tester, debugger, build, plan, or user-defined. Wins over auto-route"},
			"model": {"type": "string", "description": "Optional model id pin (bare id or provider/model). Wins over auto-route; omit to inherit"},
			"effort": {"type": "string", "description": "Optional reasoning effort: off, low, medium, high, xhigh, or max"},
			"route": {"type": "string", "description": "Optional routing: auto enables capability-aware selection; omit or off keeps pin-or-inherit"},
			"specialty": {"type": "string", "description": "Required specialty for route=auto (e.g. explore, test, review, debug). Enables auto when route omitted"},
			"capabilities": {
				"type": "array",
				"items": {"type": "string"},
				"description": "Required capability tags for route=auto (all must match)"
			},
			"max_cost_class": {"type": "string", "description": "Optional auto-route model cost filter: low, medium, or high"},
			"models": {
				"type": "array",
				"items": {"type": "string"},
				"description": "Optional model allow-list for auto-route"
			},
			"max_concurrent": {"type": "integer", "description": "Optional per-persona live-child limit before auto-route fallback"},
			"criteria": {
				"type": "array",
				"items": {"type": "string"},
				"description": "Acceptance criteria; non-empty → completion enters lifecycle review"
			},
			"deps": {
				"type": "array",
				"items": {"type": "string"},
				"description": "Upstream delegation or session ids that must reach done before spawn"
			},
			"subscribe": {
				"type": "array",
				"items": {"type": "string"},
				"description": "Lifecycle states to notify on: blocked|review|done|failed|canceled|working|queued"
			},
			"assignee": {"type": "string", "description": "Optional assignee label"},
			"verify": {
				"type": "array",
				"description": "Independent completion gates (cmd|schema|path). Failures yield blocked + verification report",
				"items": {
					"type": "object",
					"properties": {
						"kind": {"type": "string", "description": "Gate kind: cmd, schema, or path"},
						"value": {"type": "string", "description": "Command, schema name (handoff), or filesystem path"},
						"description": {"type": "string", "description": "Optional label"}
					},
					"required": ["kind", "value"]
				}
			},
			"budget": {
				"type": "object",
				"description": "Optional per-child resource limits (overlay session.agentBudget). Hard exceed → child.escalated",
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
				"description": "Sealed context: goal, acceptance, allowed/required paths, artifacts, constraints, items, file_pins",
				"properties": {
					"goal": {"type": "string"},
					"acceptance": {"type": "array", "items": {"type": "string"}},
					"allowed_paths": {"type": "array", "items": {"type": "string"}},
					"required_paths": {"type": "array", "items": {"type": "string"}},
					"artifacts": {
						"type": "array",
						"items": {
							"type": "object",
							"properties": {
								"id": {"type": "string"},
								"version": {"type": "integer"},
								"type": {"type": "string"}
							},
							"required": ["id"]
						}
					},
					"constraints": {"type": "array", "items": {"type": "string"}},
					"items": {
						"type": "array",
						"items": {
							"type": "object",
							"properties": {
								"id": {"type": "string"},
								"kind": {"type": "string"},
								"title": {"type": "string"},
								"text": {"type": "string"},
								"path": {"type": "string"},
								"hash": {"type": "string"},
								"artifact": {
									"type": "object",
									"properties": {
										"id": {"type": "string"},
										"version": {"type": "integer"},
										"type": {"type": "string"}
									}
								}
							},
							"required": ["id"]
						}
					},
					"file_pins": {
						"type": "array",
						"items": {
							"type": "object",
							"properties": {
								"path": {"type": "string"},
								"hash": {"type": "string"},
								"text": {"type": "string"}
							},
							"required": ["path"]
						}
					}
				}
			},
			"include_recent": {"type": "boolean", "description": "status: include latest_activity lines"},
			"offset": {"type": "integer", "description": "read: 0-based start index"},
			"limit": {"type": "integer", "description": "read: max entries (default 20, max 100)"},
			"last": {"type": "integer", "description": "read: when > 0, return last N entries"},
			"include_tools": {"type": "boolean", "description": "read: include tool rows (default true)"},
			"include_reasoning": {"type": "boolean", "description": "read: include reasoning deltas"},
			"text": {"type": "string", "description": "message: guidance for the child"},
			"state": {
				"type": "string",
				"enum": ["queued", "working", "blocked", "review", "done", "failed", "canceled"],
				"description": "transition: target lifecycle state"
			},
			"reason": {"type": "string", "description": "transition: optional block/cancel reason"},
			"expected_version": {"type": "integer", "description": "transition: CAS token; omit or 0 to skip"},
			"events": {
				"type": "array",
				"items": {"type": "string"},
				"description": "wait: task.done, task.failed, task.canceled, task.blocked"
			},
			"timeout_seconds": {"type": "number", "description": "wait: max seconds (0 < t ≤ 300)"}
		}
	}`)
}

func (taskTool) Execute(ctx context.Context, args json.RawMessage, tc *Context) (Result, error) {
	var a progressiveArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{}, fmt.Errorf("invalid arguments: %w", err)
	}
	return executeProgressive(ctx, "task", "task", a, tc)
}

// normalizeTaskVerify validates optional completion gates at spawn time.
// Empty/nil is fine (no independent gates).
func normalizeTaskVerify(in []VerifyGate) ([]VerifyGate, error) {
	if len(in) == 0 {
		return nil, nil
	}
	const maxGates = 16
	if len(in) > maxGates {
		return nil, fmt.Errorf("verify: at most %d gates allowed (got %d)", maxGates, len(in))
	}
	out := make([]VerifyGate, 0, len(in))
	for i, g := range in {
		kind := strings.ToLower(strings.TrimSpace(g.Kind))
		value := strings.TrimSpace(g.Value)
		desc := strings.TrimSpace(g.Description)
		if kind == "" {
			return nil, fmt.Errorf("verify: gate %d: kind is required", i+1)
		}
		switch kind {
		case "cmd", "schema", "path":
		default:
			return nil, fmt.Errorf("verify: gate %d: unknown kind %q (want cmd, schema, path)", i+1, g.Kind)
		}
		if value == "" {
			return nil, fmt.Errorf("verify: gate %d: value is empty", i+1)
		}
		if kind == "schema" {
			if strings.ToLower(value) != "handoff" {
				return nil, fmt.Errorf("verify: gate %d: unknown schema %q (want handoff)", i+1, g.Value)
			}
			value = "handoff"
		}
		out = append(out, VerifyGate{Kind: kind, Value: value, Description: desc})
	}
	return out, nil
}

func shortID(id string) string {
	id = strings.TrimSpace(id)
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

func taskMetadata(res TaskResult) json.RawMessage {
	if res.SessionID == "" && res.Status == "" && res.Name == "" && res.DelegationID == "" && res.RouteReason == "" {
		return nil
	}
	meta := map[string]string{
		"sessionId": res.SessionID,
		"status":    res.Status,
	}
	if n := strings.TrimSpace(res.Name); n != "" {
		meta["name"] = n
	}
	if id := strings.TrimSpace(res.DelegationID); id != "" {
		meta["delegationId"] = id
	}
	if lc := strings.TrimSpace(res.Lifecycle); lc != "" {
		meta["lifecycle"] = lc
	}
	if rr := strings.TrimSpace(res.RouteReason); rr != "" {
		meta["routeReason"] = rr
	}
	b, err := json.Marshal(meta)
	if err != nil {
		return nil
	}
	return b
}
