package tui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

const agentsWindowID = "agents"

// agentsStateMsg is a snapshot of the live parent's subagents for the agents
// right-pane window. Model owns child lifecycle; the window only renders.
type agentsStateMsg struct {
	parentID  string
	viewingID string
	children  []childActivity
}

// agentsOpenMsg requests opening a child transcript from the agents window.
type agentsOpenMsg struct {
	sessionID string
}

// agentsWindow lists subagents of the current parent session only — running,
// done, or failed — with select-to-open via the existing session_nav path.
type agentsWindow struct {
	parentID  string
	viewingID string
	children  []childActivity
	cursor    int
	width     int
	height    int
}

func newAgentsWindow() agentsWindow {
	return agentsWindow{}
}

func (w agentsWindow) id() string { return agentsWindowID }

func (w agentsWindow) title() string { return "agents" }

func (w agentsWindow) init() tea.Cmd { return nil }

func (w agentsWindow) update(msg tea.Msg) (window, tea.Cmd) {
	switch msg := msg.(type) {
	case agentsStateMsg:
		w.parentID = msg.parentID
		w.viewingID = msg.viewingID
		w.children = append([]childActivity(nil), msg.children...)
		w.cursor = clampAgentsCursor(w.cursor, len(w.children))
		return w, nil
	case tea.KeyMsg:
		return w.handleKey(msg)
	}
	return w, nil
}

func (w agentsWindow) resize(width, height int) window {
	w.width, w.height = max(0, width), max(0, height)
	return w
}

func (w agentsWindow) view(th theme.Theme) string {
	if w.width <= 0 {
		return ""
	}
	th = th.Resolve()
	st := th.S()
	visible := w.height
	if visible < 1 {
		visible = 0
	}
	rows := w.listableChildren()
	if len(rows) == 0 {
		return lipgloss.NewStyle().Width(max(1, w.width)).Render(
			st.Muted.Render("no subagents this session"),
		)
	}
	items := make([]ui.ListItem, len(rows))
	for i, ch := range rows {
		items[i] = agentsListItem(th, ch, w.viewingID == ch.sessionID)
	}
	return ui.List(th, ui.ListOpts{
		Items:   items,
		Cursor:  clampAgentsCursor(w.cursor, len(rows)),
		Width:   w.width,
		Visible: visible,
		Empty:   "no subagents this session",
	})
}

func (w agentsWindow) handleKey(msg tea.KeyMsg) (agentsWindow, tea.Cmd) {
	rows := w.listableChildren()
	if len(rows) == 0 {
		return w, nil
	}
	w.cursor = clampAgentsCursor(w.cursor, len(rows))
	switch msg.String() {
	case "up", "k":
		if w.cursor > 0 {
			w.cursor--
		}
	case "down", "j":
		if w.cursor < len(rows)-1 {
			w.cursor++
		}
	case "enter", "right", "l":
		ch := rows[w.cursor]
		id := strings.TrimSpace(ch.sessionID)
		if id == "" || id == "child" {
			return w, nil
		}
		return w, func() tea.Msg { return agentsOpenMsg{sessionID: id} }
	}
	return w, nil
}

// listableChildren drops placeholder spawn rows without a real session id.
func (w agentsWindow) listableChildren() []childActivity {
	if len(w.children) == 0 {
		return nil
	}
	out := make([]childActivity, 0, len(w.children))
	for _, ch := range w.children {
		if ch.sessionID == "" || ch.sessionID == "child" {
			continue
		}
		out = append(out, ch)
	}
	return out
}

func clampAgentsCursor(cursor, n int) int {
	if n <= 0 {
		return 0
	}
	if cursor < 0 {
		return 0
	}
	if cursor >= n {
		return n - 1
	}
	return cursor
}

func agentsListItem(th theme.Theme, ch childActivity, current bool) ui.ListItem {
	th = th.Resolve()

	agent := sanitizeDisplayData(ch.agent)
	if agent == "" {
		agent = "subagent"
	}
	label := agent
	if short := shortSessionID(ch.sessionID); short != "" {
		label = agent + themedSpace(th.Spacing.XS) + short
	}

	statusLabel, glyph, statusStyle := agentsStatusParts(th, ch.status)
	detail := statusLabel
	if age := agentsAgeLabel(ch, time.Now()); age != "" {
		detail = statusLabel + themedSpace(th.Spacing.XS) + age
	}
	// Pre-style glyph so running keeps the in-progress accent (pairs with #190).
	suffix := statusStyle.Render(themedSpace(th.Spacing.XS) + glyph)
	return ui.ListItem{
		Label:   label,
		Detail:  detail,
		Suffix:  suffix,
		Current: current,
	}
}

func agentsStatusParts(th theme.Theme, status string) (label, glyph string, style lipgloss.Style) {
	th = th.Resolve()
	st := th.S()
	ic := iconsFor(th)
	switch status {
	case "running":
		return "running", ic.Ellipsis, st.AccentAlt
	case string(protocol.ChildStatusCompleted):
		return "done", ic.OK, st.Success
	case string(protocol.ChildStatusFailed):
		return "failed", ic.Err, st.Error
	case string(protocol.ChildStatusCanceled):
		return "canceled", ic.Info, st.Muted
	default:
		if status == "" {
			status = "unknown"
		}
		return status, ic.Dot, st.Muted
	}
}

func agentsAgeLabel(ch childActivity, now time.Time) string {
	if ch.startedAt.IsZero() {
		return ""
	}
	end := now
	if !ch.endedAt.IsZero() {
		end = ch.endedAt
	}
	d := end.Sub(ch.startedAt)
	if d < 0 {
		d = 0
	}
	return formatCompactDuration(d)
}
