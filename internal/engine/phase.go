package engine

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/jonathanung/strike-cli/internal/config"
	"github.com/jonathanung/strike-cli/internal/protocol"
)

const phaseCheckTimeout = 30 * time.Second

// enterWorkflow starts workflow at phase index 0 (or re-enters if already active).
func (e *Engine) enterWorkflow(name string) error {
	w, ok := e.findWorkflow(name)
	if !ok {
		return fmt.Errorf("unknown workflow %q", name)
	}
	return e.enterPhase(w, 0)
}

// enterPlanPhase starts the built-in plan-implement workflow at the plan phase.
func (e *Engine) enterPlanPhase() error {
	name := "plan-implement"
	if e.opts.DefaultWorkflow != "" {
		name = e.opts.DefaultWorkflow
	}
	return e.enterWorkflow(name)
}

// clearPhase drops the active workflow phase and its permission profile.
func (e *Engine) clearPhase() {
	if e.phaseIndex < 0 && e.workflow.Name == "" {
		return
	}
	e.workflow = config.Workflow{}
	e.phaseIndex = -1
	e.perms.SetPhaseRules(nil)
	e.emit(protocol.PhaseChanged{
		Correlation: e.sessionCorr(),
	})
}

// enterPhase applies phase index of w: permissions, optional agent pin, event.
func (e *Engine) enterPhase(w config.Workflow, index int) error {
	if index < 0 || index >= len(w.Phases) {
		return fmt.Errorf("workflow %q: phase index %d out of range", w.Name, index)
	}
	phase := w.Phases[index]
	e.workflow = w
	e.phaseIndex = index
	e.perms.SetPhaseRules(phase.Permissions)

	gate := string(phase.Exit.Type)
	if gate == "" {
		gate = string(config.GateAgent)
	}
	e.emit(protocol.PhaseChanged{
		Correlation: e.sessionCorr(),
		Workflow:    w.Name,
		Phase:       phase.Name,
		Index:       index,
		Gate:        gate,
	})

	if agent := strings.TrimSpace(phase.Agent); agent != "" && e.agent.Name != agent {
		if e.turnActive() {
			if err := e.queueSwitchAgent(agent); err != nil {
				return err
			}
		} else {
			// applyAgent only — avoid syncPhaseWithAgent re-entry.
			e.applyAgent(agent)
		}
	}
	return nil
}

// advancePhase clears the current phase exit gate and loads the next phase
// (or ends the workflow). Used by phase_done and exit_plan_mode.
func (e *Engine) advancePhase(ctx context.Context) error {
	if e.phaseIndex < 0 || e.workflow.Name == "" {
		return fmt.Errorf("no active workflow phase")
	}
	phase := e.workflow.Phases[e.phaseIndex]
	if err := e.runExitGate(ctx, phase); err != nil {
		return err
	}
	next := e.phaseIndex + 1
	if next >= len(e.workflow.Phases) {
		e.clearPhase()
		return nil
	}
	return e.enterPhase(e.workflow, next)
}

func (e *Engine) runExitGate(ctx context.Context, phase config.Phase) error {
	gate := phase.Exit.Type
	if gate == "" {
		gate = config.GateAgent
	}
	switch gate {
	case config.GateAgent:
		return nil
	case config.GateUser:
		if e.questions == nil {
			// No UI: treat as approved (headless / tests without questions).
			return nil
		}
		prompts := []protocol.QuestionPrompt{{
			ID:       "phase_exit",
			Header:   "Advance phase",
			Question: fmt.Sprintf("Leave phase %q and continue?", phase.Name),
			Options: []protocol.QuestionOption{
				{Label: "Yes", Description: "Clear the exit gate and load the next phase"},
				{Label: "No", Description: "Stay in the current phase"},
			},
		}}
		answers, err := e.questions.Ask(ctx, e.sessionCorr(), prompts)
		if err != nil {
			return err
		}
		answer := ""
		if len(answers) > 0 {
			answer = strings.TrimSpace(answers[0])
		}
		if !isYesGateAnswer(answer) {
			return fmt.Errorf("user declined leaving phase %q", phase.Name)
		}
		return nil
	case config.GateCheck:
		cmd := strings.TrimSpace(phase.Exit.Command)
		if cmd == "" {
			return fmt.Errorf("phase %q check gate has empty command", phase.Name)
		}
		cctx, cancel := context.WithTimeout(ctx, phaseCheckTimeout)
		defer cancel()
		c := exec.CommandContext(cctx, "bash", "-c", cmd)
		c.Dir = e.opts.WorkDir
		var stderr bytes.Buffer
		c.Stderr = &stderr
		if err := c.Run(); err != nil {
			msg := strings.TrimSpace(stderr.String())
			if msg == "" {
				msg = err.Error()
			}
			return fmt.Errorf("phase %q check failed: %s", phase.Name, msg)
		}
		return nil
	default:
		return fmt.Errorf("phase %q: unknown gate type %q", phase.Name, gate)
	}
}

func isYesGateAnswer(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "yes", "y":
		return true
	default:
		return false
	}
}

func (e *Engine) findWorkflow(name string) (config.Workflow, bool) {
	for _, w := range e.opts.Workflows {
		if w.Name == name {
			return w, true
		}
	}
	// Always fall back to the built-in when requested by name.
	if name == "plan-implement" || name == "" {
		return config.BuiltinPlanImplement(), true
	}
	return config.Workflow{}, false
}

// currentPhase returns the active phase, or false when none.
func (e *Engine) currentPhase() (config.Phase, bool) {
	if e.phaseIndex < 0 || e.phaseIndex >= len(e.workflow.Phases) {
		return config.Phase{}, false
	}
	return e.workflow.Phases[e.phaseIndex], true
}

// syncPhaseWithAgent enters or leaves the plan workflow when the user
// switches agents via tab / SelectAgent (outside tool-driven phase_done).
func (e *Engine) syncPhaseWithAgent(agentName string) {
	switch agentName {
	case "plan":
		if phase, ok := e.currentPhase(); ok && phase.Name == "plan" {
			return
		}
		_ = e.enterPlanPhase()
	case "build":
		if phase, ok := e.currentPhase(); ok && phase.Name == "plan" {
			// User forced build: jump to implement phase of the active workflow.
			w := e.workflow
			for i, p := range w.Phases {
				if p.Name == "implement" || p.Agent == "build" {
					_ = e.enterPhase(w, i)
					return
				}
			}
		}
		if e.phaseIndex >= 0 {
			// Leaving a non-plan phase via agent switch ends the workflow.
			if phase, ok := e.currentPhase(); ok && phase.Agent != "" && phase.Agent != agentName {
				e.clearPhase()
			}
		}
	}
}

// phaseContextPrompt is the injectable system-prompt layer for the active phase.
func (e *Engine) phaseContextPrompt() string {
	phase, ok := e.currentPhase()
	if !ok {
		return ""
	}
	if s := strings.TrimSpace(phase.Context); s != "" {
		return s
	}
	// Built-in plan phase uses the embedded plan overlay when Context is empty.
	if phase.Name == "plan" || phase.Agent == "plan" {
		return PlanSystemPrompt
	}
	return ""
}
