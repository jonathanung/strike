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
	return `Delegate a bounded subtask to a child agent with its own context.

- Returns immediately after the child starts (does not block this turn).
- Result includes the child session id; a later [child.completed] carries a structured
  handoff JSON (summary, files_changed, verification, findings, blockers,
  recommended_next_action) — the finished work product. Mid-flight coordination uses
  peer messages, not polling.
- Optional verify gates (cmd|schema|path) are independent harness checks run when the
  child claims done. Implementer completion alone does not yield final completed when
  gates are set — pass → completed, fail → blocked with a structured verification report.
  The child's handoff "verification" string is never treated as gate evidence.
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
- Prefer agent_message for mid-flight blockers/handoffs; prefer [child.completed] handoff
  for the finished deliverable. Avoid chatty status ping-pong.
- Tell children (in the prompt) to end with the structured handoff schema. Engine also
  tracks files_changed from mutating tools and merges them into the handoff.
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
			"effort": {"type": "string", "description": "Optional reasoning effort for the child: off, low, medium, high, xhigh, or max. Omit to inherit the parent dial"},
			"verify": {
				"type": "array",
				"description": "Optional independent completion gates (cmd exit 0, schema handoff validity, path exists). When set, implementer-done is not final completed until all pass; failures yield blocked + verification report",
				"items": {
					"type": "object",
					"properties": {
						"kind": {"type": "string", "description": "Gate kind: cmd, schema, or path"},
						"value": {"type": "string", "description": "Command, schema name (handoff), or filesystem path"},
						"description": {"type": "string", "description": "Optional label for the verification report"}
					},
					"required": ["kind", "value"]
				}
			}
		},
		"required": ["prompt"]
	}`)
}

type taskArgs struct {
	Prompt string       `json:"prompt"`
	Name   string       `json:"name"`
	Agent  string       `json:"agent"`
	Model  string       `json:"model"`
	Effort string       `json:"effort"`
	Verify []VerifyGate `json:"verify"`
}

func (taskTool) Execute(ctx context.Context, args json.RawMessage, tc *Context) (Result, error) {
	var a taskArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{}, fmt.Errorf("invalid arguments: %w", err)
	}
	if strings.TrimSpace(a.Prompt) == "" {
		return Result{}, fmt.Errorf("prompt is empty")
	}
	gates, err := normalizeTaskVerify(a.Verify)
	if err != nil {
		return Result{}, err
	}
	if err := tc.Ask(ctx, AskRequest{Permission: "task", Patterns: []string{"*"}, Always: []string{"*"}}); err != nil {
		return Result{}, err
	}
	if tc.SpawnTask == nil {
		return Result{}, fmt.Errorf("task is not available")
	}
	res, err := tc.SpawnTask(ctx, TaskRequest{
		Prompt: a.Prompt,
		Name:   a.Name,
		Agent:  a.Agent,
		Model:  a.Model,
		Effort: a.Effort,
		Verify: gates,
	})
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
