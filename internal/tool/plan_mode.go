package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type enterPlanModeTool struct{}

func NewEnterPlanMode() Tool { return enterPlanModeTool{} }

func (enterPlanModeTool) Name() string { return "enter_plan_mode" }

func (enterPlanModeTool) Description() string {
	return `Switch the session into plan mode (the "plan" agent) and start the plan→implement workflow.

Plan mode hard-denies write/edit tools via the plan phase permission profile. Analyze and propose a plan; call exit_plan_mode (or phase_done) when ready to implement after user approval.`
}

func (enterPlanModeTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {},
		"additionalProperties": false
	}`)
}

func (enterPlanModeTool) Execute(ctx context.Context, args json.RawMessage, tc *Context) (Result, error) {
	if err := tc.Ask(ctx, AskRequest{
		Permission: "enter_plan_mode",
		Patterns:   []string{"*"},
		Always:     []string{"*"},
	}); err != nil {
		return Result{}, err
	}
	if tc.EnterPlanPhase != nil {
		if err := tc.EnterPlanPhase(); err != nil {
			return Result{}, err
		}
	} else if tc.SwitchAgent != nil {
		if err := tc.SwitchAgent("plan"); err != nil {
			return Result{}, err
		}
	} else {
		return Result{}, fmt.Errorf("enter_plan_mode: plan phase/agent switch is not configured")
	}
	return Result{
		Title:  "plan mode",
		Output: "Switched to plan mode. Write/edit tools are denied by the plan phase profile — analyze and plan; call exit_plan_mode when ready to implement.",
	}, nil
}

type exitPlanModeTool struct{}

func NewExitPlanMode() Tool { return exitPlanModeTool{} }

func (exitPlanModeTool) Name() string { return "exit_plan_mode" }

func (exitPlanModeTool) Description() string {
	return `Leave plan mode and advance the plan→implement workflow after the user approves.

Runs the plan phase user exit gate, then loads the implement phase (build agent). If no workflow phase is active, falls back to a Yes/No confirmation and switches to build.`
}

func (exitPlanModeTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {},
		"additionalProperties": false
	}`)
}

func (exitPlanModeTool) Execute(ctx context.Context, args json.RawMessage, tc *Context) (Result, error) {
	if err := tc.Ask(ctx, AskRequest{
		Permission: "exit_plan_mode",
		Patterns:   []string{"*"},
		Always:     []string{"*"},
	}); err != nil {
		return Result{}, err
	}

	if tc.AdvancePhase != nil {
		err := tc.AdvancePhase(ctx)
		switch {
		case err == nil:
			return Result{
				Title:  "build mode",
				Output: "Exited plan mode. Advanced to implement phase — you may implement the plan.",
			}, nil
		case strings.Contains(err.Error(), "declined"):
			return Result{
				Title:  "staying in plan mode",
				Output: "User declined exiting plan mode. Remaining in plan phase (write/edit denied).",
			}, nil
		case strings.Contains(err.Error(), "no active workflow phase"):
			// Fall through to agent-only exit path.
		default:
			return Result{}, err
		}
	}

	if tc.SwitchAgent == nil {
		return Result{}, fmt.Errorf("exit_plan_mode: SwitchAgent is not configured")
	}

	if tc.AskUser != nil {
		resp, err := tc.AskUser(ctx, QuestionRequest{
			Questions: []QuestionItem{{
				ID:       "exit_plan",
				Header:   "Exit plan",
				Question: "Exit plan mode and switch to build to implement?",
				Options: []QuestionOption{
					{Label: "Yes", Description: "Leave plan mode and switch to the build agent"},
					{Label: "No", Description: "Stay in plan mode"},
				},
			}},
		})
		if err != nil {
			return Result{}, err
		}
		answer := ""
		if len(resp.Answers) > 0 {
			answer = strings.TrimSpace(resp.Answers[0])
		}
		if !isYesAnswer(answer) {
			return Result{
				Title:  "staying in plan mode",
				Output: "User declined exiting plan mode. Remaining in plan mode.",
			}, nil
		}
	}

	if err := tc.SwitchAgent("build"); err != nil {
		return Result{}, err
	}
	return Result{
		Title:  "build mode",
		Output: "Exited plan mode. Switched to build agent — you may implement the plan.",
	}, nil
}

func isYesAnswer(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "yes", "y":
		return true
	default:
		return false
	}
}
