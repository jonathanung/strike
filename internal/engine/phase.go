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

// startWorkflow activates a loaded workflow at phase 0. Validates the target
// before mutating state. Exactly one workflow is active per root session:
// starting replaces any prior active (or recovery) workflow after validation.
func (e *Engine) startWorkflow(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("workflow name is required")
	}
	w, ok := e.findWorkflow(name)
	if !ok {
		return fmt.Errorf("unknown workflow %q", name)
	}
	if err := config.ValidateWorkflow(w); err != nil {
		return fmt.Errorf("workflow %q invalid: %w", name, err)
	}
	if len(w.Phases) == 0 {
		return fmt.Errorf("workflow %q has no phases", name)
	}
	// Validate phase 0 before clearing the current workflow.
	if err := e.validatePhaseTarget(w, 0); err != nil {
		return err
	}
	// Drop any prior active/recovery state so only one workflow is live.
	if e.phaseIndex >= 0 || e.workflow.Name != "" || e.phaseRecovery != "" {
		e.clearPhase()
	}
	return e.enterPhase(w, 0)
}

// stopWorkflow clears the active workflow phase and phase permissions.
// Alias for clearPhase; used by the StopWorkflow op and recovery cleanup.
func (e *Engine) stopWorkflow() {
	e.clearPhase()
}

// enterWorkflow starts workflow at phase index 0 (or re-enters if already active).
// Prefer startWorkflow for generic activation; kept for call-site compatibility.
func (e *Engine) enterWorkflow(name string) error {
	return e.startWorkflow(name)
}

// enterPlanPhase is the plan convenience adapter: starts the default plan
// workflow (DefaultWorkflow or plan-implement) at phase 0.
func (e *Engine) enterPlanPhase() error {
	name := "plan-implement"
	if e.opts.DefaultWorkflow != "" {
		name = e.opts.DefaultWorkflow
	}
	return e.startWorkflow(name)
}

// clearPhase drops the active workflow phase, recovery state, and phase
// permission profile.
func (e *Engine) clearPhase() {
	if e.phaseIndex < 0 && e.workflow.Name == "" && e.phaseRecovery == "" {
		return
	}
	e.workflow = config.Workflow{}
	e.phaseIndex = -1
	e.phaseRecovery = ""
	e.phaseGrantApproval = PhaseGrantApproval{}
	e.perms.SetPhaseRules(nil)
	e.emitSelected(protocol.PhaseChanged{
		Correlation: e.sessionCorr(),
	})
}

// enterPhase applies phase index of w: permissions, optional agent pin, event.
// Validates the target before mutating context, permissions, agent, or events.
// Permission widenings relative to config/agent layers require approval first;
// rejection leaves the current phase, permissions, and context unchanged.
func (e *Engine) enterPhase(w config.Workflow, index int) error {
	return e.enterPhaseOpts(e.phaseReviewCtx(nil), w, index, true)
}

