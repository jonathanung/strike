package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jonathanung/strike-cli/harness/tool"
	"strings"

	"github.com/jonathanung/strike-cli/internal/plan"
)

// PlanDelegateStore is the durable plan surface used by plan_delegate.
// *plan.Store satisfies this interface.
type PlanDelegateStore interface {
	Get(id string) (plan.Plan, bool, error)
	BeginSectionDelegate(id, actorRoot, sectionID, childID, childName string) (plan.Plan, error)
	FinishSectionDelegate(id, actorRoot, sectionID, childID string, outcome plan.DelegateOutcome) (plan.Plan, error)
}

type planDelegateTool struct {
	store PlanDelegateStore
}

// NewPlanDelegate builds the plan_delegate tool. store must be non-nil.
func NewPlanDelegate(store PlanDelegateStore) tool.Tool {
	return &planDelegateTool{store: store}
}

func (t *planDelegateTool) Name() string { return "plan_delegate" }

func (t *planDelegateTool) Description() string {
	return `Delegate refinement of one plan section to a child agent via the existing team runtime.

Reuses task spawn (stable name aliases, roster, peer messages, team_task). Does
not create a second scheduler. Section-to-child correlation is persisted on the
plan; a second dispatch while in_flight is rejected. On child completion the
engine applies structured handoff fields (section_title/section_body) through
content-based CAS so an intervening user edit of that section is not overwritten.

Actions:
  - dispatch: id + section_id required; optional prompt, name, agent, model, effort
    Spawns a non-blocking child and records correlation. Returns child session id.
  - status: id required — plan projection including per-section delegate fields.

Only the owning root session may dispatch. Children cannot start section
delegates. Failed/canceled/malformed results preserve prior section content.

Orchestrator implementation task boundaries remain independent of plan sections
— this tool refines plan text only, not auto-mapped implementation work.`
}

func (t *planDelegateTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"action": {
				"type": "string",
				"enum": ["dispatch", "status"],
				"description": "dispatch starts a section refiner; status reads correlation"
			},
			"id": {"type": "string", "description": "Plan id"},
			"section_id": {"type": "string", "description": "Stable section id (dispatch), e.g. s1"},
			"prompt": {"type": "string", "description": "Optional extra instructions for the child"},
			"name": {"type": "string", "description": "Short unique teammate alias from the assigned task. If omitted, derived from the prompt first line"},
			"agent": {"type": "string", "description": "Optional agent persona (default: plan)"},
			"model": {"type": "string", "description": "Optional model id for the child"},
			"effort": {"type": "string", "description": "Optional reasoning effort for the child"}
		},
		"required": ["action", "id"]
	}`)
}

func (t *planDelegateTool) Execute(ctx context.Context, args json.RawMessage, tc *tool.Context) (tool.Result, error) {
	if t.store == nil {
		return tool.Result{}, errors.New("plan store is unavailable")
	}
	var in struct {
		Action    string `json:"action"`
		ID        string `json:"id"`
		SectionID string `json:"section_id"`
		Prompt    string `json:"prompt"`
		Name      string `json:"name"`
		Agent     string `json:"agent"`
		Model     string `json:"model"`
		Effort    string `json:"effort"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return tool.Result{}, fmt.Errorf("invalid arguments: %w", err)
	}
	action := strings.ToLower(strings.TrimSpace(in.Action))
	id := strings.TrimSpace(in.ID)
	if action == "" {
		return tool.Result{}, errors.New("action is required")
	}
	if id == "" {
		return tool.Result{}, errors.New("id is required")
	}

	pattern := action + ":" + id
	if err := tc.Ask(ctx, tool.AskRequest{
		Permission: "plan_delegate",
		Patterns:   []string{pattern},
		Always:     []string{"*"},
	}); err != nil {
		return tool.Result{}, err
	}

	switch action {
	case "status":
		return t.status(id)
	case "dispatch":
		return t.dispatch(ctx, tc, in)
	default:
		return tool.Result{}, fmt.Errorf("action must be dispatch or status")
	}
}

func (t *planDelegateTool) status(id string) (tool.Result, error) {
	p, ok, err := t.store.Get(id)
	if err != nil {
		return tool.Result{}, err
	}
	if !ok {
		return planSoftError("plan miss", fmt.Sprintf("no plan %q", id), map[string]any{"id": id})
	}
	view := toPlanDelegateView(p)
	return planResultJSON(view, fmt.Sprintf("plan %s delegate status v%d", shortPlanID(p.ID), p.Version))
}

