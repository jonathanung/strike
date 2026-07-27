package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Post-plan implementer routing thresholds (documented heuristic).
// Complex plans route to orchestrator; simple ones to build.
const (
	postPlanComplexSteps = 4 // do-now steps >= this → orchestrator
	postPlanComplexAreas = 3 // distinct code areas >= this → orchestrator
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

Runs the plan phase user exit gate, then loads the implement phase and switches to build (simple) or orchestrator (complex). Pass agent explicitly, or omit and supply steps/areas/multi_agent for the built-in complexity heuristic. If no workflow phase is active, falls back to confirmation and SwitchAgent only.`
}

func (exitPlanModeTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
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

// exitPlanArgs are optional exit_plan_mode inputs for implementer routing.
type exitPlanArgs struct {
	Agent      string `json:"agent"`
	Steps      int    `json:"steps"`
	Areas      int    `json:"areas"`
	MultiAgent bool   `json:"multi_agent"`
}

// PickPostPlanAgent chooses build (simple solo) or orchestrator (complex).
// Explicit agent "build" or "orchestrator" wins; otherwise multi_agent, steps >= 4,
// or areas >= 3 → orchestrator; else build. Unknown explicit agents fall through
// to the heuristic.
func PickPostPlanAgent(agent string, steps, areas int, multiAgent bool) string {
	switch strings.ToLower(strings.TrimSpace(agent)) {
	case "build", "orchestrator":
		return strings.ToLower(strings.TrimSpace(agent))
	}
	if multiAgent || steps >= postPlanComplexSteps || areas >= postPlanComplexAreas {
		return "orchestrator"
	}
	return "build"
}

func (exitPlanModeTool) Execute(ctx context.Context, args json.RawMessage, tc *Context) (Result, error) {
	if err := tc.Ask(ctx, AskRequest{
		Permission: "exit_plan_mode",
		Patterns:   []string{"*"},
		Always:     []string{"*"},
	}); err != nil {
		return Result{}, err
	}

	var parsed exitPlanArgs
	if len(args) > 0 && string(args) != "null" {
		if err := json.Unmarshal(args, &parsed); err != nil {
			return Result{}, fmt.Errorf("exit_plan_mode: invalid args: %w", err)
		}
	}
	target := PickPostPlanAgent(parsed.Agent, parsed.Steps, parsed.Areas, parsed.MultiAgent)

	if tc.AdvancePhase != nil {
		err := tc.AdvancePhase(ctx)
		switch {
		case err == nil:
			applied, err := switchPostPlanAgent(tc, target)
			if err != nil {
				return Result{}, err
			}
			return postPlanResult(applied, true), nil
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
				Question: fmt.Sprintf("Exit plan mode and switch to %s to implement?", target),
				Options: []QuestionOption{
					{Label: "Yes", Description: fmt.Sprintf("Leave plan mode and switch to the %s agent", target)},
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

	applied, err := switchPostPlanAgent(tc, target)
	if err != nil {
		return Result{}, err
	}
	return postPlanResult(applied, false), nil
}

// switchPostPlanAgent queues the implementer. Falls back to build if the
// preferred target is unknown (e.g. orchestrator not in the agent catalog).
// Returns the agent actually queued.
func switchPostPlanAgent(tc *Context, target string) (string, error) {
	if tc.SwitchAgent == nil {
		if target == "build" {
			return "build", nil
		}
		return "", fmt.Errorf("exit_plan_mode: SwitchAgent is not configured (cannot switch to %s)", target)
	}
	if err := tc.SwitchAgent(target); err != nil {
		if target != "build" {
			if err2 := tc.SwitchAgent("build"); err2 == nil {
				return "build", nil
			}
		}
		return "", err
	}
	return target, nil
}

func postPlanResult(target string, viaPhase bool) Result {
	title := target + " mode"
	var output string
	if viaPhase {
		output = fmt.Sprintf("Exited plan mode. Advanced to implement phase as %s — you may implement the plan.", target)
	} else {
		output = fmt.Sprintf("Exited plan mode. Switched to %s agent — you may implement the plan.", target)
	}
	return Result{Title: title, Output: output}
}

func isYesAnswer(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "yes", "y":
		return true
	default:
		return false
	}
}
