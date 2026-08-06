package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type delegateTool struct{}

// NewDelegate builds the delegate tool (first-class delegation lifecycle).
func NewDelegate() Tool { return delegateTool{} }

func (delegateTool) Name() string { return "delegate" }

func (delegateTool) Description() string {
	return `First-class delegation lifecycle (create/get/list/transition).

Prefer this when you need acceptance criteria, task dependencies, or
subscriptions at create time. Plain task spawn remains supported and creates a
compatible lifecycle object automatically.

States: queued → working → blocked → review → done (+ failed / canceled).
Engine validates transitions; illegal moves return actionable errors.
Dependencies keep a delegation queued until upstream deps reach done.
When criteria are set, successful child completion enters review (not final
done) so verification gates can run. CAS via expected_version on transition.

Actions:
  - create: prompt required; optional name/agent/model/effort/assignee,
    criteria[], deps[] (delegation or session ids), subscribe[]
    (blocked|review|done|failed|canceled|working|queued). Spawns immediately
    when deps are satisfied; otherwise status=queued.
  - get: id (delegation id, session id, or name)
  - list: full registry snapshot for this session team
  - transition: id + state; optional reason, expected_version (CAS)

task_status / agent_roster / [child.completed] stay coherent with lifecycle
states. Parent→child control remains task_message / task_interrupt.`
}

func (delegateTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"action": {
				"type": "string",
				"enum": ["create", "get", "list", "transition"],
				"description": "Lifecycle operation"
			},
			"id": {"type": "string", "description": "Delegation id, session id, or name (get/transition)"},
			"prompt": {"type": "string", "description": "Subtask instructions (create)"},
			"name": {"type": "string", "description": "Optional stable teammate alias (create)"},
			"agent": {"type": "string", "description": "Optional agent persona (create)"},
			"model": {"type": "string", "description": "Optional model pin (create)"},
			"effort": {"type": "string", "description": "Optional effort pin (create)"},
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
	var a struct {
		Action          string   `json:"action"`
		ID              string   `json:"id"`
		Prompt          string   `json:"prompt"`
		Name            string   `json:"name"`
		Agent           string   `json:"agent"`
		Model           string   `json:"model"`
		Effort          string   `json:"effort"`
		Assignee        string   `json:"assignee"`
		Criteria        []string `json:"criteria"`
		Deps            []string `json:"deps"`
		Subscribe       []string `json:"subscribe"`
		State           string   `json:"state"`
		Reason          string   `json:"reason"`
		ExpectedVersion int      `json:"expected_version"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{}, fmt.Errorf("invalid arguments: %w", err)
	}
	action := strings.ToLower(strings.TrimSpace(a.Action))
	if action == "" {
		return Result{}, fmt.Errorf("action is required")
	}
	req := DelegateRequest{
		Action:          action,
		ID:              strings.TrimSpace(a.ID),
		Prompt:          a.Prompt,
		Name:            a.Name,
		Agent:           a.Agent,
		Model:           a.Model,
		Effort:          a.Effort,
		Assignee:        a.Assignee,
		Criteria:        a.Criteria,
		Deps:            a.Deps,
		Subscribe:       a.Subscribe,
		State:           strings.TrimSpace(a.State),
		Reason:          a.Reason,
		ExpectedVersion: a.ExpectedVersion,
	}
	switch action {
	case "create":
		if strings.TrimSpace(req.Prompt) == "" {
			return Result{}, fmt.Errorf("prompt is required for create")
		}
	case "list":
		// no fields
	case "get", "transition":
		if req.ID == "" {
			return Result{}, fmt.Errorf("id is required for %s", action)
		}
		if action == "transition" && req.State == "" {
			return Result{}, fmt.Errorf("state is required for transition")
		}
	default:
		return Result{}, fmt.Errorf("action must be create, get, list, or transition")
	}

	if err := tc.Ask(ctx, AskRequest{
		Permission: "delegate",
		Patterns:   []string{action},
		Always:     []string{"*"},
	}); err != nil {
		return Result{}, err
	}
	if tc.Delegate == nil {
		return Result{}, fmt.Errorf("delegate is not available")
	}
	res, err := tc.Delegate(ctx, req)
	if err != nil {
		return Result{}, err
	}
	out, err := json.Marshal(res)
	if err != nil {
		return Result{}, err
	}
	title := "delegate " + action
	if res.Conflict {
		title += " conflict"
	} else if res.Item != nil && res.Item.ID != "" {
		title += " " + res.Item.ID
		if res.Item.State != "" {
			title += " " + res.Item.State
		}
	} else if action == "list" {
		title = fmt.Sprintf("delegate list %d", len(res.Items))
	}
	return Result{Title: title, Output: string(out)}, nil
}
