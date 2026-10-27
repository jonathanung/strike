package goal

import (
	"fmt"
	"strings"
)

// ToolDenied reports whether tool matches a deny entry. "*" denies every tool.
// Matching is case-insensitive. Deny wins over the allowlist and over autopilot.
func ToolDenied(tool string, deny []string) bool {
	tool = strings.TrimSpace(tool)
	if tool == "" {
		return false
	}
	for _, d := range deny {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		if d == "*" || strings.EqualFold(d, tool) {
			return true
		}
	}
	return false
}

// ValidateAutopilot rejects hands-off runs that have no allowlist.
// Autopilot is not a skip-all switch: an empty allowlist runs no tools,
// and deny rules are checked separately.
func ValidateAutopilot(on bool, c Constraints) error {
	if !on {
		return nil
	}
	if len(c.AllowedTools) == 0 {
		return fmt.Errorf("goal: autopilot requires an allowlist; deny rules still apply and an empty allowlist runs no tools")
	}
	return nil
}

// GuardedPreAct is the production pre-act hook.
// Order: empty name, deny rule, then the allowlist.
// Autopilot without an allowlist blocks every tool. Autopilot never overrides a deny.
func GuardedPreAct(g Goal, action Action) PreActResult {
	tool := strings.TrimSpace(action.Tool)
	if tool == "" {
		return PreActResult{Decision: HookBlock, Reason: "empty tool name"}
	}
	if ToolDenied(tool, g.Constraints.DenyTools) {
		return PreActResult{
			Decision: HookBlock,
			Reason:   fmt.Sprintf("deny rule blocks %q", tool),
		}
	}
	if g.Autopilot && len(g.Constraints.AllowedTools) == 0 {
		return PreActResult{
			Decision: HookBlock,
			Reason:   "autopilot refuses an empty allowlist",
		}
	}
	return AllowlistPreAct(g, action)
}

// EstimateTokens is the harness token meter until a provider usage event
// is wired. It counts plan, action, and critic text at 4 bytes per token,
// and every committed iteration spends at least one token so a token cap
// cannot be skipped by an empty evaluate-only pass.
func EstimateTokens(plan string, actions []ActionRecord, ev EvalRecord) int {
	n := len(plan)
	for _, a := range actions {
		n += len(a.Tool) + len(a.Result) + len(a.Error) + len(a.BlockReason)
	}
	for _, c := range ev.Criteria {
		n += len(c.Description) + len(c.Evidence) + len(c.Error)
	}
	tokens := n / 4
	if tokens < 1 {
		tokens = 1
	}
	return tokens
}
