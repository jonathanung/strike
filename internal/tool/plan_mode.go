package tool

import (
	"strings"
)

// Post-plan implementer routing thresholds (documented heuristic).
// Complex plans route to orchestrator; simple ones to build.
const (
	postPlanComplexSteps = 4 // do-now steps >= this → orchestrator
	postPlanComplexAreas = 3 // distinct code areas >= this → orchestrator
	// MaxLegacyPlanText bounds the pre-feature text-plan recovery path.
	MaxLegacyPlanText = 32 * 1024
)

// PlanHandoffRequest is the unified plan-mode exit payload.
type PlanHandoffRequest struct {
	// PlanID of the structured plan to approve and hand off (preferred).
	PlanID string
	// ExpectedVersion is the CAS token from plan_read/plan_write (required with PlanID).
	ExpectedVersion int
	// LegacyText is a bounded text plan for pre-feature session recovery when
	// PlanID is empty. Ignored when PlanID is set.
	LegacyText string
	// Agent is an explicit implementer ("build" | "orchestrator"); empty uses heuristic.
	Agent string
	Steps int
	Areas int
	// MultiAgent selects orchestrator when Agent is omitted.
	MultiAgent bool
}

// PlanHandoffResult is returned after a successful unified handoff.
type PlanHandoffResult struct {
	Agent          string // implementer actually applied
	PlanID         string
	PlanVersion    int
	ApprovalSource string
	Title          string
	ViaPhase       bool
	Legacy         bool
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
