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

// agentsRootSnap is one parent row plus its subagent children for the tree.
type agentsRootSnap struct {
	ID       string
	Title    string
	State    theme.AgentState
	Children []childActivity
}

// agentsStateMsg is a snapshot of concurrent roots + subagents for the agents
// right-pane window. Model owns lifecycle; the window only renders and emits
// selection/spawn/interrupt intents.
type agentsStateMsg struct {
	activeID  string
	viewingID string
	roots     []agentsRootSnap
}

// agentsOpenMsg requests focusing a tree node (root activate or child transcript).
type agentsOpenMsg struct {
	sessionID string
	interrupt bool
}

// agentsSpawnMsg requests a new concurrent parent session.
type agentsSpawnMsg struct{}

// agentsInterruptMsg requests interrupt of the selected session.
type agentsInterruptMsg struct {
	sessionID string
}

// agentsWindow is a multi-root session tree: top-level = parent agents, nested
// = that parent's subagents. Keys: j/k move, enter select, n spawn, x interrupt,
// space/h/l toggle expand.
type agentsWindow struct {
	activeID  string
	viewingID string
	roots     []agentsRootSnap
	nodes     []ui.TreeNode
	// collapsed records user-collapsed node ids; missing means expanded.
	collapsed map[string]bool
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
		w.activeID = msg.activeID
		w.viewingID = msg.viewingID
		w.roots = append([]agentsRootSnap(nil), msg.roots...)
		w.nodes = w.buildNodes()
		// Keep cursor on the same id when possible.
		rows := ui.FlattenTree(w.nodes)
		if id := w.selectedID(rows); id != "" {
			for i, r := range rows {
				if r.ID == id {
					w.cursor = i
					break
				}
			}
		}
		w.cursor = clampAgentsCursor(w.cursor, len(rows))
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
	if len(w.nodes) == 0 {
		w.nodes = w.buildNodes()
	}
	rows := ui.FlattenTree(w.nodes)
	empty := agentsEmptyLabel(th)
	if len(rows) == 0 {
		return lipgloss.NewStyle().Width(max(1, w.width)).Render(
			st.Muted.Render(empty),
		)
	}
	return ui.Tree(th, ui.TreeOpts{
		Nodes:   w.nodes,
		Cursor:  clampAgentsCursor(w.cursor, len(rows)),
		Width:   w.width,
		Visible: visible,
		Empty:   empty,
	})
}

func agentsEmptyLabel(th theme.Theme) string {
	th = th.Resolve()
	// Match prior empty copy so registry/width tests stay stable; multi-root
	// spawn is advertised via the n keybind in the keys pane.
	return "no subagents this session"
}

func (w agentsWindow) handleKey(msg tea.KeyMsg) (agentsWindow, tea.Cmd) {
	if len(w.nodes) == 0 {
		w.nodes = w.buildNodes()
	}
	rows := ui.FlattenTree(w.nodes)
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
	case "n":
		return w, func() tea.Msg { return agentsSpawnMsg{} }
	case "x", "ctrl+c":
		if len(rows) == 0 {
			return w, nil
		}
		id := rows[w.cursor].ID
		return w, func() tea.Msg { return agentsInterruptMsg{sessionID: id} }
	case " ", "h", "left", "l", "right":
		if len(rows) == 0 {
			return w, nil
		}
		row := rows[w.cursor]
		if row.Expandable {
			if w.collapsed == nil {
				w.collapsed = map[string]bool{}
			}
			if w.collapsed[row.ID] {
				delete(w.collapsed, row.ID)
			} else {
				w.collapsed[row.ID] = true
			}
			w.nodes = w.buildNodes()
			rows = ui.FlattenTree(w.nodes)
			w.cursor = clampAgentsCursor(w.cursor, len(rows))
		} else if msg.String() == "l" || msg.String() == "right" || msg.String() == " " {
			id := row.ID
			return w, func() tea.Msg { return agentsOpenMsg{sessionID: id} }
		}
	case "enter":
		if len(rows) == 0 {
			return w, nil
		}
		id := rows[w.cursor].ID
		return w, func() tea.Msg { return agentsOpenMsg{sessionID: id} }
	}
	return w, nil
}

