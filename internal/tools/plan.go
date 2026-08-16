package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jonathanung/strike-cli/harness/tool"
	"strings"
	"time"

	"github.com/jonathanung/strike-cli/internal/persist/plan"
)

// PlanStore is the durable project plan surface used by plan tools.
type PlanStore interface {
	List() ([]plan.Meta, error)
	Get(id string) (plan.Plan, bool, error)
	Create(ownerRoot, title string, sections []plan.SectionInput) (plan.Plan, error)
	UpdateTitle(id, actorRoot, title string, expectedVersion int) (plan.Plan, error)
	UpdateSection(id, actorRoot, sectionID string, title, body *string, expectedVersion int) (plan.Plan, error)
	AddSection(id, actorRoot, title, body string, expectedVersion int) (plan.Plan, error)
	SetStatus(id, actorRoot, status string, expectedVersion int) (plan.Plan, error)
	Reopen(id, actorRoot string, expectedVersion int) (plan.Plan, error)
}

// planView is the bounded, replayable tool projection (identity + version for handoff).
type planView struct {
	ID        string            `json:"id"`
	OwnerRoot string            `json:"owner_root"`
	Title     string            `json:"title"`
	Status    string            `json:"status"`
	Sections  []planSectionView `json:"sections,omitempty"`
	Version   int               `json:"version"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
	Conflict  bool              `json:"conflict,omitempty"`
	Error     string            `json:"error,omitempty"`
}

type planSectionView struct {
	ID                string `json:"id"`
	Title             string `json:"title"`
	Body              string `json:"body,omitempty"`
	DelegateStatus    string `json:"delegate_status,omitempty"`
	DelegateChildID   string `json:"delegate_child_id,omitempty"`
	DelegateChildName string `json:"delegate_child_name,omitempty"`
	DelegateDetail    string `json:"delegate_detail,omitempty"`
	DelegateBaseVer   int    `json:"delegate_base_version,omitempty"`
}

type planMetaView struct {
	ID           string    `json:"id"`
	OwnerRoot    string    `json:"owner_root"`
	Title        string    `json:"title"`
	Status       string    `json:"status"`
	Version      int       `json:"version"`
	SectionCount int       `json:"section_count"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func toPlanView(p plan.Plan) planView {
	out := planView{
		ID:        p.ID,
		OwnerRoot: p.OwnerRoot,
		Title:     p.Title,
		Status:    p.Status,
		Version:   p.Version,
		CreatedAt: p.CreatedAt,
		UpdatedAt: p.UpdatedAt,
	}
	if len(p.Sections) > 0 {
		out.Sections = make([]planSectionView, len(p.Sections))
		for i, s := range p.Sections {
			out.Sections[i] = planSectionView{
				ID:                s.ID,
				Title:             s.Title,
				Body:              s.Body,
				DelegateStatus:    s.DelegateStatus,
				DelegateChildID:   s.DelegateChildID,
				DelegateChildName: s.DelegateChildName,
				DelegateDetail:    s.DelegateDetail,
				DelegateBaseVer:   s.DelegateBaseVersion,
			}
		}
	} else {
		out.Sections = []planSectionView{}
	}
	return out
}

func toMetaView(m plan.Meta) planMetaView {
	return planMetaView{
		ID:           m.ID,
		OwnerRoot:    m.OwnerRoot,
		Title:        m.Title,
		Status:       m.Status,
		Version:      m.Version,
		SectionCount: m.SectionCount,
		CreatedAt:    m.CreatedAt,
		UpdatedAt:    m.UpdatedAt,
	}
}

// rootActor resolves the root-owned plan actor. Mutations require the caller
// to be the root session itself (SessionID == RootSessionID).
func rootActor(tc *tool.Context) (rootID string, err error) {
	if tc == nil {
		return "", errors.New("plan: tool context is required")
	}
	sessionID := strings.TrimSpace(tc.SessionID)
	rootID = strings.TrimSpace(tc.RootSessionID)
	if rootID == "" {
		rootID = sessionID
	}
	if rootID == "" {
		return "", errors.New("plan: root session identity is required")
	}
	if sessionID == "" {
		return "", errors.New("plan: session identity is required")
	}
	if sessionID != rootID {
		return "", fmt.Errorf("%w: only the owning root session may mutate plans (caller is a child or unrelated session)", plan.ErrNotOwner)
	}
	return rootID, nil
}

func planResultJSON(v any, title string) (tool.Result, error) {
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return tool.Result{}, err
	}
	meta, _ := json.Marshal(v)
	return tool.Result{Title: title, Output: string(out), Metadata: meta}, nil
}

