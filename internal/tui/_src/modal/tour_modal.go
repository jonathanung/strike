package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

// tourSectionID identifies a feature-tour topic. Order is the default walk.
type tourSectionID int

const (
	tourSectionPanes tourSectionID = iota
	tourSectionAgents
	tourSectionPermissions
	tourSectionAutonomy
	tourSectionKeys
	tourSectionCommands
	tourSectionIDCount
)

// tourSection is one skippable, revisitable topic in the feature tour.
type tourSection struct {
	id    tourSectionID
	title string
}

// tourContext carries live TUI surface facts so copy matches keybinds and
// omits guidance for windows that are not registered/cycleable.
type tourContext struct {
	keys       keyMap
	agentsKeys agentsKeyMap
	focus      paneFocus
	activeWin  string
	windowIDs  []string
	hasAgents  bool
	// canSplit is true when the terminal is wide enough for a dual-pane layout.
	canSplit bool
	// singlePane is true when the current geometry shows only one pane column.
	singlePane bool
	orient     splitOrientation
	permMode   string
	autonomy   string
}

// tourModal is a read-only, skippable walkthrough of pane navigation, agents,
// permissions, autonomy, key help, and command discovery. It never writes
// settings, never opens mutating child pickers, and never arms timers.
type tourModal struct {
	sections []tourSection
	cursor   int
	// skipped marks sections the user skipped this visit (still revisitable).
	skipped map[tourSectionID]bool
	// seen marks sections the user advanced past or opened.
	seen map[tourSectionID]bool
	ctx  tourContext
	// restoreFocus is the pane focus to reinstate when the tour closes without
	// a parked parent modal taking over (direct close path).
	restoreFocus paneFocus
	// restoreFocusSet is true when restoreFocus was captured on open.
	restoreFocusSet bool
	flash           string
}

// tourClosedMsg is emitted when the tour closes so the app can restore focus
// without mutating other application state.
type tourClosedMsg struct {
	focus    paneFocus
	focusSet bool
	// completed is true when the user finished the tour (not esc cancel).
	completed bool
}

func newTourModal(ctx tourContext, restoreFocus paneFocus, restoreFocusSet bool) *tourModal {
	m := &tourModal{
		sections:        buildTourSections(ctx),
		skipped:         map[tourSectionID]bool{},
		seen:            map[tourSectionID]bool{},
		ctx:             ctx,
		restoreFocus:    restoreFocus,
		restoreFocusSet: restoreFocusSet,
	}
	if len(m.sections) > 0 {
		m.seen[m.sections[0].id] = true
	}
	return m
}

// buildTourSections returns available sections for the current surface.
// Unavailable surfaces (e.g. no agents window) are omitted entirely.
func buildTourSections(ctx tourContext) []tourSection {
	all := []tourSection{
		{id: tourSectionPanes, title: "Pane navigation"},
		{id: tourSectionAgents, title: "Agents and subagents"},
		{id: tourSectionPermissions, title: "Permissions"},
		{id: tourSectionAutonomy, title: "Autonomy"},
		{id: tourSectionKeys, title: "Key help"},
		{id: tourSectionCommands, title: "Command discovery"},
	}
	out := make([]tourSection, 0, len(all))
	for _, s := range all {
		if tourSectionAvailable(s.id, ctx) {
			out = append(out, s)
		}
	}
	return out
}

func tourSectionAvailable(id tourSectionID, ctx tourContext) bool {
	switch id {
	case tourSectionAgents:
		return ctx.hasAgents
	default:
		return true
	}
}

