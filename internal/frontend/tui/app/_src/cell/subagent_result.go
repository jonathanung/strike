package tui

import (
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/frontend/tui/theme"
	"github.com/jonathanung/strike-cli/internal/protocol"
)

// subagentResultCell is one child/subagent completion in the parent transcript.
// Collapsed by default to a single summary row; expand shows the full result.
type subagentResultCell struct {
	sessionID string
	agent     string
	status    string // completed | failed | canceled | blocked
	summary   string
	elapsed   time.Duration // 0 when unknown (e.g. replay without timestamps)
	// verificationLabel is claim-vs-verified chip text when gates ran (#809).
	verificationLabel string
	// verificationOK distinguishes verified (success tone) from claimed/failed.
	verificationOK bool
	expanded       bool
	selected       bool
	copiedFlash    bool
}

func (c *subagentResultCell) collapsible() bool {
	return c != nil
}

func (c *subagentResultCell) toggleExpanded() bool {
	if c == nil {
		return false
	}
	c.expanded = !c.expanded
	return true
}

func (c *subagentResultCell) copyText() string {
	if c == nil {
		return ""
	}
	return strings.TrimRight(c.summary, "\n")
}

func (c *subagentResultCell) isError() bool {
	if c == nil {
		return false
	}
	return c.status == string(protocol.ChildStatusFailed) ||
		c.status == string(protocol.ChildStatusCanceled) ||
		c.status == string(protocol.ChildStatusBlocked)
}

func (c *subagentResultCell) render(width int, th theme.Theme) string {
	th = th.Resolve()
	ic := iconsFor(th)
	st := th.S()
	space := themedSpace(th.Spacing.XS)
	dot := space + ic.Dot + space

	labelStyle := st.ToolLabel
	if c.selected {
		labelStyle = st.Selected
	}
	statusLabel, statusStyle := subagentStatusStyle(th, c.status)
	if c.copiedFlash {
		statusStyle = st.Success
		statusLabel = "copied"
	}

	glyph := ic.TreeCollapsed
	if c.expanded {
		glyph = ic.TreeExpanded
	}
	marker := labelStyle.Render(glyph) + space

	agent := strings.TrimSpace(c.agent)
	if agent == "" {
		agent = "subagent"
	}
	// Build styled segments so status carries its tone on the collapsed row.
	parts := []string{labelStyle.Render(agent)}
	if short := shortSessionID(c.sessionID); short != "" {
		parts = append(parts, labelStyle.Render(short))
	}
	parts = append(parts, statusStyle.Render(statusLabel))
	if v := strings.TrimSpace(c.verificationLabel); v != "" {
		vStyle := st.Warning
		if c.verificationOK {
			vStyle = st.Success
		} else if c.status == string(protocol.ChildStatusFailed) {
			vStyle = st.Error
		}
		parts = append(parts, vStyle.Render(v))
	}
	if c.elapsed > 0 {
		parts = append(parts, st.Muted.Render(formatCompactDuration(c.elapsed)))
	}
	head := strings.Join(parts, labelStyle.Render(dot))
	if snippet := firstLine(c.summary); snippet != "" {
		sep := labelStyle.Render(space + ic.DetailSeparator + space)
		head += sep + st.Muted.Render(snippet)
	}

	// Width-safe: strip, truncate plain, fall back to plain styled cut.
	prefixW := lipgloss.Width(marker)
	budget := max(1, width-prefixW)
	if plain := ansi.Strip(head); ansi.StringWidth(plain) > budget {
		head = labelStyle.Render(welcomeTruncate(plain, budget, ic.Ellipsis))
	}
	out := marker + head

	if !c.expanded {
		return out
	}

	body := strings.TrimRight(c.summary, "\n")
	if body == "" {
		body = statusLabel
	}
	indentPrefix := themedSpace(th.Spacing.SM) + st.BorderMuted.Render(ic.ToolGuide) + space
	bodyWidth := max(1, width-lipgloss.Width(indentPrefix))
	bodyStyle := st.Muted
	switch c.status {
	case string(protocol.ChildStatusFailed):
		bodyStyle = st.Error
	case string(protocol.ChildStatusCanceled), string(protocol.ChildStatusBlocked):
		bodyStyle = st.Warning
	}
	return out + "\n" + indent(renderCellText(bodyStyle, body, bodyWidth), indentPrefix)
}

