package tool

import (
	"context"
	"encoding/json"
	"fmt"
)

type phaseDoneTool struct{}

// NewPhaseDone returns the phase_done tool used by agent exit gates.
func NewPhaseDone() Tool { return phaseDoneTool{} }

func (phaseDoneTool) Name() string { return "phase_done" }

func (phaseDoneTool) Contract() Contract {
	return staticContract(SideEffectNone, IdempotencyConditional)
}

func (phaseDoneTool) Description() string {
	return `Signal that the current workflow phase is complete and advance to the next phase (or end the workflow).

Honors the session autonomy dial (not the workflow's authored exit type): supervised asks the user, agent treats this call as self-affirmation, checks runs the phase check command, skip-all advances without approval. Cannot leave the built-in plan phase — use exit_plan_mode with plan_id/expected_version for the unified plan approval and handoff.`
}

func (phaseDoneTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {},
		"additionalProperties": false
	}`)
}

func (phaseDoneTool) Execute(ctx context.Context, args json.RawMessage, tc *Context) (Result, error) {
	if err := tc.Ask(ctx, AskRequest{
		Permission: "phase_done",
		Patterns:   []string{"*"},
		Always:     []string{"*"},
	}); err != nil {
		return Result{}, err
	}
	if tc.AdvancePhase == nil {
		return Result{}, fmt.Errorf("phase_done: AdvancePhase is not configured")
	}
	if err := tc.AdvancePhase(ctx); err != nil {
		return Result{}, err
	}
	return Result{
		Title:  "phase advanced",
		Output: "Phase exit gate cleared. Advanced to the next workflow phase (or ended the workflow).",
	}, nil
}
