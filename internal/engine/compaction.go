package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

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

	// Bounds for the model-authored summarize path (cost + latency).
	maxSummarizeInputChars = 120_000
	summarizeMaxTokens     = 1024
	summarizeTimeout       = 90 * time.Second

	summarizeSystemPrompt = "You summarize prior conversation history for an AI coding agent. " +
		"Be concise. Preserve decisions, file paths, commands, errors, and unfinished tasks. " +
		"Do not invent details. Output only the summary, no preamble."
)

// compactMarker builds the model-facing replacement for dropped history (trim).
func compactMarker(removed int) string {
	return fmt.Sprintf("%s %d earlier messages omitted. Continue from the recent context below.]", compactMarkerPrefix, removed)
}

// summaryCompactMarker builds the model-facing replacement when summarize succeeds.
func summaryCompactMarker(removed int, summary string) string {
	summary = strings.TrimSpace(summary)
	return fmt.Sprintf("%s summary of %d earlier messages:\n%s\nContinue from the recent context below.]",
		compactMarkerPrefix, removed, summary)
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

func resolveCompactionStrategy(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case protocol.CompactionStrategySummarize, "summary":
		return protocol.CompactionStrategySummarize
	default:
		return protocol.CompactionStrategyTrim
	}
}

// applyCompaction mutates model-facing history when older turns can be dropped.
// Emits CompactionStarted then CompactionCompleted on success.
// strategyOverride, when non-empty, selects trim|summarize for this call only.
func (e *Engine) applyCompaction(ctx context.Context, reason string, corr protocol.Correlation, strategyOverride string) bool {
	keep := e.opts.KeepUserTurns
	if keep < 1 {
		keep = defaultKeepUserTurns
	}
	split := findCompactSplit(e.messages, keep)
	if split <= 0 {
		return false
	}
	tail := e.messages[split:]
	if !historyToolPairsValid(tail) {
		return false
	}
	dropped := append([]provider.Message(nil), e.messages[:split]...)
	removed := split
	keptTail := len(tail)

	requested := resolveCompactionStrategy(strategyOverride)
	if strategyOverride == "" {
		requested = resolveCompactionStrategy(e.opts.CompactionStrategy)
	}

	e.emit(protocol.CompactionStarted{
		Correlation: corr,
		Reason:      reason,
		Strategy:    requested,
	})

	marker := compactMarker(removed)
	applied := protocol.CompactionStrategyTrim
	summary := ""

	if requested == protocol.CompactionStrategySummarize {
		s, err := e.summarizeHistory(ctx, dropped)
		if err != nil || strings.TrimSpace(s) == "" {
			msg := "summarize compaction failed, fell back to trim"
			if err != nil {
				msg = msg + ": " + err.Error()
			} else {
				msg = msg + ": empty summary"
			}
			e.emit(protocol.EngineError{Correlation: corr, Message: msg})
		} else {
			summary = strings.TrimSpace(s)
			marker = summaryCompactMarker(removed, summary)
			applied = protocol.CompactionStrategySummarize
		}
	}

	out := make([]provider.Message, 0, 1+keptTail)
	out = append(out, provider.Message{Role: provider.RoleUser, Text: marker})
	out = append(out, tail...)
	e.messages = out
	e.lastUsed = 0
	e.lastUsedKnown = false
	e.emit(protocol.CompactionCompleted{
		Correlation: corr,
		Reason:      reason,
		Strategy:    applied,
		Removed:     removed,
		Kept:        keptTail + 1,
		Summary:     summary,
	})
	return true
}

func (e *Engine) handleCompact(ctx context.Context, op protocol.Compact) {
	if e.turnActive() {
		e.emit(protocol.EngineError{
			Correlation: e.sessionCorr(),
			Message:     "cannot compact while a turn is running",
		})
		return
	}
	if !e.applyCompaction(ctx, protocol.CompactionReasonManual, e.sessionCorr(), op.Strategy) {
		e.emit(protocol.EngineError{
			Correlation: e.sessionCorr(),
			Message:     "nothing to compact",
		})
	}
}

