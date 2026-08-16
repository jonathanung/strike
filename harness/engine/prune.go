package engine

import (
	"strings"

	"github.com/jonathanung/strike-cli/harness/provider"
)

const (
	// pruneProtect keeps roughly this many tokens of recent tool output intact
	// while walking history backward (OpenCode PRUNE_PROTECT).
	pruneProtect = 40_000
	// pruneMinimum requires at least this many tokens would be freed before
	// mutating history (OpenCode PRUNE_MINIMUM) — avoids thrashing on small wins.
	pruneMinimum = 20_000
	// prunedToolResultText replaces blanked tool-result bodies (peer wording).
	prunedToolResultText = "[Old tool result content cleared]"
	// pruneRecentUserTurns skips tool results inside the most recent N real
	// user turns so the active intent's tool I/O stays complete.
	pruneRecentUserTurns = 2
)

// Tools whose results must stay available to the model after prune.
var pruneProtectedTools = map[string]struct{}{
	"skill": {},
}

// pruneParams holds resolved thresholds for one prune pass. Zero fields are
// filled from the package defaults by resolvePruneParams.
type pruneParams struct {
	protect         int
	minimum         int
	recentUserTurns int
	protectedTools  map[string]struct{}
}

func resolvePruneParams(protect, minimum, recent int, extraTools []string) pruneParams {
	p := pruneParams{
		protect:         protect,
		minimum:         minimum,
		recentUserTurns: recent,
	}
	if p.protect <= 0 {
		p.protect = pruneProtect
	}
	if p.minimum <= 0 {
		p.minimum = pruneMinimum
	}
	if p.recentUserTurns <= 0 {
		p.recentUserTurns = pruneRecentUserTurns
	}
	if len(extraTools) == 0 {
		p.protectedTools = pruneProtectedTools
		return p
	}
	tools := make(map[string]struct{}, len(pruneProtectedTools)+len(extraTools))
	for name := range pruneProtectedTools {
		tools[name] = struct{}{}
	}
	for _, raw := range extraTools {
		name := strings.ToLower(strings.TrimSpace(raw))
		if name == "" {
			continue
		}
		tools[name] = struct{}{}
	}
	p.protectedTools = tools
	return p
}

func (e *Engine) pruneParams() pruneParams {
	return resolvePruneParams(
		e.opts.PruneProtectTokens,
		e.opts.PruneMinimumTokens,
		e.opts.PruneKeepUserTurns,
		e.opts.PruneProtectTools,
	)
}

// pruneToolResults blanks older completed tool-result bodies outside the
// protect budget while keeping tool_use/tool_result pairing and call structure
// intact. Mutates msgs in place via ToolResult pointers.
//
// Walks backward: protect recent user turns, then ~protect tokens of newer
// tool output; older results beyond that are candidates. Applies only when
// candidate savings exceed minimum. Already-cleared results stop the walk
// (older history was pruned earlier).
//
// Returns how many results were cleared and the approximate token estimate of
// their former bodies (before replacement).
func pruneToolResults(msgs []provider.Message, p pruneParams) (cleared, tokensFreed int) {
	if len(msgs) == 0 {
		return 0, 0
	}
	if p.protect <= 0 {
		p.protect = pruneProtect
	}
	if p.minimum <= 0 {
		p.minimum = pruneMinimum
	}
	if p.recentUserTurns <= 0 {
		p.recentUserTurns = pruneRecentUserTurns
	}
	if p.protectedTools == nil {
		p.protectedTools = pruneProtectedTools
	}

	callTool := make(map[string]string)
	for _, m := range msgs {
		if m.Role != provider.RoleAssistant {
			continue
		}
		for _, tc := range m.ToolCalls {
			if tc.ID != "" {
				callTool[tc.ID] = tc.Name
			}
		}
	}

	type cand struct {
		idx int
		est int
	}
	var toPrune []cand
	total := 0
	prunedEst := 0
	userTurns := 0

	for i := len(msgs) - 1; i >= 0; i-- {
		m := &msgs[i]
		if m.Role == provider.RoleUser {
			// Compact markers are not real turns (same rule as findCompactSplit).
			if !strings.HasPrefix(m.Text, compactMarkerPrefix) {
				userTurns++
			}
		}
		if userTurns < p.recentUserTurns {
			continue
		}
		if m.Role != provider.RoleTool || m.ToolResult == nil {
			continue
		}
		out := m.ToolResult.Output
		if out == prunedToolResultText {
			// Prior prune boundary — older results already handled.
			break
		}
		name := callTool[m.ToolResult.CallID]
		if _, ok := p.protectedTools[name]; ok {
			continue
		}
		est := estimateToolOutputTokens(out)
		if est == 0 {
			continue
		}
		total += est
		if total <= p.protect {
			continue
		}
		prunedEst += est
		toPrune = append(toPrune, cand{idx: i, est: est})
	}

	if prunedEst <= p.minimum {
		return 0, 0
	}

	for _, c := range toPrune {
		tr := msgs[c.idx].ToolResult
		if tr == nil || tr.Output == prunedToolResultText {
			continue
		}
		tr.Output = prunedToolResultText
		cleared++
		tokensFreed += c.est
	}
	return cleared, tokensFreed
}

func estimateToolOutputTokens(output string) int {
	if output == "" {
		return 0
	}
	return (len(output) + 3) / 4
}

// maybePruneToolResults shrinks older tool-result bodies on model-facing
// history before the next provider Stream. No-ops when below the minimum free
// threshold. Resets occupancy cache when anything was cleared.
func (e *Engine) maybePruneToolResults() {
	cleared, _ := pruneToolResults(e.messages, e.pruneParams())
	if cleared == 0 {
		return
	}
	e.lastUsed = 0
	e.lastUsedKnown = false
}
