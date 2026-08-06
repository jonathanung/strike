package tui

import (
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

const agentsWindowID = "agents"

// agentsViewFilter selects which nodes the agents tree shows.
type agentsViewFilter int

const (
	agentsFilterAll agentsViewFilter = iota
	agentsFilterAttention
	agentsFilterWorking
	agentsFilterReady
	agentsFilterRoots
	agentsFilterCount
)

func (f agentsViewFilter) next() agentsViewFilter {
	return (f + 1) % agentsFilterCount
}

func (f agentsViewFilter) label() string {
	switch f {
	case agentsFilterAttention:
		return "needs you"
	case agentsFilterWorking:
		return "working"
	case agentsFilterReady:
		return "ready"
	case agentsFilterRoots:
		return "roots"
	default:
		return "all"
	}
}

// agentsRootSnap is one parent row plus its subagent children for the tree.
type agentsRootSnap struct {
	ID       string
	Title    string
	State    theme.AgentState
	Children []childActivity
	// QueueLabel identifies a constrained pool while waiting (empty when not queued).
	QueueLabel string
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

// agentsHideMsg requests removing the selected entry from the agents pane only
// (ephemeral UI filter). It must not delete, truncate, or interrupt the session.
type agentsHideMsg struct {
	sessionID string
}

// agentsRenameMsg requests renaming the selected root or child session.
type agentsRenameMsg struct {
	sessionID string
}

// agentsHighlightMsg announces the agents-tree cursor target so the visualizer
// can follow selection without requiring Enter.
type agentsHighlightMsg struct {
	sessionID string
}

// agentsWindow is a multi-root session tree: top-level = parent agents, nested
// = that parent's subagents. Keys: j/k move, enter select, n spawn, x interrupt,
// r rename, d hide from pane (keeps session), space/h/l toggle expand, f cycle
// view filter, / text filter.
type agentsWindow struct {
	activeID  string
	viewingID string
	roots     []agentsRootSnap
	nodes     []ui.TreeNode
	// collapsed records user-collapsed node ids; missing means expanded.
	collapsed map[string]bool
	// viewFilter is the active state/structure filter (f cycles).
	viewFilter agentsViewFilter
	// textFilter is an optional name/id substring filter (/ to edit).
	textFilter string
	filterEdit bool
	cursor     int
	width      int
	height     int
}

func newAgentsWindow() agentsWindow {
	return agentsWindow{}
}

func (w agentsWindow) id() string { return agentsWindowID }

func (w agentsWindow) title() string {
	// title() has no theme arg; default tokens keep the panel chrome consistent.
	th := theme.Default().Resolve()
	base := "agents"
	switch w.viewFilter {
	case agentsFilterAttention:
		if n := w.countState(theme.AgentStateAttention); n > 0 {
			return dotJoin(th, base, itoa(n)+" need you")
		}
		return dotJoin(th, base, "needs you")
	case agentsFilterWorking:
		return dotJoin(th, base, "working")
	case agentsFilterReady:
		return dotJoin(th, base, "ready")
	case agentsFilterRoots:
		return dotJoin(th, base, "roots")
	default:
		if n := w.countState(theme.AgentStateAttention); n > 0 {
			return dotJoin(th, base, itoa(n)+" need you")
		}
		return base
	}
}

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
	case tea.KeyPressMsg:
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
	ic := iconsFor(th)
	visible := w.height
	if visible < 1 {
		visible = 0
	}
	// Rebuild so viewFilter/textFilter always match the rendered tree.
	w.nodes = w.buildNodes()
	rows := ui.FlattenTree(w.nodes)
	empty := agentsEmptyLabel(w.viewFilter, w.textFilter)
	showFilter := w.filterEdit || w.textFilter != ""
	bodyH := visible
	var header string
	if showFilter {
		cursor := ""
		if w.filterEdit {
			cursor = ic.FilterCursor
		}
		header = st.Muted.Render("filter: ") + st.Input.Render(sanitizeDisplayData(w.textFilter)+cursor)
		if bodyH > 0 {
			bodyH--
		}
	}
	var body string
	if len(rows) == 0 {
		body = lipgloss.NewStyle().Width(max(1, w.width)).Render(
			st.Muted.Render(empty),
		)
	} else {
		body = ui.Tree(th, ui.TreeOpts{
			Nodes:   w.nodes,
			Cursor:  clampAgentsCursor(w.cursor, len(rows)),
			Width:   w.width,
			Visible: bodyH,
			Empty:   empty,
		})
	}
	if header == "" {
		return body
	}
	if bodyH <= 0 || body == "" {
		return lipgloss.NewStyle().Width(max(1, w.width)).Render(header)
	}
	return header + "\n" + body
}

func agentsEmptyLabel(filter agentsViewFilter, text string) string {
	if q := strings.TrimSpace(text); q != "" {
		return "no matches for \"" + sanitizeDisplayData(q) + "\""
	}
	ak := defaultAgentsKeyMap()
	spawn := ak.Spawn.Help()
	spawnHint := spawn.Key + " " + spawn.Desc
	// title()/empty labels have no theme arg; default tokens keep separators themed.
	th := theme.Default().Resolve()
	switch filter {
	case agentsFilterAttention:
		return "no agents need attention"
	case agentsFilterWorking:
		return "no agents working"
	case agentsFilterReady:
		return "no agents ready"
	case agentsFilterRoots:
		return dotJoin(th, "no parent agents", spawnHint)
	default:
		// Advertise concurrent-root spawn from the keymap (not a duplicated literal).
		return dotJoin(th, "no subagents", spawnHint)
	}
}

// agentsPaneFooter is the agents-window chrome hint row (n/enter/x/d/j/k/f).
// Derived from defaultAgentsKeyMap so /keys and the pane stay aligned.
// width is the panel footer budget (PanelInnerWidth); ui.KeyHints keeps the
// row single-line by dropping whole hints that do not fit.
func agentsPaneFooter(th theme.Theme, width int) string {
	return ui.KeyHints(th, width, agentsPaneKeyHints())
}

// agentsPaneKeyHints is the ordered agents-pane footer binding list.
func agentsPaneKeyHints() []ui.KeyHint {
	ak := defaultAgentsKeyMap()
	hints := make([]ui.KeyHint, 0, 7)
	for _, b := range []key.Binding{ak.Spawn, ak.Open, ak.Interrupt, ak.Rename, ak.Hide, ak.Move, ak.Filter} {
		h := b.Help()
		if h.Key == "" {
			continue
		}
		hints = append(hints, ui.KeyHint{Key: h.Key, Label: h.Desc})
	}
	return hints
}

func (w agentsWindow) handleKey(msg tea.KeyPressMsg) (agentsWindow, tea.Cmd) {
	if w.filterEdit {
		return w.handleFilterEditKey(msg)
	}
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
		return w, w.highlightCmd()
	case "down", "j":
		if w.cursor < len(rows)-1 {
			w.cursor++
		}
		return w, w.highlightCmd()
	case "f":
		w.viewFilter = w.viewFilter.next()
		w.nodes = w.buildNodes()
		rows = ui.FlattenTree(w.nodes)
		w.cursor = clampAgentsCursor(w.cursor, len(rows))
		return w, w.highlightCmd()
	case "/":
		w.filterEdit = true
		return w, nil
	case "n":
		return w, func() tea.Msg { return agentsSpawnMsg{} }
	case "x", "ctrl+c":
		if len(rows) == 0 {
			return w, nil
		}
		id := rows[w.cursor].ID
		return w, func() tea.Msg { return agentsInterruptMsg{sessionID: id} }
	case "r":
		if len(rows) == 0 {
			return w, nil
		}
		id := rows[w.cursor].ID
		return w, func() tea.Msg { return agentsRenameMsg{sessionID: id} }
	case "d":
		if len(rows) == 0 {
			return w, nil
		}
		id := rows[w.cursor].ID
		return w, func() tea.Msg { return agentsHideMsg{sessionID: id} }
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
			return w, w.highlightCmd()
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

// highlightCmd emits the current cursor session id for the visualizer.
func (w agentsWindow) highlightCmd() tea.Cmd {
	if len(w.nodes) == 0 {
		w.nodes = w.buildNodes()
	}
	id := w.selectedID(ui.FlattenTree(w.nodes))
	return func() tea.Msg { return agentsHighlightMsg{sessionID: id} }
}

func (w agentsWindow) handleFilterEditKey(msg tea.KeyPressMsg) (agentsWindow, tea.Cmd) {
	switch msg.String() {
	case "esc":
		w.filterEdit = false
		w.textFilter = ""
		w.nodes = w.buildNodes()
		w.cursor = clampAgentsCursor(w.cursor, len(ui.FlattenTree(w.nodes)))
	case "enter":
		w.filterEdit = false
	case "backspace":
		if w.textFilter != "" {
			runes := []rune(w.textFilter)
			w.textFilter = string(runes[:len(runes)-1])
			w.nodes = w.buildNodes()
			w.cursor = clampAgentsCursor(w.cursor, len(ui.FlattenTree(w.nodes)))
		}
	case "ctrl+u":
		w.textFilter = ""
		w.nodes = w.buildNodes()
		w.cursor = clampAgentsCursor(w.cursor, len(ui.FlattenTree(w.nodes)))
	default:
		if len(msg.Text) > 0 {
			w.textFilter += msg.Text
			w.nodes = w.buildNodes()
			w.cursor = clampAgentsCursor(w.cursor, len(ui.FlattenTree(w.nodes)))
		}
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
	q := strings.ToLower(strings.TrimSpace(w.textFilter))
	out := make([]ui.TreeNode, 0, len(w.roots))
	rootNum := 0
	for _, root := range w.roots {
		id := strings.TrimSpace(root.ID)
		if id == "" {
			rootNum++
			continue
		}
		label := strings.TrimSpace(root.Title)
		if label == "" {
			label = shortSessionID(id)
		}
		if label == "" {
			label = "session"
		}
		// Prefix with 1)–9) for ctrl+s modal numbering; no keybinding, visual label only.
		if rootNum < 9 {
			label = itoa(rootNum+1) + ") " + label
		}
		rootNum++
		detail := agentsRootDetail(root.State)
		if q := strings.TrimSpace(root.QueueLabel); q != "" {
			detail = queueDetailLabel(q)
		} else if len(root.Children) == 0 {
			// keep state detail
		}
		node := ui.TreeNode{
			ID:      id,
			Label:   label,
			Current: w.viewingID == id || (w.viewingID == "" && w.activeID == id),
			Tone:    agentStateTone(root.State),
			Detail:  detail,
		}
		if w.viewFilter == agentsFilterRoots {
			// Structure filter: parents only; still honor text filter.
			if !agentsTextMatches(q, id, label) {
				continue
			}
			node.Leaf = true
			out = append(out, node)
			continue
		}

		kids := listableChildActivities(root.Children)
		var childNodes []ui.TreeNode
		if len(kids) > 0 {
			childNodes = w.filterChildTree(kids, q)
		}
		includeRoot := false
		switch {
		case w.viewFilter == agentsFilterAll:
			includeRoot = q == "" || agentsTextMatches(q, id, label) || len(childNodes) > 0
			if q != "" && !agentsTextMatches(q, id, label) {
				// Keep root only as container for matching descendants.
				if len(childNodes) == 0 {
					includeRoot = false
				}
			}
		default:
			// State filters: root if it matches, or it has matching descendants.
			stateOK := agentsStateMatches(root.State, w.viewFilter)
			textOK := q == "" || agentsTextMatches(q, id, label)
			includeRoot = (stateOK && textOK) || len(childNodes) > 0
			if stateOK && !textOK && len(childNodes) == 0 {
				includeRoot = false
			}
		}
		if !includeRoot {
			continue
		}
		if len(childNodes) == 0 {
			node.Leaf = true
		} else {
			node.Expanded = !w.collapsed[id]
			node.Children = childNodes
		}
		out = append(out, node)
	}
	return out
}

func (w agentsWindow) filterChildTree(kids []childActivity, q string) []ui.TreeNode {
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
	var build func(ch childActivity) (ui.TreeNode, bool)
	build = func(ch childActivity) (ui.TreeNode, bool) {
		label := childViewTitle(ch.agent, ch.prompt, ch.sessionID, ch.title, ch.name)
		if label == "" {
			label = shortSessionID(ch.sessionID)
		}
		state := childAgentState(ch.status)
		if ch.rosterState == "needs you" {
			state = theme.AgentStateAttention
		}
		if len(ch.queuePools) > 0 {
			// Queued work is live — never treat as ready/idle.
			state = theme.AgentStateWorking
		}
		detail := ch.status
		if ch.rosterState != "" {
			detail = ch.rosterState
		}
		if q := childQueueDetail(ch); q != "" {
			detail = q
		}
		node := ui.TreeNode{
			ID:      ch.sessionID,
			Label:   label,
			Detail:  detail,
			Current: w.viewingID == ch.sessionID,
			Tone:    childStatusTone(detail),
		}
		var children []ui.TreeNode
		for _, g := range byParent[ch.sessionID] {
			if gn, ok := build(g); ok {
				children = append(children, gn)
			}
		}
		selfOK := agentsStateMatches(state, w.viewFilter) && agentsTextMatches(q, ch.sessionID, label, ch.agent, ch.prompt, ch.title, ch.name)
		if !selfOK && len(children) == 0 {
			return ui.TreeNode{}, false
		}
		if len(children) == 0 {
			node.Leaf = true
		} else {
			node.Expanded = !w.collapsed[ch.sessionID]
			node.Children = children
		}
		return node, true
	}
	out := make([]ui.TreeNode, 0, len(tops))
	for _, ch := range tops {
		if n, ok := build(ch); ok {
			out = append(out, n)
		}
	}
	return out
}

func agentsStateMatches(state theme.AgentState, filter agentsViewFilter) bool {
	switch filter {
	case agentsFilterAttention:
		return state == theme.AgentStateAttention
	case agentsFilterWorking:
		return state == theme.AgentStateWorking
	case agentsFilterReady:
		return state == theme.AgentStateReady
	default:
		// all, roots: state not used as exclusion
		return true
	}
}

func agentsTextMatches(q string, parts ...string) bool {
	if q == "" {
		return true
	}
	for _, p := range parts {
		if strings.Contains(strings.ToLower(p), q) {
			return true
		}
	}
	return false
}

func childAgentState(status string) theme.AgentState {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "running", "working", "starting":
		return theme.AgentStateWorking
	case "needs you", "needs_attention":
		return theme.AgentStateAttention
	case "failed", "error":
		return theme.AgentStateError
	case "canceled", "cancelled":
		return theme.AgentStateDead
	default:
		// completed / unknown → idle green (ready)
		return theme.AgentStateReady
	}
}

// childQueueDetail is the agents-tree chip while waiting on a pool.
func childQueueDetail(ch childActivity) string {
	if label := strings.TrimSpace(ch.queueLabel); label != "" {
		return queueDetailLabel(label)
	}
	if len(ch.queuePools) == 0 {
		return ""
	}
	return queueDetailLabel(strings.Join(ch.queuePools, ","))
}

// queueDetailLabel formats a short "queued: <pool>" chip without promising position.
// Uses ASCII colon (not Icons.DetailSeparator) so plain status strings stay
// theme-free for activity/task_status projection.
func queueDetailLabel(label string) string {
	label = strings.TrimSpace(label)
	if label == "" {
		return "queued"
	}
	return "queued: " + label
}

func (w agentsWindow) countState(want theme.AgentState) int {
	n := 0
	for _, root := range w.roots {
		if strings.TrimSpace(root.ID) == "" {
			continue
		}
		if root.State == want {
			n++
		}
		for _, ch := range listableChildActivities(root.Children) {
			if childAgentState(ch.status) == want {
				n++
			}
		}
	}
	return n
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

func childStatusTone(status string) ui.Tone {
	s := strings.ToLower(strings.TrimSpace(status))
	switch {
	case strings.HasPrefix(s, "queued"):
		return ui.ToneAccentAlt
	case s == "running" || s == "working" || s == "starting":
		return ui.ToneAccentAlt
	case s == "needs you" || s == "needs_attention":
		return ui.ToneWarning
	case s == "completed" || s == "done":
		return ui.ToneSuccess
	case s == "failed" || s == "error":
		return ui.ToneError
	case s == "canceled" || s == "cancelled":
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
		return "needs you"
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

	label := sanitizeDisplayData(childViewTitle(ch.agent, ch.prompt, ch.sessionID, ch.title, ch.name))
	if label == "" {
		label = "subagent"
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
