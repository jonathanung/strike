package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jonathanung/strike-cli/internal/config"
	"github.com/jonathanung/strike-cli/internal/permission"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/question"
	"github.com/jonathanung/strike-cli/internal/tool"
)

const phaseCheckTimeout = 30 * time.Second

// PhaseGrantApproval is a persisted decision that a workflow phase may apply
// permission rules that widen earlier config/agent denies (or ask→allow).
type PhaseGrantApproval struct {
	Workflow    string
	Phase       string
	Index       int
	Fingerprint string
	Grants      permission.Ruleset
}

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
	e.phaseGrantApproval = PhaseGrantApproval{}
	e.perms.SetPhaseRules(nil)
	e.emitSelected(protocol.PhaseChanged{
		Correlation: e.sessionCorr(),
	})
}

// enterPhase applies phase index of w: permissions, optional agent pin, event.
// Permission widenings relative to config/agent layers require approval first;
// rejection leaves the current phase, permissions, and context unchanged.
func (e *Engine) enterPhase(w config.Workflow, index int) error {
	return e.enterPhaseOpts(e.phaseReviewCtx(nil), w, index, true)
}

// enterPhaseOpts is enterPhase with optional phase.Agent pin. pinAgent false
// keeps the current persona (used when the user/tool already chose build or
// orchestrator while leaving plan).
func (e *Engine) enterPhaseOpts(ctx context.Context, w config.Workflow, index int, pinAgent bool) error {
	if index < 0 || index >= len(w.Phases) {
		return fmt.Errorf("workflow %q: phase index %d out of range", w.Name, index)
	}
	phase := w.Phases[index]
	delta := e.perms.WideningFromPhase(phase.Permissions)
	if len(delta) > 0 {
		if err := e.approvePhaseWidening(ctx, w, phase, index, delta); err != nil {
			return err
		}
	}

	e.workflow = w
	e.phaseIndex = index
	if len(delta) > 0 {
		e.phaseGrantApproval = PhaseGrantApproval{
			Workflow:    w.Name,
			Phase:       phase.Name,
			Index:       index,
			Fingerprint: w.Fingerprint,
			Grants:      append(permission.Ruleset(nil), delta...),
		}
	} else {
		e.phaseGrantApproval = PhaseGrantApproval{}
	}
	e.perms.SetPhaseRules(phase.Permissions)

	e.emitSelected(protocol.PhaseChanged{
		Correlation: e.sessionCorr(),
		Workflow:    w.Name,
		Phase:       phase.Name,
		Index:       index,
		Gate:        e.effectiveGateLabel(),
	})

	if !pinAgent {
		return nil
	}
	if agent := strings.TrimSpace(phase.Agent); agent != "" && e.agent.Name != agent {
		if e.turnActive() {
			if err := e.queueSwitchAgent(agent); err != nil {
				return err
			}
		} else {
			// keepModel: phase agent pins must not thrash the session model.
			// applyAgent only — avoid syncPhaseWithAgent re-entry.
			e.applyAgent(agent, true)
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
	return e.enterPhaseOpts(ctx, e.workflow, next, true)
}

// phaseReviewCtx prefers an explicit ctx, then Run's parent context.
func (e *Engine) phaseReviewCtx(ctx context.Context) context.Context {
	if ctx != nil {
		return ctx
	}
	if e.runCtx != nil {
		return e.runCtx
	}
	return context.Background()
}

// approvePhaseWidening requires explicit acceptance of delta before phase
// rules that open earlier denies/asks may apply. --auto accepts without a
// prompt. Resume restores a matching prior decision without re-prompting.
func (e *Engine) approvePhaseWidening(ctx context.Context, w config.Workflow, phase config.Phase, index int, delta permission.Ruleset) error {
	ctx = e.phaseReviewCtx(ctx)
	if e.phaseGrantMatches(w, phase, index, delta) {
		return nil
	}
	if e.opts.DangerouslySkipPermissions {
		e.emitPhaseGrantApproved(w, phase, index, delta, true)
		return nil
	}
	if e.questions == nil {
		return fmt.Errorf("phase %q: permission widening requires a question service (or --auto)", phase.Name)
	}
	body := formatPhaseGrantDelta(delta)
	prompts := []protocol.QuestionPrompt{{
		ID:     "phase_grant",
		Header: "Permission widening",
		Question: fmt.Sprintf(
			"Workflow %q phase %q would widen effective permissions:\n%s\nAllow these grants for this phase?",
			w.Name, phase.Name, body,
		),
		Options: []protocol.QuestionOption{
			{Label: "Yes", Description: "Apply the widened grants with this phase"},
			{Label: "No", Description: "Keep the current phase and permissions unchanged"},
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
		return &question.RejectedError{
			Message: fmt.Sprintf("User declined permission widening for phase %q.", phase.Name),
		}
	}
	e.emitPhaseGrantApproved(w, phase, index, delta, false)
	return nil
}

func (e *Engine) phaseGrantMatches(w config.Workflow, phase config.Phase, index int, delta permission.Ruleset) bool {
	candidates := []PhaseGrantApproval{e.phaseGrantApproval, e.opts.InitialPhaseGrantApproval}
	for _, a := range candidates {
		if a.Workflow != w.Name || a.Phase != phase.Name || a.Index != index {
			continue
		}
		if a.Fingerprint == "" || w.Fingerprint == "" || a.Fingerprint != w.Fingerprint {
			continue
		}
		if permission.RulesEqual(a.Grants, delta) {
			return true
		}
	}
	return false
}

func (e *Engine) emitPhaseGrantApproved(w config.Workflow, phase config.Phase, index int, delta permission.Ruleset, auto bool) {
	grants := make([]protocol.PhaseGrantRule, 0, len(delta))
	for _, r := range delta {
		grants = append(grants, protocol.PhaseGrantRule{
			Permission: r.Permission,
			Pattern:    r.Pattern,
			Action:     string(r.Action),
		})
	}
	e.emitSelected(protocol.PhaseGrantApproved{
		Correlation: e.sessionCorr(),
		Workflow:    w.Name,
		Phase:       phase.Name,
		Index:       index,
		Fingerprint: w.Fingerprint,
		Grants:      grants,
		Auto:        auto,
	})
}

func formatPhaseGrantDelta(delta permission.Ruleset) string {
	var b strings.Builder
	for i, r := range delta {
		if i > 0 {
			b.WriteByte('\n')
		}
		pat := r.Pattern
		if pat == "" {
			pat = "*"
		}
		fmt.Fprintf(&b, "  • %s %s → %s", r.Permission, pat, r.Action)
	}
	return b.String()
}

// effectiveGateLabel is the PhaseChanged.Gate value for the session autonomy
// dial. Autonomy is authoritative for every exit path.
func (e *Engine) effectiveGateLabel() string {
	switch e.autonomy.Normalize() {
	case protocol.AutonomyAgent:
		return string(config.GateAgent)
	case protocol.AutonomyChecks:
		return string(config.GateCheck)
	case protocol.AutonomySkipAll:
		return "skip"
	default:
		return string(config.GateUser)
	}
}

// runExitGate applies the session autonomy policy for one phase exit.
// Every caller (phase_done, exit_plan_mode) shares this resolver.
func (e *Engine) runExitGate(ctx context.Context, phase config.Phase) error {
	switch e.autonomy.Normalize() {
	case protocol.AutonomySkipAll:
		// Bypass workflow/plan approval only — tool permissions are untouched.
		return nil
	case protocol.AutonomyAgent:
		// phase_done / exit_plan_mode is the explicit self-affirmation.
		return nil
	case protocol.AutonomyChecks:
		return e.runCheckGate(ctx, phase)
	default:
		return e.runUserGate(ctx, phase)
	}
}

func (e *Engine) runUserGate(ctx context.Context, phase config.Phase) error {
	if e.questions == nil {
		// Fail closed: supervised mode requires a real approval path.
		return fmt.Errorf("phase %q: supervised autonomy requires a question service", phase.Name)
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
		return &question.RejectedError{
			Message: fmt.Sprintf("User declined leaving phase %q.", phase.Name),
		}
	}
	return nil
}

func (e *Engine) runCheckGate(ctx context.Context, phase config.Phase) error {
	cmd := strings.TrimSpace(phase.Exit.Command)
	if cmd == "" {
		return fmt.Errorf("phase %q check gate has empty command", phase.Name)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("phase %q check canceled: %w", phase.Name, err)
	}

	// Source-aware trust: phase check commands are not free bash — ask under
	// permission "phase_check" with workflow/phase metadata before exec.
	meta, _ := json.Marshal(map[string]string{
		"source":   "workflow_phase_check",
		"workflow": e.workflow.Name,
		"phase":    phase.Name,
	})
	if err := e.perms.AskWithCorrelation(ctx, tool.AskRequest{
		Permission: "phase_check",
		Patterns:   []string{cmd},
		Always:     []string{cmd},
		Metadata:   meta,
	}, e.sessionCorr()); err != nil {
		return fmt.Errorf("phase %q check trust denied: %w", phase.Name, err)
	}

	res, err := tool.RunProcess(ctx, tool.ProcessSpec{
		Argv:    []string{"bash", "-c", cmd},
		Dir:     e.opts.WorkDir,
		Timeout: phaseCheckTimeout,
		Combine: false,
	}, tool.ProcessObserver{})
	if err != nil {
		return fmt.Errorf("phase %q check failed to start: %v", phase.Name, err)
	}
	switch res.Status {
	case tool.ProcessStatusTimeout:
		// Parent ctx may deadline sooner than phaseCheckTimeout; do not claim
		// a fixed duration that may not match the deadline that fired.
		return fmt.Errorf("phase %q check timed out", phase.Name)
	case tool.ProcessStatusCanceled:
		return fmt.Errorf("phase %q check canceled", phase.Name)
	case tool.ProcessStatusError:
		msg := strings.TrimSpace(res.Stderr)
		if msg == "" {
			msg = strings.TrimSpace(res.Output)
		}
		if msg == "" {
			msg = "process error"
		}
		return fmt.Errorf("phase %q check failed: %s", phase.Name, msg)
	}
	if res.ExitCode != 0 {
		msg := strings.TrimSpace(res.Stderr)
		if msg == "" {
			msg = strings.TrimSpace(res.Stdout)
		}
		if msg == "" {
			msg = fmt.Sprintf("exit %d", res.ExitCode)
		}
		return fmt.Errorf("phase %q check failed: %s", phase.Name, msg)
	}
	return nil
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
// build and orchestrator both leave plan for the implement phase (post-plan
// routing may pick either after exit_plan_mode).
func (e *Engine) syncPhaseWithAgent(agentName string) {
	switch agentName {
	case "plan":
		if phase, ok := e.currentPhase(); ok && phase.Name == "plan" {
			return
		}
		_ = e.enterPlanPhase()
	case "build", "orchestrator":
		if phase, ok := e.currentPhase(); ok && phase.Name == "plan" {
			// User/tool forced implementer: jump to implement phase without
			// re-pinning phase.Agent (would clobber orchestrator → build).
			w := e.workflow
			for i, p := range w.Phases {
				if p.Name == "implement" || p.Agent == "build" || p.Agent == "orchestrator" {
					_ = e.enterPhaseOpts(e.phaseReviewCtx(nil), w, i, false)
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
