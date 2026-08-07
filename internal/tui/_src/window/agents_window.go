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
// view filter, / text filter, p cycle pet for the focused agent.
// A per-agent ASCII pet sits above the tree for identification.
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
	// pets maps session id → petCatalog index (per-agent companion).
	pets map[string]int
	// petFrame is the shared animation frame for the focused pet art.
	petFrame int
	cursor   int
	width    int
	height   int
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
		w = w.ensurePetsAssigned()
		w.nodes = w.buildNodes(theme.Default())
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
	case petsTickMsg:
		if p, ok := w.focusPet(); ok {
			frames := p.framesFor(w.focusPetState())
			if len(frames) > 0 {
				w.petFrame = (w.petFrame + 1) % len(frames)
			}
		}
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
	w.nodes = w.buildNodes(th)
	rows := ui.FlattenTree(w.nodes)
	empty := agentsEmptyLabel(w.viewFilter, w.textFilter)
	showFilter := w.filterEdit || w.textFilter != ""
	bodyH := visible
	var parts []string
	// Pet companion sits above the agent tree (focused session's pet).
	if petBlock, n := w.renderFocusPet(th); petBlock != "" {
		parts = append(parts, petBlock)
		if bodyH > n {
			bodyH -= n
		} else {
			bodyH = 0
		}
	}
	if showFilter {
		cursor := ""
		if w.filterEdit {
			cursor = ic.FilterCursor
		}
		header := st.Muted.Render("filter: ") + st.Input.Render(sanitizeDisplayData(w.textFilter)+cursor)
		parts = append(parts, wrapWindowText(header, w.width))
		if bodyH > 0 {
			bodyH--
		}
	}
	var body string
	if len(rows) == 0 {
		body = lipgloss.NewStyle().Width(max(1, w.width)).Render(
			st.Muted.Render(empty),
		)
	} else if bodyH > 0 {
		body = ui.Tree(th, ui.TreeOpts{
			Nodes:   w.nodes,
			Cursor:  clampAgentsCursor(w.cursor, len(rows)),
			Width:   w.width,
			Visible: bodyH,
			Empty:   empty,
		})
	}
	// Always surface empty-state copy (even when the pet consumed the height
	// budget). Tree body only when there is remaining vertical room.
	if body != "" {
		parts = append(parts, body)
	}
	return strings.Join(parts, "\n")
}