func planSoftError(title, msg string, extra map[string]any) (tool.Result, error) {
	payload := map[string]any{"error": msg}
	for k, v := range extra {
		payload[k] = v
	}
	out, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return tool.Result{}, err
	}
	meta, _ := json.Marshal(payload)
	return tool.Result{Title: title, Output: string(out), Metadata: meta}, nil
}

func planConflictResult(store PlanStore, id string, cause error) (tool.Result, error) {
	cur, ok, err := store.Get(id)
	if err != nil {
		return tool.Result{}, err
	}
	if !ok {
		return planSoftError("plan conflict", cause.Error(), map[string]any{
			"id":       id,
			"conflict": true,
		})
	}
	view := toPlanView(cur)
	view.Conflict = true
	view.Error = cause.Error()
	return planResultJSON(view, fmt.Sprintf("plan %s conflict v%d", shortPlanID(cur.ID), cur.Version))
}

func shortID(id string) string {
	id = strings.TrimSpace(id)
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

func shortPlanID(id string) string { return shortID(id) }

// --- plan_write ---

type planWriteTool struct {
	store PlanStore
}

// NewPlanWrite builds the plan_write tool. store must be non-nil.
func NewPlanWrite(store PlanStore) tool.Tool {
	return &planWriteTool{store: store}
}

func (t *planWriteTool) Name() string { return "plan_write" }

func (t *planWriteTool) Contract() tool.Contract {
	return tool.StaticContract(tool.SideEffectExternal, tool.IdempotencyConditional)
}

func (t *planWriteTool) Description() string {
	return `Create or revise a root-session-owned structured plan (durable project artifact).

Use this instead of workspace files for the planning artifact. Plan mode can
mutate plans while write/edit stay denied. Only the owning root session may
create or revise; children and other roots are rejected without delegated
authority. Mutations are compare-and-swap on version — stale updates return
conflict=true with the current plan (newer edit preserved).

Actions:
  - create: title required; optional sections[{title,body}]
  - update_title: id + title + expected_version
  - update_section: id + section_id + title and/or body + expected_version
  - add_section: id + title + optional body + expected_version
  - set_status: id + status (draft|approved|closed) + expected_version
  - reopen: id + expected_version (closed → draft only)

Returns JSON with id, owner_root, version, and sections for handoff.
No full-document overwrite — revise by section.`
}

func (t *planWriteTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"action": {
				"type": "string",
				"enum": ["create", "update_title", "update_section", "add_section", "set_status", "reopen"],
				"description": "Plan mutation"
			},
			"id": {"type": "string", "description": "Plan id (required except create)"},
			"title": {"type": "string", "description": "Plan or section title"},
			"body": {"type": "string", "description": "Section body (update_section/add_section)"},
			"section_id": {"type": "string", "description": "Stable section id (update_section), e.g. s1"},
			"status": {
				"type": "string",
				"enum": ["draft", "approved", "closed"],
				"description": "Lifecycle status for set_status"
			},
			"expected_version": {
				"type": "integer",
				"description": "CAS token from a prior plan_read/plan_write (required for mutations)"
			},
			"sections": {
				"type": "array",
				"description": "Initial sections for create only",
				"items": {
					"type": "object",
					"properties": {
						"title": {"type": "string"},
						"body": {"type": "string"}
					},
					"required": ["title"]
				}
			}
		},
		"required": ["action"]
	}`)
}

func (t *planWriteTool) Execute(ctx context.Context, args json.RawMessage, tc *tool.Context) (tool.Result, error) {
	if t.store == nil {
		return tool.Result{}, errors.New("plan store is unavailable")
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(args, &raw); err != nil {
		return tool.Result{}, fmt.Errorf("invalid arguments: %w", err)
	}
	var in struct {
		Action          string `json:"action"`
		ID              string `json:"id"`
		Title           string `json:"title"`
		Body            string `json:"body"`
		SectionID       string `json:"section_id"`
		Status          string `json:"status"`
		ExpectedVersion int    `json:"expected_version"`
		Sections        []struct {
			Title string `json:"title"`
			Body  string `json:"body"`
		} `json:"sections"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return tool.Result{}, fmt.Errorf("invalid arguments: %w", err)
	}
	action := strings.ToLower(strings.TrimSpace(in.Action))
	if action == "" {
		return tool.Result{}, errors.New("action is required")
	}
	_, titleSet := raw["title"]
	_, bodySet := raw["body"]

	pattern := action
	if id := strings.TrimSpace(in.ID); id != "" {
		pattern = action + ":" + id
	}
	if err := tc.Ask(ctx, tool.AskRequest{
		Permission: "plan_write",
		Patterns:   []string{pattern},
		Always:     []string{"*"},
	}); err != nil {
		return tool.Result{}, err
	}

	ownerRoot, err := rootActor(tc)
	if err != nil {
		if errors.Is(err, plan.ErrNotOwner) {
			return planSoftError("plan not owner", err.Error(), map[string]any{
				"action": action,
			})
		}
		return tool.Result{}, err
	}

	switch action {
	case "create":
		title := strings.TrimSpace(in.Title)
		if title == "" {
			return tool.Result{}, errors.New("title is required for create")
		}
		secs := make([]plan.SectionInput, 0, len(in.Sections))
		for _, s := range in.Sections {
			secs = append(secs, plan.SectionInput{Title: s.Title, Body: s.Body})
		}
		p, err := t.store.Create(ownerRoot, title, secs)
		if err != nil {
			return tool.Result{}, err
		}
		return planResultJSON(toPlanView(p), fmt.Sprintf("plan %s created", shortPlanID(p.ID)))

	case "update_title":
		id := strings.TrimSpace(in.ID)
		if id == "" {
			return tool.Result{}, errors.New("id is required for update_title")
		}
		if !titleSet || strings.TrimSpace(in.Title) == "" {
			return tool.Result{}, errors.New("title is required for update_title")
		}
		if in.ExpectedVersion < 1 {
			return tool.Result{}, errors.New("expected_version is required for update_title")
		}
		p, err := t.store.UpdateTitle(id, ownerRoot, in.Title, in.ExpectedVersion)
		return t.finishMutation(id, p, err, "updated")

	case "update_section":
		id := strings.TrimSpace(in.ID)
		secID := strings.TrimSpace(in.SectionID)
		if id == "" {
			return tool.Result{}, errors.New("id is required for update_section")
		}
		if secID == "" {
			return tool.Result{}, errors.New("section_id is required for update_section")
		}
		if !titleSet && !bodySet {
			return tool.Result{}, errors.New("provide title and/or body for update_section")
		}
		if in.ExpectedVersion < 1 {
			return tool.Result{}, errors.New("expected_version is required for update_section")
		}
		var titlePtr, bodyPtr *string
		if titleSet {
			t := in.Title
			titlePtr = &t
		}
		if bodySet {
			b := in.Body
			bodyPtr = &b
		}
		p, err := t.store.UpdateSection(id, ownerRoot, secID, titlePtr, bodyPtr, in.ExpectedVersion)
		return t.finishMutation(id, p, err, "section updated")

	case "add_section":
		id := strings.TrimSpace(in.ID)
		if id == "" {
			return tool.Result{}, errors.New("id is required for add_section")
		}
		if !titleSet || strings.TrimSpace(in.Title) == "" {
			return tool.Result{}, errors.New("title is required for add_section")
		}
		if in.ExpectedVersion < 1 {
			return tool.Result{}, errors.New("expected_version is required for add_section")
		}
		body := ""
		if bodySet {
			body = in.Body
		}
		p, err := t.store.AddSection(id, ownerRoot, in.Title, body, in.ExpectedVersion)
		return t.finishMutation(id, p, err, "section added")

	case "set_status":
		id := strings.TrimSpace(in.ID)
		status := strings.TrimSpace(in.Status)
		if id == "" {
			return tool.Result{}, errors.New("id is required for set_status")
		}
		if status == "" {
			return tool.Result{}, errors.New("status is required for set_status")
		}
		if in.ExpectedVersion < 1 {
			return tool.Result{}, errors.New("expected_version is required for set_status")
		}
		p, err := t.store.SetStatus(id, ownerRoot, status, in.ExpectedVersion)
		return t.finishMutation(id, p, err, "status "+status)

	case "reopen":
		id := strings.TrimSpace(in.ID)
		if id == "" {
			return tool.Result{}, errors.New("id is required for reopen")
		}
		if in.ExpectedVersion < 1 {
			return tool.Result{}, errors.New("expected_version is required for reopen")
		}
		p, err := t.store.Reopen(id, ownerRoot, in.ExpectedVersion)
		return t.finishMutation(id, p, err, "reopened")

	default:
		return tool.Result{}, fmt.Errorf("action must be create, update_title, update_section, add_section, set_status, or reopen")
	}
}

