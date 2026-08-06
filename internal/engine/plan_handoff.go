package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/jonathanung/strike-cli/internal/config"
	"github.com/jonathanung/strike-cli/internal/plan"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tool"
)

// PlanStore is the engine-facing surface for plan handoff validation/approval
// and section-delegate completion apply. *plan.Store satisfies this.
type PlanStore interface {
	Get(id string) (plan.Plan, bool, error)
	SetStatus(id, actorRoot, status string, expectedVersion int) (plan.Plan, error)
	// FinishSectionDelegate settles plan_delegate correlation on child terminal.
	FinishSectionDelegate(id, actorRoot, sectionID, childID string, outcome plan.DelegateOutcome) (plan.Plan, error)
}

// PlanHandoffState is the in-memory record of the last successful plan handoff.
// Restored from protocol.PlanHandoff on resume.
type PlanHandoffState struct {
	Active         bool
	PlanID         string
	Version        int
	ApprovalSource string
	Title          string
	Agent          string
	LegacyText     string
}

// handoffPlan is the single approval + handoff operation for leaving plan mode.
// Every successful plan→implement transition must go through this path.
//
// On rejection the plan lifecycle and phase stay unchanged. Missing/stale/
// unauthorized/unapproved plans fail closed under supervised/agent/checks.
// Skip-all may bypass approval (and the structured-plan requirement) without
// touching tool permissions; the bypass is recorded on PlanHandoff.
func (e *Engine) handoffPlan(ctx context.Context, req tool.PlanHandoffRequest) (tool.PlanHandoffResult, error) {
	target := tool.PickPostPlanAgent(req.Agent, req.Steps, req.Areas, req.MultiAgent)

	artifact, err := e.resolveHandoffArtifact(req)
	if err != nil {
		return tool.PlanHandoffResult{}, err
	}

	// Autonomy gate once — shared resolver with phase_done.
	phase, inPhase := e.currentPhase()
	if inPhase {
		if err := e.runExitGate(ctx, phase); err != nil {
			return tool.PlanHandoffResult{}, err
		}
	} else if err := e.runExitGate(ctx, config.Phase{Name: "plan"}); err != nil {
		return tool.PlanHandoffResult{}, err
	}

	source := e.planApprovalSource()

	// Promote draft → approved only after the gate clears (rejection leaves draft).
	finalVersion := artifact.version
	title := artifact.title
	if artifact.planID != "" && e.opts.PlanStore != nil && artifact.needsApprove {
		root := e.rootSessionID()
		p, err := e.opts.PlanStore.SetStatus(artifact.planID, root, plan.StatusApproved, artifact.version)
		if err != nil {
			return tool.PlanHandoffResult{}, fmt.Errorf("plan handoff: approve: %w", err)
		}
		finalVersion = p.Version
		title = p.Title
	}

	// Record + emit before phase/agent mutation so resume retains identity.
	e.planHandoff = PlanHandoffState{
		Active:         true,
		PlanID:         artifact.planID,
		Version:        finalVersion,
		ApprovalSource: source,
		Title:          title,
		Agent:          target,
		LegacyText:     artifact.legacyText,
	}
	e.emit(protocol.PlanHandoff{
		Correlation:    e.sessionCorr(),
		PlanID:         artifact.planID,
		PlanVersion:    finalVersion,
		ApprovalSource: source,
		Title:          title,
		Agent:          target,
		LegacyText:     artifact.legacyText,
	})

	viaPhase := false
	if inPhase {
		// Gate already cleared — advance without re-asking.
		if err := e.advancePhaseAfterGate(); err != nil {
			return tool.PlanHandoffResult{}, err
		}
		viaPhase = true
	}
	applied, err := e.switchPostPlanAgent(target)
	if err != nil {
		return tool.PlanHandoffResult{}, err
	}
	e.planHandoff.Agent = applied

	return tool.PlanHandoffResult{
		Agent:          applied,
		PlanID:         artifact.planID,
		PlanVersion:    finalVersion,
		ApprovalSource: source,
		Title:          title,
		ViaPhase:       viaPhase,
		Legacy:         artifact.legacyText != "",
	}, nil
}

type handoffArtifact struct {
	planID       string
	version      int
	title        string
	legacyText   string
	needsApprove bool
}

func (e *Engine) resolveHandoffArtifact(req tool.PlanHandoffRequest) (handoffArtifact, error) {
	planID := strings.TrimSpace(req.PlanID)
	legacy := req.LegacyText
	if len(legacy) > tool.MaxLegacyPlanText {
		return handoffArtifact{}, fmt.Errorf("plan handoff: legacy_text exceeds %d bytes", tool.MaxLegacyPlanText)
	}

	if planID != "" {
		if e.opts.PlanStore == nil {
			return handoffArtifact{}, fmt.Errorf("plan handoff: plan store is unavailable")
		}
		if req.ExpectedVersion < 1 {
			return handoffArtifact{}, fmt.Errorf("plan handoff: expected_version is required with plan_id")
		}
		p, ok, err := e.opts.PlanStore.Get(planID)
		if err != nil {
			return handoffArtifact{}, fmt.Errorf("plan handoff: %w", err)
		}
		if !ok {
			return handoffArtifact{}, fmt.Errorf("plan handoff: plan %q not found", planID)
		}
		root := e.rootSessionID()
		if p.OwnerRoot != root {
			return handoffArtifact{}, fmt.Errorf("plan handoff: %w", plan.ErrNotOwner)
		}
		if p.Version != req.ExpectedVersion {
			return handoffArtifact{}, fmt.Errorf("%w: have %d, expected %d", plan.ErrConflict, p.Version, req.ExpectedVersion)
		}
		switch p.Status {
		case plan.StatusDraft:
			return handoffArtifact{
				planID:       p.ID,
				version:      p.Version,
				title:        p.Title,
				needsApprove: true,
			}, nil
		case plan.StatusApproved:
			return handoffArtifact{
				planID:  p.ID,
				version: p.Version,
				title:   p.Title,
			}, nil
		case plan.StatusClosed:
			return handoffArtifact{}, fmt.Errorf("plan handoff: plan %q is closed; reopen before handoff", planID)
		default:
			return handoffArtifact{}, fmt.Errorf("plan handoff: plan %q has invalid status %q", planID, p.Status)
		}
	}

	// No structured plan_id.
	if text := strings.TrimSpace(legacy); text != "" {
		return handoffArtifact{
			title:      "legacy plan",
			legacyText: text,
		}, nil
	}

	// Skip-all may hand off without a plan artifact (policy bypass recorded).
	if e.autonomy.Normalize() == protocol.AutonomySkipAll {
		return handoffArtifact{title: "skip-all handoff"}, nil
	}

	return handoffArtifact{}, fmt.Errorf("plan handoff: plan_id+expected_version required (or legacy_text for pre-feature recovery); skip-all autonomy may omit both")
}