// renderFocusPet draws the focused agent's ASCII pet above the tree.
// Returns the block and how many rows it consumes (including trailing blank).
// Art and name use AgentState tokens so status is visible at a glance.
func (w agentsWindow) renderFocusPet(th theme.Theme) (string, int) {
	th = th.Resolve()
	p, ok := w.focusPet()
	if !ok {
		return "", 0
	}
	state := w.focusPetState()
	frames := p.framesFor(state)
	if len(frames) == 0 {
		return "", 0
	}
	// Need room for name + at least one art row.
	if w.height < 2 {
		return "", 0
	}
	lines := make([]string, 0, 8)
	stateStyle := th.AgentStateStyle(state)
	nameLabel := sanitizeDisplayData(p.Name)
	// Muted status word next to the pet name when not ready.
	if state != theme.AgentStateReady {
		sep := th.Icons.DetailSeparator
		if strings.TrimSpace(sep) == "" {
			sep = "·"
		}
		nameLabel = nameLabel + " " + sep + " " + state.Label()
	}
	lines = append(lines, petsCenterLine(th, stateStyle.Bold(true).Render(nameLabel), w.width))
	art := frames[w.petFrame%len(frames)]
	artRows := strings.Split(art, "\n")
	// Budget: leave at least 2 rows for the tree when possible.
	budget := w.height - 2
	if budget < 2 {
		budget = w.height
	}
	// name already used 1
	artBudget := budget - 1
	if artBudget < 1 {
		return strings.Join(lines, "\n"), len(lines)
	}
	if len(artRows) > artBudget {
		artRows = artRows[:artBudget]
	}
	for _, row := range artRows {
		lines = append(lines, petsCenterLine(th, stateStyle.Render(row), w.width))
	}
	// Trailing blank separator when space remains for the tree.
	if w.height > len(lines)+1 {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n"), len(lines)
}

// focusPetState is the runtime state of the session whose pet is shown.
func (w agentsWindow) focusPetState() theme.AgentState {
	id := w.focusPetSessionID()
	if id == "" {
		return theme.AgentStateReady
	}
	for _, root := range w.roots {
		if root.ID == id {
			return root.State
		}
		for _, ch := range root.Children {
			if ch.sessionID == id {
				state := childAgentState(ch.status)
				if ch.rosterState == "needs you" {
					state = theme.AgentStateAttention
				}
				if len(ch.queuePools) > 0 {
					state = theme.AgentStateWorking
				}
				return state
			}
		}
	}
	return theme.AgentStateReady
}

// focusPetSessionID is the session whose pet is shown (cursor → viewing → active).
func (w agentsWindow) focusPetSessionID() string {
	if len(w.nodes) == 0 {
		w.nodes = w.buildNodes(theme.Default())
	}
	if id := w.selectedID(ui.FlattenTree(w.nodes)); id != "" {
		return id
	}
	if id := strings.TrimSpace(w.viewingID); id != "" {
		return id
	}
	return strings.TrimSpace(w.activeID)
}

// focusPet returns the catalog pet for the focused session.
func (w agentsWindow) focusPet() (petSpec, bool) {
	id := w.focusPetSessionID()
	if id == "" || w.pets == nil {
		return petSpec{}, false
	}
	idx, ok := w.pets[id]
	if !ok {
		return petSpec{}, false
	}
	return petAt(idx)
}

// ensurePetsAssigned gives every live root/child a pet. Prefers catalog entries
// not already assigned; when the roster is exhausted, picks any random pet.
func (w agentsWindow) ensurePetsAssigned() agentsWindow {
	if len(petCatalog) == 0 {
		return w
	}
	if w.pets == nil {
		w.pets = map[string]int{}
	}
	ids := w.liveAgentIDs()
	live := make(map[string]bool, len(ids))
	for _, id := range ids {
		live[id] = true
	}
	// Drop assignments for sessions no longer in the tree.
	for id := range w.pets {
		if !live[id] {
			delete(w.pets, id)
		}
	}
	used := make(map[int]bool, len(w.pets))
	for id, idx := range w.pets {
		if live[id] {
			used[idx] = true
		}
	}
	for _, id := range ids {
		if _, ok := w.pets[id]; ok {
			continue
		}
		free := make([]int, 0, len(petCatalog))
		for i := range petCatalog {
			if !used[i] {
				free = append(free, i)
			}
		}
		var pick int
		if len(free) == 0 {
			pick = petRandN(len(petCatalog))
		} else {
			pick = free[petRandN(len(free))]
		}
		w.pets[id] = pick
		used[pick] = true
	}
	return w
}

// liveAgentIDs lists root and child session ids currently in the agents tree.
func (w agentsWindow) liveAgentIDs() []string {
	out := make([]string, 0, len(w.roots)*2)
	seen := map[string]bool{}
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		out = append(out, id)
	}
	for _, root := range w.roots {
		add(root.ID)
		for _, ch := range root.Children {
			add(ch.sessionID)
		}
	}
	return out
}

// cycleFocusPet advances the focused agent's pet through the catalog.
func (w agentsWindow) cycleFocusPet(delta int) agentsWindow {
	n := len(petCatalog)
	if n == 0 {
		return w
	}
	id := w.focusPetSessionID()
	if id == "" {
		return w
	}
	if w.pets == nil {
		w.pets = map[string]int{}
	}
	cur, ok := w.pets[id]
	if !ok {
		cur = 0
	}
	w.pets[id] = (cur + delta%n + n) % n
	w.petFrame = 0
	return w
}

