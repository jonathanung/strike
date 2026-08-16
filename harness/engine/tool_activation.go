package engine

import (
	"strings"

	"github.com/jonathanung/strike-cli/pkg/protocol"
)

// Tool family names activated from deterministic workflow state (#991).
const (
	activationFamilyPlan  = "plan"
	activationFamilyChild = "child"
	activationFamilyTeam  = "team"
	activationFamilyTask  = "task-advanced"
)

// planFamilyTools are deferred tools needed during plan mode / active plan work.
var planFamilyTools = []string{
	"plan_write", "plan_read", "plan_delegate",
	"enter_plan_mode", "exit_plan_mode", "phase_done",
}

// childFamilyTools are coordination tools after at least one child exists.
var childFamilyTools = []string{
	"agent_roster", "agent_ownership", "agent_message", "agent_thread",
	"wait",
	"task_status", "task_read", "task_message", "task_interrupt",
	"delegate",
}

// teamFamilyTools are multi-child shared coordination tools.
var teamFamilyTools = []string{
	"agent_broadcast", "team_task", "patch_collab",
}

// applyWorkflowToolActivation discovers deferred tool families and promotes
// progressive schemas from explicit engine state (no classifier / extra model
// round trip). Safe to call on every stream; Discover/PromoteSchema are
// idempotent. Permission hard-deny filtering remains authoritative in
// effectiveToolSchemas after this runs.
//
// Returns the family tokens activated this call (for guidance source metadata).
func (e *Engine) applyWorkflowToolActivation() []string {
	if e == nil || e.opts.Registry == nil {
		return nil
	}
	reg := e.opts.Registry
	var families []string

	if e.shouldActivatePlanFamily() {
		reg.Discover(planFamilyTools...)
		families = append(families, activationFamilyPlan)
	}

	live := e.liveOwnedChildCount()
	hist := e.ownedChildHistoryCount()
	if live >= 1 || hist >= 1 {
		reg.Discover(childFamilyTools...)
		// Advanced task schema once any child lifecycle is in play.
		reg.PromoteSchema("task")
		families = append(families, activationFamilyChild, activationFamilyTask)
	}

	if live >= 2 {
		reg.Discover(teamFamilyTools...)
		families = append(families, activationFamilyTeam)
	}

	return uniqueStrings(families)
}

// shouldActivatePlanFamily reports plan mode, active workflow phase, plan
// agent persona, or an active structured plan handoff.
func (e *Engine) shouldActivatePlanFamily() bool {
	if e == nil {
		return false
	}
	if e.permMode == protocol.PermissionModePlan {
		return true
	}
	// Active workflow phase (plan-implement or any healthy phase).
	if e.activeWorkflowHealthy() {
		return true
	}
	// Plan agent persona.
	if strings.EqualFold(strings.TrimSpace(e.agent.Name), "plan") {
		return true
	}
	// Structured plan handoff still active (implement with plan context).
	if e.planHandoff.Active && strings.TrimSpace(e.planHandoff.PlanID) != "" {
		return true
	}
	return false
}

// liveOwnedChildCount is the number of currently live child engines.
func (e *Engine) liveOwnedChildCount() int {
	if e == nil {
		return 0
	}
	e.childMu.Lock()
	defer e.childMu.Unlock()
	return len(e.children)
}

// ownedChildHistoryCount is terminal children retained for status/read.
func (e *Engine) ownedChildHistoryCount() int {
	if e == nil {
		return 0
	}
	e.childMu.Lock()
	defer e.childMu.Unlock()
	return len(e.childHistory)
}

// activationSourceSuffix formats family tokens for the tools prompt layer source.
func activationSourceSuffix(families []string) string {
	if len(families) == 0 {
		return ""
	}
	return "+activate:" + strings.Join(families, ",")
}