func (m *tourModal) clampCursor() {
	if len(m.sections) == 0 {
		m.cursor = 0
		return
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= len(m.sections) {
		m.cursor = len(m.sections) - 1
	}
}

func (m *tourModal) current() (tourSection, bool) {
	m.clampCursor()
	if len(m.sections) == 0 {
		return tourSection{}, false
	}
	return m.sections[m.cursor], true
}

func (m *tourModal) markSeen(id tourSectionID) {
	if m.seen == nil {
		m.seen = map[tourSectionID]bool{}
	}
	m.seen[id] = true
}

func (m *tourModal) update(msg tea.KeyPressMsg) (modal, tea.Cmd) {
	if isEscape(msg) || msg.String() == "q" {
		return nil, m.closeCmd(false)
	}
	if len(m.sections) == 0 {
		// Nothing to show — finish immediately.
		if msg.String() == "enter" || msg.String() == "f" {
			return nil, m.closeCmd(true)
		}
		return m, nil
	}
	m.clampCursor()
	switch msg.String() {
	case "up", "k", "ctrl+p", "left", "h", "p":
		if m.cursor > 0 {
			m.cursor--
			m.markSeen(m.sections[m.cursor].id)
		}
		m.flash = ""
		return m, nil
	case "down", "j", "ctrl+n", "right", "l", "n", "tab":
		return m.advance()
	case "s":
		return m.skipCurrent()
	case "f":
		return nil, m.closeCmd(true)
	case "enter":
		// Enter advances; on the last section it finishes.
		if m.cursor >= len(m.sections)-1 {
			m.markSeen(m.sections[m.cursor].id)
			return nil, m.closeCmd(true)
		}
		return m.advance()
	case "home", "g":
		m.cursor = 0
		m.markSeen(m.sections[0].id)
		m.flash = ""
		return m, nil
	case "G", "end":
		m.cursor = len(m.sections) - 1
		m.markSeen(m.sections[m.cursor].id)
		m.flash = ""
		return m, nil
	default:
		// Number keys 1..n jump to a section for revisit.
		if len(msg.Text) == 1 {
			ch := msg.Text[0]
			if ch >= '1' && ch <= '9' {
				idx := int(ch - '1')
				if idx < len(m.sections) {
					m.cursor = idx
					m.markSeen(m.sections[idx].id)
					m.flash = ""
				}
			}
		}
		return m, nil
	}
}

func (m *tourModal) advance() (modal, tea.Cmd) {
	m.clampCursor()
	if len(m.sections) == 0 {
		return nil, m.closeCmd(true)
	}
	m.markSeen(m.sections[m.cursor].id)
	if m.cursor < len(m.sections)-1 {
		m.cursor++
		m.markSeen(m.sections[m.cursor].id)
		m.flash = ""
		return m, nil
	}
	return nil, m.closeCmd(true)
}

func (m *tourModal) skipCurrent() (modal, tea.Cmd) {
	m.clampCursor()
	if len(m.sections) == 0 {
		return nil, m.closeCmd(true)
	}
	id := m.sections[m.cursor].id
	if m.skipped == nil {
		m.skipped = map[tourSectionID]bool{}
	}
	m.skipped[id] = true
	m.markSeen(id)
	m.flash = "section skipped — revisit anytime"
	if m.cursor < len(m.sections)-1 {
		m.cursor++
		m.markSeen(m.sections[m.cursor].id)
		return m, nil
	}
	// Last section skipped → finish tour.
	return nil, m.closeCmd(true)
}

func (m *tourModal) closeCmd(completed bool) tea.Cmd {
	focus, set := m.restoreFocus, m.restoreFocusSet
	return func() tea.Msg {
		return tourClosedMsg{focus: focus, focusSet: set, completed: completed}
	}
}

func (m *tourModal) view(width int, th theme.Theme) string {
	th = th.Resolve()
	st := th.S()
	inner := max(1, ui.PanelInnerWidth(th, width))
	m.clampCursor()

	intro := st.Muted.Render("Feature tour — guidance only; nothing here changes settings or session state.")
	var body strings.Builder
	body.WriteString(wrapToWidth(intro, inner))
	body.WriteString(strings.Repeat("\n", max(1, th.Spacing.SM)))

	if len(m.sections) == 0 {
		body.WriteString(wrapToWidth(st.Muted.Render("No tour sections available on this surface."), inner))
	} else {
		// Section list (advance / skip / revisit).
		items := make([]ui.ListItem, 0, len(m.sections))
		for i, sec := range m.sections {
			mark := th.Icons.Info
			switch {
			case m.skipped[sec.id]:
				mark = th.Icons.Ellipsis
			case m.seen[sec.id] && i != m.cursor:
				mark = th.Icons.OK
			}
			detail := fmt.Sprintf("%d/%d", i+1, len(m.sections))
			if m.skipped[sec.id] {
				detail = detailJoin(th, detail, "skipped")
			}
			items = append(items, ui.ListItem{
				Label:  mark + themedSpace(th.Spacing.Label) + sec.title,
				Detail: detail,
			})
		}
		body.WriteString(ui.List(th, ui.ListOpts{
			Items:   items,
			Cursor:  m.cursor,
			Width:   inner,
			Visible: min(len(m.sections), 6),
			Empty:   "no sections",
		}))
		body.WriteString(strings.Repeat("\n", max(1, th.Spacing.SM)))

		// Detail for the focused section — live key labels from keyMap.
		if sec, ok := m.current(); ok {
			title := st.Title.Render(sec.title)
			body.WriteString(wrapToWidth(title, inner))
			body.WriteString("\n")
			body.WriteString(wrapToWidth(m.sectionBody(sec.id, th), inner))
		}
	}

	if m.flash != "" {
		body.WriteString(strings.Repeat("\n", max(1, th.Spacing.SM)))
		body.WriteString(wrapToWidth(st.Muted.Render(m.flash), inner))
	}

	hints := []string{"↑/↓ section", "enter next", "s skip", "f finish", "esc cancel"}
	return ui.Dialog(th, ui.DialogOpts{
		Title: "tour",
		Hint:  dotJoin(th, hints...),
		Width: width,
	}, body.String())
}

func (m *tourModal) sectionBody(id tourSectionID, th theme.Theme) string {
	th = th.Resolve()
	st := th.S()
	keys := m.ctx.keys
	switch id {
	case tourSectionPanes:
		return m.panesBody(st, keys, th)
	case tourSectionAgents:
		return m.agentsBody(st, keys, th)
	case tourSectionPermissions:
		return m.permissionsBody(st, keys)
	case tourSectionAutonomy:
		return m.autonomyBody(st)
	case tourSectionKeys:
		return m.keysBody(st, keys)
	case tourSectionCommands:
		return m.commandsBody(st, keys)
	default:
		return ""
	}
}

func bindingKey(b key.Binding) string {
	h := b.Help()
	if k := strings.TrimSpace(h.Key); k != "" {
		return k
	}
	ks := b.Keys()
	if len(ks) == 0 {
		return ""
	}
	return joinChordHelp(ks)
}

func (m *tourModal) panesBody(st theme.Styles, keys keyMap, th theme.Theme) string {
	th = th.Resolve()
	dot := th.Icons.Dot
	var b strings.Builder
	left := bindingKey(keys.FocusLeft)
	right := bindingKey(keys.FocusRight)
	next := bindingKey(keys.CycleWindowNext)
	prev := bindingKey(keys.CycleWindowPrev)
	gNext := bindingKey(keys.CycleGroupNext)
	gPrev := bindingKey(keys.CycleGroupPrev)
	split := bindingKey(keys.ToggleOrientation)

	b.WriteString(st.Text.Render("Left is the transcript and composer; right is the tool stack (context, agents, files, and more)."))
	b.WriteString("\n")
	b.WriteString(st.Muted.Render(fmt.Sprintf("Focus: %s left %s %s right.", left, dot, right)))
	if m.ctx.canSplit && !m.ctx.singlePane {
		orient := "side-by-side"
		if m.ctx.orient == orientVertical {
			orient = "stacked top/bottom"
		}
		b.WriteString("\n")
		b.WriteString(st.Muted.Render(fmt.Sprintf("Split is %s. Toggle with %s.", orient, split)))
	} else if m.ctx.singlePane {
		b.WriteString("\n")
		b.WriteString(st.Muted.Render(fmt.Sprintf("Narrow layout shows one pane at a time - %s / %s still switch columns.", left, right)))
	}
	if len(m.ctx.windowIDs) > 1 {
		b.WriteString("\n")
		b.WriteString(st.Muted.Render(fmt.Sprintf("Cycle panes: %s / %s. Cycle groups: %s / %s.", next, prev, gNext, gPrev)))
		if w := strings.TrimSpace(m.ctx.activeWin); w != "" {
			b.WriteString("\n")
			b.WriteString(st.Muted.Render("Active right pane: " + w + "."))
		}
	} else if len(m.ctx.windowIDs) == 1 {
		b.WriteString("\n")
		b.WriteString(st.Muted.Render("Right stack has a single pane right now."))
	}
	return b.String()
}

func (m *tourModal) agentsBody(st theme.Styles, keys keyMap, th theme.Theme) string {
	th = th.Resolve()
	dot := th.Icons.Dot
	ak := m.ctx.agentsKeys
	var b strings.Builder
	b.WriteString(st.Text.Render("The agents pane lists concurrent roots and nested subagents."))
	b.WriteString("\n")
	parts := []string{
		bindingKey(ak.Move) + " move",
		bindingKey(ak.Open) + " open",
		bindingKey(ak.Spawn) + " new root",
		bindingKey(ak.Interrupt) + " interrupt",
		bindingKey(ak.Rename) + " rename",
		bindingKey(ak.Hide) + " hide",
		bindingKey(ak.Filter) + " filter",
	}
	b.WriteString(st.Muted.Render("In the agents pane: " + strings.Join(parts, " "+dot+" ") + "."))
	b.WriteString("\n")
	leader := bindingKey(keys.Leader)
	b.WriteString(st.Muted.Render(fmt.Sprintf(
		"Subagent transcripts: %s then arrows (enter child / parent / next / prev). Persona cycle: %s.",
		leader, bindingKey(keys.Agent),
	)))
	b.WriteString("\n")
	b.WriteString(st.Muted.Render(fmt.Sprintf("Session switcher: %s.", bindingKey(keys.RootSwitcher))))
	return b.String()
}

func (m *tourModal) permissionsBody(st theme.Styles, keys keyMap) string {
	var b strings.Builder
	b.WriteString(st.Text.Render("Tool calls may ask before running. Choose allow once, session, project, or reject."))
	b.WriteString("\n")
	modeKey := bindingKey(keys.PermissionMode)
	b.WriteString(st.Muted.Render(fmt.Sprintf(
		"Cycle permission posture with %s (or /mode). Postures: default, plan, accept-edits, yolo.",
		modeKey,
	)))
	if mode := strings.TrimSpace(m.ctx.permMode); mode != "" {
		b.WriteString("\n")
		b.WriteString(st.Muted.Render("Current session mode: " + mode + "."))
	}
	b.WriteString("\n")
	b.WriteString(st.Muted.Render("Esc on a permission prompt always rejects — dismissal never silently continues."))
	return b.String()
}

func (m *tourModal) autonomyBody(st theme.Styles) string {
	var b strings.Builder
	b.WriteString(st.Text.Render("Autonomy is the exit-gate policy for when the agent may stop or keep going."))
	b.WriteString("\n")
	b.WriteString(st.Muted.Render("Open /autonomy to pick supervised, agent, or checks. This tour does not change the setting."))
	if a := strings.TrimSpace(m.ctx.autonomy); a != "" {
		b.WriteString("\n")
		b.WriteString(st.Muted.Render("Current autonomy: " + a + "."))
	}
	return b.String()
}

func (m *tourModal) keysBody(st theme.Styles, keys keyMap) string {
	var b strings.Builder
	help := bindingKey(keys.KeyHelp)
	b.WriteString(st.Text.Render("Keybinds are context-sensitive; the footer shows the chords for the focused region."))
	b.WriteString("\n")
	b.WriteString(st.Muted.Render(fmt.Sprintf(
		"Full cheatsheet: %s or /keys (filterable). UI glyphs: /legend. Neither replaces this tour.",
		help,
	)))
	return b.String()
}

func (m *tourModal) commandsBody(st theme.Styles, keys keyMap) string {
	var b strings.Builder
	pal := bindingKey(keys.Palette)
	b.WriteString(st.Text.Render("Slash commands and the command palette cover setup, panes, and tools."))
	b.WriteString("\n")
	b.WriteString(st.Muted.Render(fmt.Sprintf(
		"Open the palette with %s. Type / for slash completion. /help lists commands; /ftue reopens setup and this tour.",
		pal,
	)))
	return b.String()
}

// buildTourContext snapshots the live surface for tour copy. Read-only.
func (m *Model) buildTourContext() tourContext {
	ctx := tourContext{
		keys:       m.keyMap,
		agentsKeys: defaultAgentsKeyMap(),
		focus:      m.focus,
		orient:     m.splitOrientation,
		permMode:   string(m.permMode.Normalize()),
		autonomy:   string(m.autonomy.Normalize()),
	}
	if m.width > 0 {
		// Dual-pane threshold matches layout: below compact width → single column.
		ctx.canSplit = m.width >= 93
		gutter := m.paneGutter()
		geo := computePaneGeometry(m.width, gutter, m.focus)
		ctx.singlePane = geo.mode == paneSingle
	}
	ids := make([]string, 0, len(m.windows.windows))
	for _, w := range m.windows.windows {
		if w == nil || !windowCycleable(w) {
			continue
		}
		id := w.id()
		ids = append(ids, id)
		if id == agentsWindowID {
			ctx.hasAgents = true
		}
	}
	ctx.windowIDs = ids
	if m.focus == focusRight {
		if w := m.windows.active(); w != nil {
			ctx.activeWin = w.id()
		}
	} else if w := m.windows.active(); w != nil {
		ctx.activeWin = w.id()
	}
	return ctx
}

// openTourModal builds and shows the feature tour, capturing focus for restore.
func (m *Model) openTourModal() *tourModal {
	return newTourModal(m.buildTourContext(), m.focus, true)
}

// applyTourClosed restores focus after the tour dismisses and marks the FTUE
// tour step complete when appropriate. Does not write settings.
//
// Modal promotion is handled by the key path (afterModalClosed batched with the
// close cmd). This handler only stamps wizard state and restores pane focus.
func (m *Model) applyTourClosed(msg tourClosedMsg) tea.Cmd {
	// Stamp completion on a visible or parked FTUE wizard. afterModalClosed may
	// already have promoted the wizard before this message runs, so check both.
	if msg.completed {
		mark := func(mod modal) {
			if f, ok := mod.(*ftueModal); ok {
				f.tourDone = true
				f.flash = "feature tour complete"
			}
		}
		mark(m.modal)
		for _, q := range m.modalQueue {
			mark(q)
		}
	}
	if !msg.focusSet {
		return nil
	}
	// Restore the focus captured when the tour opened. Safe under a promoted
	// wizard modal — focus only affects the underlying layout.
	return m.setPaneFocus(msg.focus)
}