// setFocusPetByName assigns a catalog pet to the focused (or given) session.
func (w agentsWindow) setFocusPetByName(name, sessionID string) (agentsWindow, bool) {
	idx, ok := petByID(name)
	if !ok {
		return w, false
	}
	id := strings.TrimSpace(sessionID)
	if id == "" {
		id = w.focusPetSessionID()
	}
	if id == "" {
		id = strings.TrimSpace(w.activeID)
	}
	if id == "" {
		return w, false
	}
	if w.pets == nil {
		w.pets = map[string]int{}
	}
	w.pets[id] = idx
	w.petFrame = 0
	return w, true
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
	hints := make([]ui.KeyHint, 0, 8)
	for _, b := range []key.Binding{ak.Spawn, ak.Open, ak.Interrupt, ak.Rename, ak.Hide, ak.Move, ak.Filter, ak.Pet} {
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
		w.nodes = w.buildNodes(theme.Default())
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
		w.nodes = w.buildNodes(theme.Default())
		rows = ui.FlattenTree(w.nodes)
		w.cursor = clampAgentsCursor(w.cursor, len(rows))
		return w, w.highlightCmd()
	case "p":
		// Cycle companion pet for the focused agent.
		return w.cycleFocusPet(1), nil
	case "P":
		return w.cycleFocusPet(-1), nil
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
			w.nodes = w.buildNodes(theme.Default())
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
		w.nodes = w.buildNodes(theme.Default())
	}
	id := w.selectedID(ui.FlattenTree(w.nodes))
	return func() tea.Msg { return agentsHighlightMsg{sessionID: id} }
}

