package engine

import (
	"fmt"
	"strings"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/provider"
)

const (
	// defaultKeepUserTurns is how many trailing user-turn starts to preserve
	// when compacting (the current intent plus one prior turn by default).
	defaultKeepUserTurns = 2
	// defaultCompactionThreshold triggers automatic compaction when occupancy
	// reaches this fraction of the known context window.
	defaultCompactionThreshold = 0.80
	// defaultCompactionBuffer reserves headroom (output allowance) so threshold
	// compaction fires before hard exhaustion.
	defaultCompactionBuffer = 4096

	compactMarkerPrefix = "[Prior conversation compacted —"
)

// compactMarker builds the model-facing replacement for dropped history.
func compactMarker(removed int) string {
	return fmt.Sprintf("%s %d earlier messages omitted. Continue from the recent context below.]", compactMarkerPrefix, removed)
}

// compactMessages replaces older history with a single user marker while
// preserving a bounded recent tail at valid turn/tool boundaries.
// ok is false when nothing can be removed without dropping the required tail.
func compactMessages(msgs []provider.Message, keepUserTurns int) (out []provider.Message, removed, kept int, ok bool) {
	if keepUserTurns < 1 {
		keepUserTurns = defaultKeepUserTurns
	}
	split := findCompactSplit(msgs, keepUserTurns)
	if split <= 0 {
		return msgs, 0, len(msgs), false
	}
	tail := msgs[split:]
	if !historyToolPairsValid(tail) {
		// Should not happen when split is at a user turn; refuse rather than
		// emit dangling tool calls/results.
		return msgs, 0, len(msgs), false
	}
	removed = split
	kept = len(tail)
	out = make([]provider.Message, 0, 1+kept)
	out = append(out, provider.Message{Role: provider.RoleUser, Text: compactMarker(removed)})
	out = append(out, tail...)
	return out, removed, kept + 1, true
}

// findCompactSplit returns the index of the earliest message to keep so that
// at least keepUserTurns user-role messages remain in the tail. Returns 0 when
// there is nothing older to drop.
func findCompactSplit(msgs []provider.Message, keepUserTurns int) int {
	if len(msgs) == 0 {
		return 0
	}
	userSeen := 0
	split := 0
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != provider.RoleUser {
			continue
		}
		// Skip prior compact markers when counting "real" user turns so
		// repeated compaction still drops old content.
		if strings.HasPrefix(msgs[i].Text, compactMarkerPrefix) {
			continue
		}
		userSeen++
		if userSeen >= keepUserTurns {
			split = i
			break
		}
	}
	if userSeen < keepUserTurns {
		// Not enough real user turns — try keeping a single user turn.
		if keepUserTurns > 1 {
			return findCompactSplit(msgs, 1)
		}
		return 0
	}
	if split == 0 {
		return 0
	}
	// If the split lands on a tool result, walk back to the owning assistant.
	// (User-turn splits should not, but be defensive.)
	for split > 0 && msgs[split].Role == provider.RoleTool {
		split--
	}
	return split
}

// historyToolPairsValid reports whether every assistant tool call has a
// matching RoleTool result and every tool result references a prior call.
func historyToolPairsValid(msgs []provider.Message) bool {
	pending := map[string]struct{}{}
	for _, m := range msgs {
		switch m.Role {
		case provider.RoleAssistant:
			for _, c := range m.ToolCalls {
				if c.ID == "" {
					return false
				}
				pending[c.ID] = struct{}{}
			}
		case provider.RoleTool:
			if m.ToolResult == nil || m.ToolResult.CallID == "" {
				return false
			}
			if _, ok := pending[m.ToolResult.CallID]; !ok {
				return false
			}
			delete(pending, m.ToolResult.CallID)
		}
	}
	return len(pending) == 0
}

// estimateTokens is a rough local occupancy estimate (~4 chars/token).
// Used only when provider usage is unknown; labeled estimated at call sites.
func estimateTokens(system string, msgs []provider.Message) int {
	n := len(system)
	for _, m := range msgs {
		n += len(m.Text)
		for _, tc := range m.ToolCalls {
			n += len(tc.Name) + len(tc.Args)
		}
		if m.ToolResult != nil {
			n += len(m.ToolResult.Output) + len(m.ToolResult.CallID)
		}
		for _, r := range m.Reasoning {
			n += len(r)
		}
	}
	if n <= 0 {
		return 0
	}
	return (n + 3) / 4
}

// applyCompaction mutates model-facing history when older turns can be dropped.
// Emits CompactionStarted then CompactionCompleted on success.
func (e *Engine) applyCompaction(reason string, corr protocol.Correlation) bool {
	keep := e.opts.KeepUserTurns
	if keep < 1 {
		keep = defaultKeepUserTurns
	}
	next, removed, kept, ok := compactMessages(e.messages, keep)
	if !ok {
		return false
	}
	e.emit(protocol.CompactionStarted{Correlation: corr, Reason: reason})
	e.messages = next
	e.lastUsed = 0
	e.lastUsedKnown = false
	e.emit(protocol.CompactionCompleted{
		Correlation: corr,
		Reason:      reason,
		Removed:     removed,
		Kept:        kept,
	})
	return true
}

func (e *Engine) handleCompact() {
	if e.turnActive() {
		e.emit(protocol.EngineError{
			Correlation: e.sessionCorr(),
			Message:     "cannot compact while a turn is running",
		})
		return
	}
	if !e.applyCompaction(protocol.CompactionReasonManual, e.sessionCorr()) {
		e.emit(protocol.EngineError{
			Correlation: e.sessionCorr(),
			Message:     "nothing to compact",
		})
	}
}

// maybeThresholdCompact drops older history when occupancy approaches the
// model context window. No-ops when the window is unknown and the estimate
// cannot be compared, or when nothing can be removed.
func (e *Engine) maybeThresholdCompact(turnID string) {
	if !e.overCompactionThreshold() {
		return
	}
	corr := e.baseCorr()
	corr.TurnID = turnID
	e.applyCompaction(protocol.CompactionReasonThreshold, corr)
}

func (e *Engine) overCompactionThreshold() bool {
	window := e.contextWindow()
	if window <= 0 {
		return false
	}
	threshold := e.opts.CompactionThreshold
	if threshold <= 0 {
		threshold = defaultCompactionThreshold
	}
	if threshold >= 1 {
		return false
	}
	used := e.occupancyTokens()
	if used <= 0 {
		return false
	}
	// Absolute headroom: reserve max output + buffer so we compact before the
	// next request is rejected.
	buffer := e.opts.CompactionBuffer
	if buffer <= 0 {
		buffer = defaultCompactionBuffer
	}
	reserve := e.opts.MaxTokens + buffer
	budget := window - reserve
	if budget < window/4 {
		budget = window / 4
	}
	limit := int(threshold * float64(window))
	if budget < limit {
		limit = budget
	}
	return used >= limit
}

func (e *Engine) contextWindow() int {
	if e.contextWindowTokens > 0 {
		return e.contextWindowTokens
	}
	return e.opts.ContextWindow
}

func (e *Engine) occupancyTokens() int {
	if e.lastUsedKnown && e.lastUsed > 0 {
		return e.lastUsed
	}
	return estimateTokens(e.system(), e.messages)
}

func (e *Engine) refreshContextWindow() {
	if e.opts.LookupContextWindow == nil || e.provName == "" || e.model == "" {
		return
	}
	if n := e.opts.LookupContextWindow(e.provName, e.model); n > 0 {
		e.contextWindowTokens = n
	}
}