func (t *planWriteTool) finishMutation(id string, p plan.Plan, err error, verb string) (tool.Result, error) {
	if err != nil {
		if errors.Is(err, plan.ErrConflict) {
			return planConflictResult(t.store, id, err)
		}
		if errors.Is(err, plan.ErrNotOwner) {
			return planSoftError("plan not owner", err.Error(), map[string]any{
				"id": id,
			})
		}
		if errors.Is(err, plan.ErrNotFound) {
			return planSoftError("plan miss", fmt.Sprintf("no plan %q", id), map[string]any{
				"id": id,
			})
		}
		if errors.Is(err, plan.ErrClosedPlan) || errors.Is(err, plan.ErrInvalidStatus) {
			return planSoftError("plan rejected", err.Error(), map[string]any{
				"id": id,
			})
		}
		return tool.Result{}, err
	}
	return planResultJSON(toPlanView(p), fmt.Sprintf("plan %s %s v%d", shortPlanID(p.ID), verb, p.Version))
}

// --- plan_read ---

type planReadTool struct {
	store PlanStore
}

// NewPlanRead builds the plan_read tool. store must be non-nil.
func NewPlanRead(store PlanStore) tool.Tool {
	return &planReadTool{store: store}
}

func (t *planReadTool) Name() string { return "plan_read" }