// summarizeHistory runs a tools-free model call over dropped turns and returns
// the assistant text. Does not emit TextDelta (not part of the user transcript).
// Never executes tools; completed mutating tools are not replayed.
func (e *Engine) summarizeHistory(ctx context.Context, dropped []provider.Message) (string, error) {
	if e.prov == nil {
		return "", errors.New("no provider")
	}
	body := formatDroppedForSummary(dropped)
	if strings.TrimSpace(body) == "" {
		return "", errors.New("nothing to summarize")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, summarizeTimeout)
		defer cancel()
	}
	model := e.model
	if m := strings.TrimSpace(e.opts.CompactionModel); m != "" {
		model = m
	}
	stream, err := e.prov.Stream(ctx, provider.Request{
		Model:  model,
		System: summarizeSystemPrompt,
		Messages: []provider.Message{{
			Role: provider.RoleUser,
			Text: "Summarize this conversation history:\n\n" + body,
		}},
		MaxTokens: summarizeMaxTokens,
	})
	if err != nil {
		return "", err
	}
	stream = provider.NormalizeStream(stream)
	var text strings.Builder
	var streamErr error
	terminated := false
	for ev := range stream {
		if terminated {
			continue
		}
		switch ev.Type {
		case provider.EventTextDelta:
			text.WriteString(ev.Text)
		case provider.EventDone:
			terminated = true
		case provider.EventError:
			terminated = true
			streamErr = ev.Err
			if streamErr == nil {
				streamErr = errors.New("provider stream error")
			}
		}
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if !terminated {
		return "", provider.ErrIncompleteStream
	}
	if streamErr != nil {
		return "", streamErr
	}
	s := strings.TrimSpace(text.String())
	if s == "" {
		return "", errors.New("empty summary")
	}
	return s, nil
}

// formatDroppedForSummary flattens dropped messages into a bounded plain-text
// block for the summarizer. Tool args/outputs are truncated.
func formatDroppedForSummary(msgs []provider.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		if b.Len() >= maxSummarizeInputChars {
			break
		}
		switch m.Role {
		case provider.RoleUser:
			fmt.Fprintf(&b, "User: %s\n\n", m.Text)
		case provider.RoleAssistant:
			if m.Text != "" {
				fmt.Fprintf(&b, "Assistant: %s\n", m.Text)
			} else {
				b.WriteString("Assistant:\n")
			}
			for _, tc := range m.ToolCalls {
				fmt.Fprintf(&b, "  [tool %s id=%s args=%s]\n",
					tc.Name, tc.ID, truncateRunes(string(tc.Args), 400))
			}
			b.WriteByte('\n')
		case provider.RoleTool:
			if m.ToolResult == nil {
				continue
			}
			out := m.ToolResult.Output
			if m.ToolResult.IsError {
				out = "ERROR: " + out
			}
			fmt.Fprintf(&b, "Tool(%s): %s\n\n", m.ToolResult.CallID, truncateRunes(out, 1500))
		}
	}
	s := b.String()
	if len(s) > maxSummarizeInputChars {
		// Byte-safe cut at rune boundary.
		s = truncateRunes(s, maxSummarizeInputChars)
		return s + "\n…[truncated]"
	}
	return s
}

func truncateRunes(s string, max int) string {
	if max <= 0 || s == "" {
		return ""
	}
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max]) + "…"
}

// dropLastUserTurn removes the last real user turn and everything after it
// from model-facing history. Compact markers are not treated as real turns.
// ok is false when there is nothing to drop or the remainder would be invalid.
func dropLastUserTurn(msgs []provider.Message) (out []provider.Message, ok bool) {
	lastUser := -1
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != provider.RoleUser {
			continue
		}
		if strings.HasPrefix(msgs[i].Text, compactMarkerPrefix) {
			continue
		}
		lastUser = i
		break
	}
	if lastUser < 0 {
		return msgs, false
	}
	out = msgs[:lastUser]
	if !historyToolPairsValid(out) {
		return msgs, false
	}
	// Copy so callers can retain the original slice header safely.
	kept := make([]provider.Message, len(out))
	copy(kept, out)
	return kept, true
}

func (e *Engine) handleRewind() {
	if e.turnActive() {
		e.emit(protocol.EngineError{
			Correlation: e.sessionCorr(),
			Message:     "cannot rewind while a turn is running",
		})
		return
	}
	next, ok := dropLastUserTurn(e.messages)
	if !ok {
		e.emit(protocol.EngineError{
			Correlation: e.sessionCorr(),
			Message:     "nothing to rewind",
		})
		return
	}
	removed := len(e.messages) - len(next)
	e.messages = next
	e.lastUsed = 0
	e.lastUsedKnown = false
	e.emit(protocol.SessionRewound{
		Correlation: e.sessionCorr(),
		Removed:     removed,
	})
}

// maybeThresholdCompact drops older history when occupancy approaches the
// model context window. No-ops when the window is unknown and the estimate
// cannot be compared, or when nothing can be removed.
func (e *Engine) maybeThresholdCompact(ctx context.Context, turnID string) {
	if !e.overCompactionThreshold() {
		return
	}
	corr := e.baseCorr()
	corr.TurnID = turnID
	e.applyCompaction(ctx, protocol.CompactionReasonThreshold, corr, "")
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