func (w agentsWindow) handleFilterEditKey(msg tea.KeyPressMsg) (agentsWindow, tea.Cmd) {
	switch msg.String() {
	case "esc":
		w.filterEdit = false
		w.textFilter = ""
		w.nodes = w.buildNodes(theme.Default())
		w.cursor = clampAgentsCursor(w.cursor, len(ui.FlattenTree(w.nodes)))
	case "enter":
		w.filterEdit = false
	case "backspace":
		if w.textFilter != "" {
			runes := []rune(w.textFilter)
			w.textFilter = string(runes[:len(runes)-1])
			w.nodes = w.buildNodes(theme.Default())
			w.cursor = clampAgentsCursor(w.cursor, len(ui.FlattenTree(w.nodes)))
		}
	case "ctrl+u":
		w.textFilter = ""
		w.nodes = w.buildNodes(theme.Default())
		w.cursor = clampAgentsCursor(w.cursor, len(ui.FlattenTree(w.nodes)))
	default:
		if len(msg.Text) > 0 {
			w.textFilter += msg.Text
			w.nodes = w.buildNodes(theme.Default())
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

func (w agentsWindow) buildNodes(th theme.Theme) []ui.TreeNode {
	th = th.Resolve()
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
			childNodes = w.filterChildTree(th, kids, q)
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

func (w agentsWindow) filterChildTree(th theme.Theme, kids []childActivity, q string) []ui.TreeNode {
	th = th.Resolve()
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
			ID:     ch.sessionID,
			Label:  label,
			Detail: detail,
			// Orchestration chips (blocked/conflict/budget/verify) — additive.
			Suffix:  agentsOrchSuffix(th, ch),
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

// agentsMaxOrchChips bounds suffix badges per row so narrow widths stay readable.
// Tree drops the whole suffix when it cannot fit; we also pre-bound count.
const agentsMaxOrchChips = 3

// agentsOrchSuffix builds compact orchestration chips for a child tree row.
// Additive to existing status Detail coloring. Empty when no flags apply.
// Priority (highest first): blocked → conflict → escalated/over-budget →
// claimed/unverified. Verified-success stays quiet (detail pane owns it).
func agentsOrchSuffix(th theme.Theme, ch childActivity) string {
	th = th.Resolve()
	type chip struct {
		label string
		tone  ui.Tone
	}
	var chips []chip

	if agentsChildBlocked(ch) {
		chips = append(chips, chip{"blocked", ui.ToneWarning})
	}
	if len(ch.pathOverlaps) > 0 {
		tone := ui.ToneWarning
		label := "conflict"
		for _, po := range ch.pathOverlaps {
			if po.blocked || strings.EqualFold(po.policy, "block") {
				tone = ui.ToneError
				label = "conflict"
				break
			}
		}
		chips = append(chips, chip{label, tone})
	}
	if label, tone, ok := agentsChildBudgetChip(ch); ok {
		chips = append(chips, chip{label, tone})
	}
	if label, tone, ok := agentsChildVerifyChip(ch); ok {
		chips = append(chips, chip{label, tone})
	}

	if len(chips) == 0 {
		return ""
	}
	if len(chips) > agentsMaxOrchChips {
		chips = chips[:agentsMaxOrchChips]
	}
	parts := make([]string, 0, len(chips))
	for _, c := range chips {
		parts = append(parts, ui.Badge(th, c.tone, c.label))
	}
	return strings.Join(parts, themedSpace(th.Spacing.XS))
}

func agentsChildBlocked(ch childActivity) bool {
	// Explicit block reason always counts (needs-you with a block).
	if strings.TrimSpace(ch.blockReason) != "" {
		return true
	}
	// Wire blocked status only — bare "needs you" already has Detail coloring
	// and is not necessarily a path/permission block.
	status := strings.ToLower(strings.TrimSpace(ch.status))
	roster := strings.ToLower(strings.TrimSpace(ch.rosterState))
	if status == string(protocol.ChildStatusBlocked) || status == "blocked" {
		return true
	}
	if roster == "blocked" {
		return true
	}
	return false
}

func agentsChildBudgetChip(ch childActivity) (label string, tone ui.Tone, ok bool) {
	if k := strings.TrimSpace(ch.escalateKind); k != "" {
		switch {
		case strings.EqualFold(k, "stall"):
			return "stall", ui.ToneWarning, true
		case strings.EqualFold(k, "loop"):
			return "loop", ui.ToneWarning, true
		default:
			return "escalated", ui.ToneWarning, true
		}
	}
	if strings.TrimSpace(ch.escalateReason) != "" || strings.TrimSpace(ch.escalateAction) != "" {
		return "escalated", ui.ToneWarning, true
	}
	b := ch.budget
	if b == nil {
		return "", ui.ToneMuted, false
	}
	if b.Stall {
		return "stall", ui.ToneWarning, true
	}
	if b.Loop {
		return "loop", ui.ToneWarning, true
	}
	if b.Escalated || strings.TrimSpace(b.EscalateKind) != "" {
		return "escalated", ui.ToneWarning, true
	}
	// Over-budget: any limited dimension at/over max.
	if agentsBudgetOver(b) {
		return "over budget", ui.ToneError, true
	}
	return "", ui.ToneMuted, false
}

func agentsBudgetOver(b *protocol.AgentBudgetView) bool {
	if b == nil {
		return false
	}
	if b.MaxToolCalls > 0 && b.ToolCalls >= b.MaxToolCalls {
		return true
	}
	if b.MaxDangerousTools > 0 && b.DangerousTools >= b.MaxDangerousTools {
		return true
	}
	if b.MaxTokens > 0 && b.TokensUsed >= b.MaxTokens {
		return true
	}
	if b.MaxWallClockS > 0 && b.ElapsedS >= b.MaxWallClockS {
		return true
	}
	if b.MaxCostUSD > 0 && b.CostUSDUsed >= b.MaxCostUSD {
		return true
	}
	// Remaining pointers at zero also count.
	if b.ToolCallsRemaining != nil && b.MaxToolCalls > 0 && *b.ToolCallsRemaining <= 0 {
		return true
	}
	if b.TokensRemaining != nil && b.MaxTokens > 0 && *b.TokensRemaining <= 0 {
		return true
	}
	if b.WallClockRemainingS != nil && b.MaxWallClockS > 0 && *b.WallClockRemainingS <= 0 {
		return true
	}
	if b.CostUSDRemaining != nil && b.MaxCostUSD > 0 && *b.CostUSDRemaining <= 0 {
		return true
	}
	return false
}

// agentsChildVerifyChip surfaces claimed/unverified when a report exists.
// Quiet on verified success so the tree stays glanceable for problems.
func agentsChildVerifyChip(ch childActivity) (label string, tone ui.Tone, ok bool) {
	v := ch.verification
	if v == nil {
		return "", ui.ToneMuted, false
	}
	switch {
	case v.verified && v.passed:
		return "", ui.ToneMuted, false
	case v.claimed && !v.verified:
		return "claimed", ui.ToneWarning, true
	case !v.passed:
		return "unverified", ui.ToneError, true
	default:
		return "unverified", ui.ToneError, true
	}
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
	case string(protocol.ChildStatusBlocked):
		return "blocked", ic.Info, st.Warning
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
