package goal

import (
	"fmt"
	"strings"
)

// HookDecision is the pre_act outcome.
type HookDecision int

const (
	HookAllow HookDecision = iota
	HookBlock
	HookTransform
)

// PreActResult is returned by pre-act hooks.
type PreActResult struct {
	Decision HookDecision
	Reason   string
	Action   Action // set when Decision == HookTransform
}

// Hooks are deterministic enforcement points (never LLM).
type Hooks struct {
	// PreAct runs before each action. nil allows all (still subject to allowlist).
	PreAct func(g Goal, action Action) PreActResult
	// PostAct runs after each action (audit / cost). Optional.
	PostAct func(g Goal, action Action, result ActionRecord)
	// PreCommit runs before persisting an iteration. Return error to block commit.
	PreCommit func(g Goal, rec IterationRecord) error
}

// DefaultHooks enforces the tool allowlist in pre_act.
func DefaultHooks() Hooks {
	return Hooks{
		PreAct: AllowlistPreAct,
	}
}

// AllowlistPreAct blocks tools not in Constraints.AllowedTools.
// Empty allowlist blocks every tool action (evaluate-only loop).
func AllowlistPreAct(g Goal, action Action) PreActResult {
	tool := strings.TrimSpace(action.Tool)
	if tool == "" {
		return PreActResult{Decision: HookBlock, Reason: "empty tool name"}
	}
	allowed := g.Constraints.AllowedTools
	if len(allowed) == 0 {
		return PreActResult{
			Decision: HookBlock,
			Reason:   "no tools allowed (empty allowlist; evaluate-only)",
		}
	}
	for _, a := range allowed {
		if strings.EqualFold(strings.TrimSpace(a), tool) {
			return PreActResult{Decision: HookAllow}
		}
	}
	return PreActResult{
		Decision: HookBlock,
		Reason:   fmt.Sprintf("tool %q not in allowlist", tool),
	}
}

// ChainPreAct runs hooks in order; first block wins; transforms accumulate.
func ChainPreAct(hooks ...func(Goal, Action) PreActResult) func(Goal, Action) PreActResult {
	return func(g Goal, action Action) PreActResult {
		cur := action
		for _, h := range hooks {
			if h == nil {
				continue
			}
			r := h(g, cur)
			switch r.Decision {
			case HookBlock:
				return r
			case HookTransform:
				cur = r.Action
			}
		}
		return PreActResult{Decision: HookAllow, Action: cur}
	}
}