func (w agentsWindow) selectedID(rows []ui.TreeRow) string {
	if len(rows) == 0 {
		return ""
	}
	i := clampAgentsCursor(w.cursor, len(rows))
	return rows[i].ID
}

func (w agentsWindow) buildNodes() []ui.TreeNode {
	if len(w.roots) == 0 {
		return nil
	}
	out := make([]ui.TreeNode, 0, len(w.roots))
	for _, root := range w.roots {
		id := strings.TrimSpace(root.ID)
		if id == "" {
			continue
		}
		label := strings.TrimSpace(root.Title)
		if label == "" {
			label = shortSessionID(id)
		}
		if label == "" {
			label = "session"
		}
		node := ui.TreeNode{
			ID:      id,
			Label:   label,
			Current: w.viewingID == id || (w.viewingID == "" && w.activeID == id),
			Tone:    agentStateTone(root.State),
			Detail:  agentsRootDetail(root.State),
		}
		kids := listableChildActivities(root.Children)
		if len(kids) == 0 {
			node.Leaf = true
		} else {
			node.Expanded = !w.collapsed[id]
			node.Children = childActivitiesToTree(kids, w.viewingID, w.collapsed)
		}
		out = append(out, node)
	}
	return out
}

func listableChildActivities(children []childActivity) []childActivity {
	if len(children) == 0 {
		return nil
	}
	out := make([]childActivity, 0, len(children))
	for _, ch := range children {
		if ch.sessionID == "" || ch.sessionID == "child" {
			continue
		}
		out = append(out, ch)
	}
	return out
}

func childActivitiesToTree(kids []childActivity, viewingID string, collapsed map[string]bool) []ui.TreeNode {
	// Index by parent for nesting; kids may be a flat list with parentIDs.
	byParent := map[string][]childActivity{}
	ids := map[string]bool{}
	for _, ch := range kids {
		ids[ch.sessionID] = true
	}
	var tops []childActivity
	for _, ch := range kids {
		p := strings.TrimSpace(ch.parentID)
		if p == "" || !ids[p] {
			tops = append(tops, ch)
			continue
		}
		byParent[p] = append(byParent[p], ch)
	}
	var build func(ch childActivity) ui.TreeNode
	build = func(ch childActivity) ui.TreeNode {
		label := childViewTitle(ch.agent, ch.prompt)
		if label == "" {
			label = shortSessionID(ch.sessionID)
		}
		node := ui.TreeNode{
			ID:      ch.sessionID,
			Label:   label,
			Detail:  ch.status,
			Current: viewingID == ch.sessionID,
			Tone:    childStatusTone(ch.status),
		}
		grand := byParent[ch.sessionID]
		if len(grand) == 0 {
			node.Leaf = true
		} else {
			node.Expanded = !collapsed[ch.sessionID]
			node.Children = make([]ui.TreeNode, 0, len(grand))
			for _, g := range grand {
				node.Children = append(node.Children, build(g))
			}
		}
		return node
	}
	out := make([]ui.TreeNode, 0, len(tops))
	for _, ch := range tops {
		out = append(out, build(ch))
	}
	return out
}

func childStatusTone(status string) ui.Tone {
	switch status {
	case "running":
		return ui.ToneAccentAlt
	case string(protocol.ChildStatusCompleted):
		return ui.ToneSuccess
	case string(protocol.ChildStatusFailed):
		return ui.ToneError
	case string(protocol.ChildStatusCanceled):
		return ui.ToneMuted
	default:
		return ui.ToneDefault
	}
}

func agentsRootDetail(state theme.AgentState) string {
	switch state {
	case theme.AgentStateWorking:
		return "working"
	case theme.AgentStateAttention:
		return "attention"
	case theme.AgentStateError:
		return "error"
	case theme.AgentStateDead:
		return "dead"
	default:
		return "ready"
	}
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

// agentsListItem retained for tests that still build status chips.
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