// enterPhaseOpts is enterPhase with optional phase.Agent pin. pinAgent false
// keeps the current persona (used when the user/tool already chose build or
// orchestrator while leaving plan).
func (e *Engine) enterPhaseOpts(ctx context.Context, w config.Workflow, index int, pinAgent bool) error {
	if err := e.validatePhaseTarget(w, index); err != nil {
		return err
	}
	phase := w.Phases[index]
	// Children inherit the parent ceiling (opts.Rules includes parent phase).
	// Filter phase Allows that would override a parent Deny (AG3); denies always
	// apply. Root engines keep authored phase rules and review true widenings.
	phasePerms := phase.Permissions
	if e.opts.Depth > 0 {
		phasePerms = permission.ChildAgentRules(e.perms.BaselineLayers(), phase.Permissions)
	}
	delta := e.perms.WideningFromPhase(phasePerms)
	if len(delta) > 0 {
		if err := e.approvePhaseWidening(ctx, w, phase, index, delta); err != nil {
			return err
		}
	}

	// Mutate only after validation and widening approval succeed.
	e.workflow = w
	e.phaseIndex = index
	e.phaseRecovery = ""
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
	e.perms.SetPhaseRules(phasePerms)

	e.emitPhaseChanged(phase.Name, index, "")

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

// validatePhaseTarget checks workflow/index before any state mutation.
func (e *Engine) validatePhaseTarget(w config.Workflow, index int) error {
	if strings.TrimSpace(w.Name) == "" {
		return fmt.Errorf("workflow has empty name")
	}
	if index < 0 || index >= len(w.Phases) {
		return fmt.Errorf("workflow %q: phase index %d out of range", w.Name, index)
	}
	phase := w.Phases[index]
	if err := config.ValidatePhaseName(phase.Name); err != nil {
		return fmt.Errorf("workflow %q phase %d: %w", w.Name, index, err)
	}
	return nil
}

// emitPhaseChanged emits the current (or recovery) phase identity.
func (e *Engine) emitPhaseChanged(phaseName string, index int, status string) {
	ev := protocol.PhaseChanged{
		Correlation: e.sessionCorr(),
		Workflow:    e.workflow.Name,
		Phase:       phaseName,
		Index:       index,
		Source:      string(e.workflow.Source),
		Fingerprint: e.workflow.Fingerprint,
		Status:      status,
	}
	if status == "" && phaseName != "" {
		ev.Gate = e.effectiveGateLabel()
	}
	e.emitSelected(ev)
}

// advancePhase clears the current phase exit gate and loads the next phase
// (or ends the workflow). Used by phase_done. Leaving the plan convenience
// plan phase requires exit_plan_mode (unified handoff) — phase_done cannot
// bypass plan approval.
func (e *Engine) advancePhase(ctx context.Context) error {
	if err := e.requirePlanHandoffForPhaseAdvance(); err != nil {
		return err
	}
	if e.phaseRecovery != "" {
		return fmt.Errorf("workflow %q is in resume recovery (%s): stop or restart before advancing",
			e.workflow.Name, e.phaseRecovery)
	}
	if e.phaseIndex < 0 || e.workflow.Name == "" {
		return fmt.Errorf("no active workflow phase")
	}
	phase := e.workflow.Phases[e.phaseIndex]
	if err := e.runExitGate(ctx, phase); err != nil {
		return err
	}
	return e.advancePhaseAfterGate()
}

// advancePhaseAfterGate loads the next phase (or ends the workflow) after the
// exit gate has already been cleared. Used by the unified plan handoff so the
// autonomy ask runs once.
func (e *Engine) advancePhaseAfterGate() error {
	if e.phaseRecovery != "" {
		return fmt.Errorf("workflow %q is in resume recovery (%s): stop or restart before advancing",
			e.workflow.Name, e.phaseRecovery)
	}
	if e.phaseIndex < 0 || e.workflow.Name == "" {
		return fmt.Errorf("no active workflow phase")
	}
	next := e.phaseIndex + 1
	if next >= len(e.workflow.Phases) {
		e.clearPhase()
		return nil
	}
	// Validate next before mutating away from the current phase.
	if err := e.validatePhaseTarget(e.workflow, next); err != nil {
		return err
	}
	return e.enterPhaseOpts(e.phaseReviewCtx(nil), e.workflow, next, true)
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

// restoreWorkflowPhase re-enters a recorded phase after session resume.
// When fingerprint is non-empty it must match the loaded definition; otherwise
// a fail-closed recovery state is surfaced (no phase permissions applied).
// Legacy resumes with empty fingerprint bind to the current loaded definition.
func (e *Engine) restoreWorkflowPhase(name string, index int, phaseName, fingerprint string) {
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	w, ok := e.findWorkflow(name)
	if !ok {
		e.enterPhaseRecovery(name, phaseName, index, fingerprint, "", protocol.PhaseStatusMissing)
		return
	}
	fp := strings.TrimSpace(fingerprint)
	if fp != "" && w.Fingerprint != "" && !strings.EqualFold(fp, w.Fingerprint) {
		e.enterPhaseRecovery(name, phaseName, index, fingerprint, string(w.Source), protocol.PhaseStatusMismatch)
		return
	}
	if index < 0 || index >= len(w.Phases) {
		e.enterPhaseRecovery(name, phaseName, index, fingerprint, string(w.Source), protocol.PhaseStatusMismatch)
		return
	}
	if pn := strings.TrimSpace(phaseName); pn != "" && w.Phases[index].Name != pn {
		e.enterPhaseRecovery(name, phaseName, index, fingerprint, string(w.Source), protocol.PhaseStatusMismatch)
		return
	}
	// Healthy restore: apply the fingerprinted (or legacy name-matched) def.
	_ = e.enterPhase(w, index)
}

// enterPhaseRecovery records fail-closed resume state without applying
// phase permissions or agent pins. Always emits (even during QuietStartup)
// so displayed recovery cannot desync from enforced empty phase rules.
func (e *Engine) enterPhaseRecovery(name, phaseName string, index int, fingerprint, source, status string) {
	e.perms.SetPhaseRules(nil)
	e.phaseGrantApproval = PhaseGrantApproval{}
	e.workflow = config.Workflow{
		Name:        name,
		Source:      config.WorkflowSource(source),
		Fingerprint: fingerprint,
	}
	e.phaseIndex = index
	if e.phaseIndex < 0 {
		e.phaseIndex = 0
	}
	e.phaseRecovery = status
	ev := protocol.PhaseChanged{
		Correlation: e.sessionCorr(),
		Workflow:    e.workflow.Name,
		Phase:       phaseName,
		Index:       e.phaseIndex,
		Source:      string(e.workflow.Source),
		Fingerprint: e.workflow.Fingerprint,
		Status:      status,
	}
	e.emit(ev)
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
	if name == "review-fix" {
		return config.BuiltinReviewFix(), true
	}
	return config.Workflow{}, false
}

// currentPhase returns the active enforced phase, or false when none / recovery.
func (e *Engine) currentPhase() (config.Phase, bool) {
	if e.phaseRecovery != "" {
		return config.Phase{}, false
	}
	if e.phaseIndex < 0 || e.phaseIndex >= len(e.workflow.Phases) {
		return config.Phase{}, false
	}
	return e.workflow.Phases[e.phaseIndex], true
}

// activeWorkflowHealthy reports whether a fully enforced workflow phase is live.
func (e *Engine) activeWorkflowHealthy() bool {
	_, ok := e.currentPhase()
	return ok
}

// syncPhaseWithAgent keeps displayed and enforced phase state aligned when the
// user switches agents via tab / SelectAgent (outside tool-driven phase_done).
//
// Plan convenience adapters handle the default plan-implement workflow only.
// Generic workflows: if the new agent differs from the phase pin, stop the
// workflow so phase permissions cannot linger under a different persona.
func (e *Engine) syncPhaseWithAgent(agentName string) {
	if e.phaseRecovery != "" {
		// Recovery is display-only until stop/restart; agent switches do not
		// invent enforcement from a stale record.
		return
	}
	if e.isPlanConvenienceWorkflow() {
		e.syncPlanConvenienceWithAgent(agentName)
		return
	}
	phase, ok := e.currentPhase()
	if !ok {
		// No active generic workflow: plan agent still enters plan convenience.
		if agentName == "plan" {
			_ = e.enterPlanPhase()
		}
		return
	}
	pin := strings.TrimSpace(phase.Agent)
	if pin != "" && pin != agentName {
		e.stopWorkflow()
	}
}

// isPlanConvenienceWorkflow reports whether the active workflow is the default
// plan→implement sequence (built-in or DefaultWorkflow override).
func (e *Engine) isPlanConvenienceWorkflow() bool {
	if e.workflow.Name == "" {
		return false
	}
	def := "plan-implement"
	if e.opts.DefaultWorkflow != "" {
		def = e.opts.DefaultWorkflow
	}
	return e.workflow.Name == def || e.workflow.Name == "plan-implement"
}

// syncPlanConvenienceWithAgent is the plan-mode adapter: selecting plan enters
// the plan workflow. build/orchestrator cannot enter implement without the
// unified plan handoff — manual agent selection abandons the plan workflow
// (clears phase) rather than bypassing approval.
func (e *Engine) syncPlanConvenienceWithAgent(agentName string) {
	switch agentName {
	case "plan":
		if phase, ok := e.currentPhase(); ok && phase.Name == "plan" {
			return
		}
		_ = e.enterPlanPhase()
	case "build", "orchestrator":
		if phase, ok := e.currentPhase(); ok && phase.Name == "plan" {
			// Already handed off: allow implement alignment without re-pinning.
			if e.planHandoff.Active {
				w := e.workflow
				for i, p := range w.Phases {
					if p.Name == "implement" || p.Agent == "build" || p.Agent == "orchestrator" {
						_ = e.enterPhaseOpts(e.phaseReviewCtx(nil), w, i, false)
						return
					}
				}
			}
			// No handoff: abandon plan workflow. Do not enter implement.
			e.clearPhase()
			return
		}
		if e.phaseIndex >= 0 {
			// Leaving a non-plan phase via agent switch ends the workflow.
			if phase, ok := e.currentPhase(); ok && phase.Agent != "" && phase.Agent != agentName {
				e.clearPhase()
			}
		}
	default:
		// Other agents leave plan convenience when the pin no longer matches.
		if phase, ok := e.currentPhase(); ok {
			pin := strings.TrimSpace(phase.Agent)
			if pin != "" && pin != agentName {
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
	// Plan convenience: built-in plan phase uses the embedded plan overlay
	// when Context is empty.
	if e.isPlanConvenienceWorkflow() && (phase.Name == "plan" || phase.Agent == "plan") {
		return PlanSystemPrompt
	}
	return ""
}