func (t *planReadTool) Contract() tool.Contract {
	return tool.StaticContract(tool.SideEffectRead, tool.IdempotencySafeRetry)
}

func (t *planReadTool) Description() string {
	return `Read root-session-owned structured plans for this project.

Usage notes:
  - Provide id to fetch one full plan (sections + version for CAS handoff).
  - Omit id to list project-wide index metadata (no section bodies).
  - Returns JSON. Empty list when nothing matches.
  - Any session may read; only the owning root may mutate via plan_write.`
}

func (t *planReadTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"id": {"type": "string", "description": "Fetch a single plan by id"}
		}
	}`)
}

func (t *planReadTool) Execute(ctx context.Context, args json.RawMessage, tc *tool.Context) (tool.Result, error) {
	if t.store == nil {
		return tool.Result{}, errors.New("plan store is unavailable")
	}
	var in struct {
		ID string `json:"id"`
	}
	if len(args) > 0 && string(args) != "null" {
		if err := json.Unmarshal(args, &in); err != nil {
			return tool.Result{}, fmt.Errorf("invalid arguments: %w", err)
		}
	}
	id := strings.TrimSpace(in.ID)

	pattern := "*"
	if id != "" {
		pattern = id
	}
	if err := tc.Ask(ctx, tool.AskRequest{
		Permission: "plan_read",
		Patterns:   []string{pattern},
		Always:     []string{"*"},
	}); err != nil {
		return tool.Result{}, err
	}

	if id != "" {
		p, ok, err := t.store.Get(id)
		if err != nil {
			return tool.Result{}, err
		}
		if !ok {
			return planSoftError("plan miss", fmt.Sprintf("no plan %q", id), map[string]any{
				"id": id,
			})
		}
		return planResultJSON(toPlanView(p), fmt.Sprintf("plan %s v%d", shortPlanID(p.ID), p.Version))
	}

	list, err := t.store.List()
	if err != nil {
		return tool.Result{}, err
	}
	views := make([]planMetaView, 0, len(list))
	for _, m := range list {
		views = append(views, toMetaView(m))
	}
	out, err := json.MarshalIndent(views, "", "  ")
	if err != nil {
		return tool.Result{}, err
	}
	meta, _ := json.Marshal(map[string]any{"count": len(views), "plans": views})
	return tool.Result{
		Title:    fmt.Sprintf("%d plans", len(views)),
		Output:   string(out),
		Metadata: meta,
	}, nil
}