func (t *planDelegateTool) dispatch(ctx context.Context, tc *tool.Context, in struct {
	Action    string `json:"action"`
	ID        string `json:"id"`
	SectionID string `json:"section_id"`
	Prompt    string `json:"prompt"`
	Name      string `json:"name"`
	Agent     string `json:"agent"`
	Model     string `json:"model"`
	Effort    string `json:"effort"`
}) (tool.Result, error) {
	ownerRoot, err := rootActor(tc)
	if err != nil {
		if errors.Is(err, plan.ErrNotOwner) {
			return planSoftError("plan not owner", err.Error(), map[string]any{
				"action": "dispatch",
				"id":     strings.TrimSpace(in.ID),
			})
		}
		return tool.Result{}, err
	}
	id := strings.TrimSpace(in.ID)
	secID := strings.TrimSpace(in.SectionID)
	if secID == "" {
		return tool.Result{}, errors.New("section_id is required for dispatch")
	}
	if tc.SpawnTask == nil {
		return tool.Result{}, errors.New("task spawn is not available")
	}

	p, ok, err := t.store.Get(id)
	if err != nil {
		return tool.Result{}, err
	}
	if !ok {
		return planSoftError("plan miss", fmt.Sprintf("no plan %q", id), map[string]any{"id": id})
	}
	if p.OwnerRoot != ownerRoot {
		return planSoftError("plan not owner", plan.ErrNotOwner.Error(), map[string]any{"id": id})
	}
	sec, found := findSection(p, secID)
	if !found {
		return planSoftError("section miss", fmt.Sprintf("no section %q on plan %s", secID, shortPlanID(id)), map[string]any{
			"id":         id,
			"section_id": secID,
		})
	}
	if sec.DelegateStatus == plan.DelegateInFlight {
		// Live child still running → reject. Stale correlation (process restart,
		// unknown/terminal child) is reclaimed so dispatch is not stuck forever.
		if liveSectionDelegate(ctx, tc, sec.DelegateChildID) {
			return planSoftError("section in flight", fmt.Sprintf("section %s already has in-flight child %s", secID, sec.DelegateChildID), map[string]any{
				"id":         id,
				"section_id": secID,
				"child_id":   sec.DelegateChildID,
				"in_flight":  true,
			})
		}
		if _, err := t.store.FinishSectionDelegate(id, ownerRoot, secID, sec.DelegateChildID, plan.DelegateOutcome{
			Status: plan.DelegateCanceled,
			Detail: "stale in_flight reclaimed (child not live); prior content preserved",
		}); err != nil && !errors.Is(err, plan.ErrDelegateMismatch) {
			// Race: another finish won — re-read and continue if no longer in_flight.
			p2, ok2, gerr := t.store.Get(id)
			if gerr != nil {
				return tool.Result{}, gerr
			}
			if !ok2 {
				return planSoftError("plan miss", fmt.Sprintf("no plan %q", id), map[string]any{"id": id})
			}
			sec2, found2 := findSection(p2, secID)
			if !found2 || sec2.DelegateStatus == plan.DelegateInFlight {
				return planSoftError("section in flight", fmt.Sprintf("section %s still in_flight after reclaim attempt", secID), map[string]any{
					"id": id, "section_id": secID, "in_flight": true,
				})
			}
			p = p2
			sec = sec2
		} else {
			// Refresh section snapshot after reclaim.
			p2, ok2, gerr := t.store.Get(id)
			if gerr != nil {
				return tool.Result{}, gerr
			}
			if ok2 {
				p = p2
				if s2, f2 := findSection(p2, secID); f2 {
					sec = s2
				}
			}
		}
	}

	agent := strings.TrimSpace(in.Agent)
	if agent == "" {
		agent = "plan"
	}
	childPrompt := buildSectionDelegatePrompt(p, sec, in.Prompt)

	res, err := tc.SpawnTask(ctx, tool.TaskRequest{
		Prompt:    childPrompt,
		Name:      strings.TrimSpace(in.Name),
		Agent:     agent,
		Model:     strings.TrimSpace(in.Model),
		Effort:    strings.TrimSpace(in.Effort),
		PlanID:    id,
		SectionID: secID,
	})
	if err != nil {
		return tool.Result{}, err
	}
	if res.SessionID == "" {
		return tool.Result{}, errors.New("plan_delegate: child session id missing after spawn")
	}

	// Record correlation after spawn. If another dispatch won the race, interrupt
	// the orphan child when possible and surface in_flight.
	updated, beginErr := t.store.BeginSectionDelegate(id, ownerRoot, secID, res.SessionID, res.Name)
	if beginErr != nil {
		if tc.TaskInterrupt != nil {
			_, _ = tc.TaskInterrupt(ctx, tool.TaskInterruptRequest{SessionID: res.SessionID})
		}
		if errors.Is(beginErr, plan.ErrInFlight) {
			return planSoftError("section in flight", beginErr.Error(), map[string]any{
				"id":         id,
				"section_id": secID,
				"in_flight":  true,
			})
		}
		if errors.Is(beginErr, plan.ErrNotOwner) {
			return planSoftError("plan not owner", beginErr.Error(), map[string]any{"id": id})
		}
		if errors.Is(beginErr, plan.ErrClosedPlan) {
			return planSoftError("plan rejected", beginErr.Error(), map[string]any{"id": id})
		}
		return tool.Result{}, beginErr
	}

	out := map[string]any{
		"action":     "dispatch",
		"id":         updated.ID,
		"section_id": secID,
		"child_id":   res.SessionID,
		"name":       res.Name,
		"agent":      agent,
		"status":     plan.DelegateInFlight,
		"version":    updated.Version,
		"plan":       toPlanDelegateView(updated),
		"spawn":      res.Output,
	}
	title := fmt.Sprintf("plan %s section %s → %s", shortPlanID(id), secID, shortID(res.SessionID))
	if n := strings.TrimSpace(res.Name); n != "" {
		title = fmt.Sprintf("plan %s section %s → %s", shortPlanID(id), secID, n)
	}
	return planResultJSON(out, title)
}