func (e *Engine) planApprovalSource() string {
	switch e.autonomy.Normalize() {
	case protocol.AutonomyAgent:
		return protocol.PlanApprovalAgent
	case protocol.AutonomyChecks:
		return protocol.PlanApprovalChecks
	case protocol.AutonomySkipAll:
		return protocol.PlanApprovalSkipAll
	default:
		return protocol.PlanApprovalUser
	}
}

// switchPostPlanAgent queues the implementer. Falls back to build if the
// preferred target is unknown (e.g. orchestrator not in the agent catalog).
func (e *Engine) switchPostPlanAgent(target string) (string, error) {
	target = strings.TrimSpace(strings.ToLower(target))
	if target == "" {
		target = "build"
	}
	if err := e.queueSwitchAgent(target); err != nil {
		if target != "build" {
			if err2 := e.queueSwitchAgent("build"); err2 == nil {
				return "build", nil
			}
		}
		return "", err
	}
	return target, nil
}

// restorePlanHandoff seeds in-memory handoff state after session resume.
func (e *Engine) restorePlanHandoff(h PlanHandoffState) {
	if !h.Active && h.PlanID == "" && h.LegacyText == "" && h.ApprovalSource == "" {
		return
	}
	h.Active = true
	e.planHandoff = h
}

// planHandoffPrompt is injected so the implementer sees the exact approved plan.
func (e *Engine) planHandoffPrompt() string {
	h := e.planHandoff
	if !h.Active {
		return ""
	}
	var b strings.Builder
	b.WriteString("# Approved plan handoff\n\n")
	fmt.Fprintf(&b, "Approval source: %s\n", h.ApprovalSource)
	if h.Agent != "" {
		fmt.Fprintf(&b, "Implementer: %s\n", h.Agent)
	}
	if h.PlanID != "" {
		fmt.Fprintf(&b, "Plan id: %s\nVersion: %d\n", h.PlanID, h.Version)
		if h.Title != "" {
			fmt.Fprintf(&b, "Title: %s\n", h.Title)
		}
		b.WriteString("\nRetrieve the exact approved plan with plan_read using this id. Implement this version; do not invent a different plan.\n")
		// Inline body when store is available so the next request sees content
		// even before a tool call.
		if e.opts.PlanStore != nil {
			if p, ok, err := e.opts.PlanStore.Get(h.PlanID); err == nil && ok {
				b.WriteString("\n## Plan body\n\n")
				fmt.Fprintf(&b, "**%s** (status=%s, v%d)\n\n", p.Title, p.Status, p.Version)
				for _, sec := range p.Sections {
					fmt.Fprintf(&b, "### %s (%s)\n\n", sec.Title, sec.ID)
					if body := strings.TrimSpace(sec.Body); body != "" {
						b.WriteString(body)
						b.WriteString("\n\n")
					}
				}
			}
		}
	} else if text := strings.TrimSpace(h.LegacyText); text != "" {
		b.WriteString("\n## Legacy text plan\n\n")
		b.WriteString(text)
		b.WriteString("\n")
	} else {
		b.WriteString("\nNo structured plan artifact (skip-all or empty handoff). Proceed from conversation context.\n")
	}
	return strings.TrimSpace(b.String()) + "\n"
}

// requirePlanHandoffForPhaseAdvance rejects phase_done (and any other advance
// caller) when leaving the plan convenience plan phase without going through
// handoffPlan. exit_plan_mode uses advancePhaseAfterGate after handoff.
func (e *Engine) requirePlanHandoffForPhaseAdvance() error {
	if e.phaseRecovery != "" {
		return nil
	}
	if !e.isPlanConvenienceWorkflow() {
		return nil
	}
	phase, ok := e.currentPhase()
	if !ok || phase.Name != "plan" {
		return nil
	}
	// Already handed off in this session (e.g. resume mid-implement should not
	// hit this — phase would not be plan). Block bare advance from plan.
	if e.planHandoff.Active {
		// Handoff already recorded but still on plan phase — allow advance only
		// via handoffPlan's after-gate path. phase_done must not double-advance
		// without identity; require exit_plan_mode for the transition itself.
	}
	return fmt.Errorf("plan handoff required: call exit_plan_mode with plan_id and expected_version (or legacy_text); phase_done cannot leave the plan phase")
}
