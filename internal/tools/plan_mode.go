package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jonathanung/strike-cli/harness/tool"
)

type enterPlanModeTool struct{}

func NewEnterPlanMode() tool.Tool { return enterPlanModeTool{} }

func (enterPlanModeTool) Name() string { return "enter_plan_mode" }

func (enterPlanModeTool) Contract() tool.Contract {
	return tool.StaticContract(tool.SideEffectNone, tool.IdempotencyConditional)
}

func (enterPlanModeTool) Description() string {
	return `Switch the session into plan mode (the "plan" agent) and start the plan→implement workflow.

Plan mode hard-denies write/edit tools via the plan phase permission profile. Analyze and propose a plan via plan_write; call exit_plan_mode with the plan id/version when ready to implement after user approval.`
}

func (enterPlanModeTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {},
		"additionalProperties": false
	}`)
}

func (enterPlanModeTool) Execute(ctx context.Context, args json.RawMessage, tc *tool.Context) (tool.Result, error) {
	if err := tc.Ask(ctx, tool.AskRequest{
		Permission: "enter_plan_mode",
		Patterns:   []string{"*"},
		Always:     []string{"*"},
	}); err != nil {
		return tool.Result{}, err
	}
	if tc.EnterPlanPhase != nil {
		if err := tc.EnterPlanPhase(); err != nil {
			return tool.Result{}, err
		}
	} else if tc.SwitchAgent != nil {
		if err := tc.SwitchAgent("plan"); err != nil {
			return tool.Result{}, err
		}
	} else {
		return tool.Result{}, fmt.Errorf("enter_plan_mode: plan phase/agent switch is not configured")
	}
	return tool.Result{
		Title:  "plan mode",
		Output: "Switched to plan mode. Write/edit tools are denied by the plan phase profile — create/refine the structured plan with plan_write; call exit_plan_mode with plan_id and expected_version when ready to implement.",
	}, nil
}

type exitPlanModeTool struct{}

func NewExitPlanMode() tool.Tool { return exitPlanModeTool{} }

func (exitPlanModeTool) Name() string { return "exit_plan_mode" }

func (exitPlanModeTool) Contract() tool.Contract {
	return tool.StaticContract(tool.SideEffectNone, tool.IdempotencyConditional)
}

func (exitPlanModeTool) Description() string {
	return `Leave plan mode via the unified approval and handoff path.

Requires a canonical structured plan (plan_id + expected_version) unless autonomy is skip-all or a bounded legacy_text plan is supplied for pre-feature session recovery. Runs the session autonomy gate once, records whether approval came from the user or skip-all/agent/checks policy, marks the plan approved, advances plan→implement, and switches to build (simple) or orchestrator (complex).

Rejection or revision leaves plan mode and plan lifecycle unchanged. Manual agent/permission dials and phase_done cannot bypass this path.`
}

func (exitPlanModeTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"plan_id": {
				"type": "string",
				"description": "Structured plan id from plan_write/plan_read (required for new sessions unless autonomy is skip-all or legacy_text is set)."
			},
			"expected_version": {
				"type": "integer",
				"description": "CAS version from the last plan_read/plan_write. Stale versions fail handoff without mutating the plan."
			},
			"legacy_text": {
				"type": "string",
				"description": "Bounded text plan for pre-feature session recovery when no structured plan_id exists (max 32KiB). Ignored when plan_id is set."
			},
			"agent": {
				"type": "string",
				"description": "Post-plan implementer: \"build\" (solo simple) or \"orchestrator\" (multi-area / multi-agent). Overrides the complexity heuristic."
			},
			"steps": {
				"type": "integer",
				"description": "Count of do-now plan steps. When agent is omitted, steps >= 4 selects orchestrator."
			},
			"areas": {
				"type": "integer",
				"description": "Distinct packages/code areas touched. When agent is omitted, areas >= 3 selects orchestrator."
			},
			"multi_agent": {
				"type": "boolean",
				"description": "True when the plan needs parallel specialists or task delegation. When agent is omitted, selects orchestrator."
			}
		},
		"additionalProperties": false
	}`)
}

type exitPlanArgs struct {
	PlanID          string `json:"plan_id"`
	ExpectedVersion int    `json:"expected_version"`
	LegacyText      string `json:"legacy_text"`
	Agent           string `json:"agent"`
	Steps           int    `json:"steps"`
	Areas           int    `json:"areas"`
	MultiAgent      bool   `json:"multi_agent"`
}

func (exitPlanModeTool) Execute(ctx context.Context, args json.RawMessage, tc *tool.Context) (tool.Result, error) {
	if err := tc.Ask(ctx, tool.AskRequest{
		Permission: "exit_plan_mode",
		Patterns:   []string{"*"},
		Always:     []string{"*"},
	}); err != nil {
		return tool.Result{}, err
	}

	var parsed exitPlanArgs
	if len(args) > 0 && string(args) != "null" {
		if err := json.Unmarshal(args, &parsed); err != nil {
			return tool.Result{}, fmt.Errorf("exit_plan_mode: invalid args: %w", err)
		}
	}
	target := tool.PickPostPlanAgent(parsed.Agent, parsed.Steps, parsed.Areas, parsed.MultiAgent)

	if tc.HandoffPlan == nil {
		return tool.Result{}, fmt.Errorf("exit_plan_mode: plan handoff is not configured")
	}
	res, err := tc.HandoffPlan(ctx, tool.PlanHandoffRequest{
		PlanID:          strings.TrimSpace(parsed.PlanID),
		ExpectedVersion: parsed.ExpectedVersion,
		LegacyText:      parsed.LegacyText,
		Agent:           target,
		Steps:           parsed.Steps,
		Areas:           parsed.Areas,
		MultiAgent:      parsed.MultiAgent,
	})
	if err != nil {
		return tool.Result{}, err
	}
	return postPlanHandoffResult(res), nil
}

func postPlanHandoffResult(res tool.PlanHandoffResult) tool.Result {
	title := res.Agent + " mode"
	var b strings.Builder
	fmt.Fprintf(&b, "Exited plan mode via unified handoff.")
	if res.ViaPhase {
		fmt.Fprintf(&b, " Advanced to implement phase as %s.", res.Agent)
	} else {
		fmt.Fprintf(&b, " Switched to %s agent.", res.Agent)
	}
	if res.PlanID != "" {
		fmt.Fprintf(&b, " Approved plan %s v%d (source=%s).", shortPlanID(res.PlanID), res.PlanVersion, res.ApprovalSource)
	} else if res.Legacy {
		fmt.Fprintf(&b, " Legacy text plan handed off (source=%s).", res.ApprovalSource)
	} else {
		fmt.Fprintf(&b, " Approval source=%s.", res.ApprovalSource)
	}
	fmt.Fprintf(&b, " Implement the exact approved plan; use plan_read when plan_id is set.")
	return tool.Result{Title: title, Output: b.String()}
}
