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
	return `Switch the session into plan mode (the "plan" agent).

Plan mode is prompt-enforced readonly: the model should analyze and propose a plan without editing files or running mutating commands. Call exit_plan_mode when ready to return to build mode after user approval.`
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
	if tc.SwitchAgent == nil {
		return Result{}, fmt.Errorf("enter_plan_mode: SwitchAgent is not configured")
	}
	if err := tc.SwitchAgent("plan"); err != nil {
		return Result{}, err
	}
	return Result{
		Title:  "plan mode",
		Output: "Switched to plan mode. Plan mode is prompt-enforced readonly — analyze and plan; do not edit or run mutating tools. Call exit_plan_mode when ready to implement.",
	}, nil
}

type exitPlanModeTool struct{}

func NewExitPlanMode() Tool { return exitPlanModeTool{} }

func (exitPlanModeTool) Name() string { return "exit_plan_mode" }

func (exitPlanModeTool) Description() string {
	return `Leave plan mode and return to the build agent after the user approves.

When AskUser is available, asks a Yes/No confirmation. Yes switches to build; No stays in plan mode. If AskUser is unavailable, switches to build directly. Plan mode is prompt-enforced readonly until exit succeeds.`
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
				Output: "User declined exiting plan mode. Remaining in plan mode (prompt-enforced readonly).",
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
