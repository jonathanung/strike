package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type teamTaskTool struct{}

// NewTeamTask builds the team_task tool (shared lead-scoped board).
func NewTeamTask() Tool { return teamTaskTool{} }

func (teamTaskTool) Name() string { return "team_task" }

func (teamTaskTool) Description() string {
	return `Shared team task board for claim/assign coordination (lead session scope).

Use when multiple teammates split work and need claim semantics. Prefer
todowrite/todoread for solo lead planning (session todo list, full-replace).
Prefer team_task when children must see one board and claim items without
double-work.

Actions:
  - create: content required → pending unclaimed item (id t1, t2, …)
  - list: full board snapshot
  - update: id + content and/or status; optional expected_version (CAS)
  - claim: id; sets owner=self and status=claimed; fails if another owner
  - complete: id; owner-gated when claimed; optional expected_version (CAS)

Conflict policy: claim is exclusive by owner. Mutating ops accept
expected_version for compare-and-swap (omit or 0 = no version check; claim
still rejects foreign owners). Conflicts return conflict=true with the current
task — not a hard tool error.

Board is cleared when the lead session ends (team dissolve / GC).
Available to lead and children on the implicit session team.`
}

func (teamTaskTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"action": {
				"type": "string",
				"enum": ["create", "list", "update", "claim", "complete"],
				"description": "Board operation"
			},
			"id": {"type": "string", "description": "Task id (required for update/claim/complete)"},
			"content": {"type": "string", "description": "Task text (required for create; optional for update)"},
			"status": {
				"type": "string",
				"enum": ["pending", "claimed", "completed", "cancelled"],
				"description": "New status for update only"
			},
			"expected_version": {
				"type": "integer",
				"description": "CAS token from a prior list/create/claim; omit or 0 to skip version check"
			}
		},
		"required": ["action"]
	}`)
}

func (teamTaskTool) Execute(ctx context.Context, args json.RawMessage, tc *Context) (Result, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(args, &raw); err != nil {
		return Result{}, fmt.Errorf("invalid arguments: %w", err)
	}
	var a struct {
		Action          string `json:"action"`
		ID              string `json:"id"`
		Content         string `json:"content"`
		Status          string `json:"status"`
		ExpectedVersion int    `json:"expected_version"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{}, fmt.Errorf("invalid arguments: %w", err)
	}
	action := strings.ToLower(strings.TrimSpace(a.Action))
	if action == "" {
		return Result{}, fmt.Errorf("action is required")
	}
	_, contentSet := raw["content"]
	req := TeamTaskRequest{
		Action:          action,
		ID:              strings.TrimSpace(a.ID),
		Content:         a.Content,
		ContentSet:      contentSet,
		Status:          strings.TrimSpace(a.Status),
		ExpectedVersion: a.ExpectedVersion,
	}
	switch action {
	case "create":
		if strings.TrimSpace(req.Content) == "" {
			return Result{}, fmt.Errorf("content is required for create")
		}
	case "list":
		// no fields
	case "update":
		if req.ID == "" {
			return Result{}, fmt.Errorf("id is required for update")
		}
		if !contentSet && req.Status == "" {
			return Result{}, fmt.Errorf("provide content and/or status for update")
		}
	case "claim", "complete":
		if req.ID == "" {
			return Result{}, fmt.Errorf("id is required for %s", action)
		}
	default:
		return Result{}, fmt.Errorf("action must be create, list, update, claim, or complete")
	}

	if err := tc.Ask(ctx, AskRequest{
		Permission: "team_task",
		Patterns:   []string{action},
		Always:     []string{"*"},
	}); err != nil {
		return Result{}, err
	}
	if tc.TeamTask == nil {
		return Result{}, fmt.Errorf("team_task is not available")
	}
	res, err := tc.TeamTask(ctx, req)
	if err != nil {
		return Result{}, err
	}
	out, err := json.Marshal(res)
	if err != nil {
		return Result{}, err
	}
	title := "team_task " + action
	if res.Conflict {
		title += " conflict"
	} else if res.Task != nil && res.Task.ID != "" {
		title += " " + res.Task.ID
	} else if n := len(res.Tasks); action == "list" {
		title = fmt.Sprintf("team_task list %d", n)
	}
	return Result{Title: title, Output: string(out)}, nil
}