func findSection(p plan.Plan, sectionID string) (plan.Section, bool) {
	for _, s := range p.Sections {
		if s.ID == sectionID {
			return s, true
		}
	}
	return plan.Section{}, false
}

// liveSectionDelegate reports whether childID still looks like a running
// owned child. Unknown/missing TaskStatus or terminal states are not live.
func liveSectionDelegate(ctx context.Context, tc *tool.Context, childID string) bool {
	childID = strings.TrimSpace(childID)
	if childID == "" || tc == nil || tc.TaskStatus == nil {
		return false
	}
	st, err := tc.TaskStatus(ctx, tool.TaskStatusRequest{SessionID: childID})
	if err != nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(st.State)) {
	case "starting", "working", "needs_attention", "blocked":
		return true
	default:
		// completed|failed|canceled|unknown|"" → not live
		return false
	}
}

func buildSectionDelegatePrompt(p plan.Plan, sec plan.Section, extra string) string {
	var b strings.Builder
	b.WriteString("You are refining ONE section of a structured plan. Do not implement code or mutate the workspace unless the section text itself requires research via read-only tools.\n\n")
	fmt.Fprintf(&b, "Plan id: %s\nPlan title: %s\nPlan status: %s\n", p.ID, p.Title, p.Status)
	fmt.Fprintf(&b, "Section id: %s\nSection title: %s\n", sec.ID, sec.Title)
	b.WriteString("Section body:\n")
	if strings.TrimSpace(sec.Body) == "" {
		b.WriteString("(empty)\n")
	} else {
		b.WriteString(sec.Body)
		if !strings.HasSuffix(sec.Body, "\n") {
			b.WriteByte('\n')
		}
	}
	if x := strings.TrimSpace(extra); x != "" {
		b.WriteString("\nAdditional instructions from the lead:\n")
		b.WriteString(x)
		b.WriteByte('\n')
	}
	b.WriteString(`
When finished, end with a structured completion handoff JSON object that includes:
  - summary: short outcome
  - section_title: optional revised section title (omit to keep current)
  - section_body: the full revised section body (required for a successful apply)
  - findings, blockers, recommended_next_action as usual

The lead applies section_title/section_body via compare-and-swap on this section only.
Do not call plan_write — you cannot mutate the plan store directly.
`)
	return b.String()
}

// planDelegateView extends planView with per-section delegate correlation.
type planDelegateView struct {
	ID        string                    `json:"id"`
	OwnerRoot string                    `json:"owner_root"`
	Title     string                    `json:"title"`
	Status    string                    `json:"status"`
	Sections  []planDelegateSectionView `json:"sections"`
	Version   int                       `json:"version"`
}

type planDelegateSectionView struct {
	ID                string `json:"id"`
	Title             string `json:"title"`
	Body              string `json:"body,omitempty"`
	DelegateStatus    string `json:"delegate_status,omitempty"`
	DelegateChildID   string `json:"delegate_child_id,omitempty"`
	DelegateChildName string `json:"delegate_child_name,omitempty"`
	DelegateDetail    string `json:"delegate_detail,omitempty"`
	DelegateBaseVer   int    `json:"delegate_base_version,omitempty"`
}

func toPlanDelegateView(p plan.Plan) planDelegateView {
	out := planDelegateView{
		ID:        p.ID,
		OwnerRoot: p.OwnerRoot,
		Title:     p.Title,
		Status:    p.Status,
		Version:   p.Version,
		Sections:  []planDelegateSectionView{},
	}
	if len(p.Sections) == 0 {
		return out
	}
	out.Sections = make([]planDelegateSectionView, len(p.Sections))
	for i, s := range p.Sections {
		out.Sections[i] = planDelegateSectionView{
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
	return out
}