func subagentStatusStyle(th theme.Theme, status string) (label string, style lipgloss.Style) {
	th = th.Resolve()
	st := th.S()
	switch status {
	case string(protocol.ChildStatusCompleted), "":
		return string(protocol.ChildStatusCompleted), st.Success
	case string(protocol.ChildStatusFailed):
		return string(protocol.ChildStatusFailed), st.Error
	case string(protocol.ChildStatusCanceled):
		return string(protocol.ChildStatusCanceled), st.Warning
	case string(protocol.ChildStatusBlocked):
		return string(protocol.ChildStatusBlocked), st.Warning
	default:
		if status == "" {
			status = "unknown"
		}
		return status, st.Muted
	}
}

// firstLine returns the first non-empty line of s (trimmed).
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

// newSubagentResultCell builds a collapsed subagent result row from a
// ChildCompleted event and optional agent/elapsed metadata.
func newSubagentResultCell(ev protocol.ChildCompleted, agent string, elapsed time.Duration) *subagentResultCell {
	status := string(ev.Status)
	if status == "" {
		status = string(protocol.ChildStatusCompleted)
	}
	return &subagentResultCell{
		sessionID: strings.TrimSpace(ev.SessionID),
		agent:     strings.TrimSpace(agent),
		status:    status,
		summary:   strings.TrimSpace(ev.Summary),
		elapsed:   elapsed,
	}
}

// appendSubagentResultCell adds a subagent result row unless one already exists
// for the same session id (exactly-once completion UI).
func appendSubagentResultCell(cells []cell, ev protocol.ChildCompleted, agent string, elapsed time.Duration) []cell {
	id := strings.TrimSpace(ev.SessionID)
	if id != "" {
		for _, c := range cells {
			if sc, ok := c.(*subagentResultCell); ok && sc.sessionID == id {
				// Refresh terminal fields; keep expanded/selection UI state.
				sc.status = string(ev.Status)
				if sc.status == "" {
					sc.status = string(protocol.ChildStatusCompleted)
				}
				if s := strings.TrimSpace(ev.Summary); s != "" {
					sc.summary = s
				}
				if agent != "" {
					sc.agent = agent
				}
				if elapsed > 0 {
					sc.elapsed = elapsed
				}
				if ev.Verification != nil {
					applyVerificationToSubagent(sc, ev.Verification)
				}
				return cells
			}
		}
	}
	sc := newSubagentResultCell(ev, agent, elapsed)
	if ev.Verification != nil {
		applyVerificationToSubagent(sc, ev.Verification)
	}
	return append(cells, sc)
}

// applySubagentVerification stamps claim-vs-verified labels onto an existing
// subagent row (or no-ops when the session is missing).
func applySubagentVerification(cells []cell, sessionID string, rep *protocol.VerificationReport) {
	if rep == nil {
		return
	}
	id := strings.TrimSpace(sessionID)
	for _, c := range cells {
		sc, ok := c.(*subagentResultCell)
		if !ok {
			continue
		}
		if id != "" && sc.sessionID != id {
			continue
		}
		applyVerificationToSubagent(sc, rep)
		return
	}
}

func applyVerificationToSubagent(sc *subagentResultCell, rep *protocol.VerificationReport) {
	if sc == nil || rep == nil {
		return
	}
	label, _, ok := verificationBadgeLabel(rep)
	if !ok {
		return
	}
	sc.verificationLabel = label
	sc.verificationOK = rep.Verified && rep.Passed
}

// lookupChildMeta finds agent name and elapsed duration for a child session.
func lookupChildMeta(children []childActivity, sessionID string) (agent string, elapsed time.Duration) {
	id := strings.TrimSpace(sessionID)
	for i := range children {
		ch := children[i]
		if id != "" && ch.sessionID != id {
			continue
		}
		if id == "" && i != len(children)-1 {
			continue
		}
		agent = ch.agent
		if !ch.startedAt.IsZero() {
			end := ch.endedAt
			if end.IsZero() {
				end = time.Now()
			}
			if d := end.Sub(ch.startedAt); d > 0 {
				elapsed = d
			}
		}
		return agent, elapsed
	}
	return "", 0
}
