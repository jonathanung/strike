package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jonathanung/strike-cli/internal/frontend/host"
	"github.com/jonathanung/strike-cli/internal/frontend/tui/theme"
	"github.com/jonathanung/strike-cli/internal/frontend/tui/ui"
)

// rebindAppliedMsg is sent when a single keybind override is applied in-session.
// Chords is nil when resetting to default.
type rebindAppliedMsg struct {
	ID     string
	Chords []string
}

// keybindsSaveMsg triggers persisting all pending overrides.
type keybindsSaveMsg struct {
	Overrides map[string][]string
}

// keybindsSavedMsg reports the outcome of a save.
type keybindsSavedMsg struct {
	err error
}

const keybindEditorVisible = 18

// editorTabOrder defines the category tabs shown in the keybind editor and their
// display order. Only these categories appear in the tab bar.
var editorTabOrder = []string{"Navigation", "Global", "Editor", "Composer", "Completion"}

type conflictPending struct {
	Chord      string
	CaptureID  string // the entry being rebound
	ConflictID string // existing entry that uses this chord
	Keys       string // current keys of the conflicted entry (for display)
}

// keybindEditor is a modal for interactively rebinding keys and saving overrides
// to ~/.strike/keybinds.jsonc. Bindings are filtered by the active category tab.
type keybindEditor struct {
	entries   []keybindEntry
	filtered  []keybindEntry
	cursor    int
	filter    string
	pending   map[string][]string
	saved     map[string][]string
	capturing bool
	captureID string
	effective keyMap
	settings  host.Settings
	tab       int // index into editorTabOrder; -1 means "all"

	confirm        *conflictPending // non-nil when user must confirm a conflict
	unsavedPrompt  bool             // user tried to close with unsaved changes
	closeAfterSave bool             // close modal once save completes
	dirty          bool             // user made at least one modification
}

func newKeybindEditor(effective keyMap, persistedOverrides map[string][]string, settings host.Settings) *keybindEditor {
	m := &keybindEditor{
		entries:   keybindCatalog(effective),
		effective: effective,
		settings:  settings,
		tab:       -1, // start showing all categories
	}
	if len(persistedOverrides) > 0 {
		m.saved = make(map[string][]string, len(persistedOverrides))
		for id, chords := range persistedOverrides {
			if chords != nil {
				m.saved[id] = append([]string(nil), chords...)
			}
		}
	}
	if m.saved == nil {
		m.saved = make(map[string][]string)
	}
	m.pending = make(map[string][]string)
	for id, chords := range m.saved {
		m.pending[id] = append([]string(nil), chords...)
	}
	m.refilter()
	return m
}

func (m *keybindEditor) refilter() {
	q := strings.ToLower(strings.TrimSpace(m.filter))
	// Start from all entries.
	pool := m.entries
	if q == "" {
		m.filtered = filterByCategory(pool, m.tab)
		if m.cursor >= len(m.filtered) {
			m.cursor = max(0, len(m.filtered)-1)
		}
		return
	}
	// With a filter query, search all entries (ignore tab).
	out := make([]keybindEntry, 0, len(pool))
	for _, e := range pool {
		fields := []string{e.Action, e.Category, e.ID, e.Keys}
		matched := false
		for _, f := range fields {
			if strings.Contains(strings.ToLower(f), q) {
				matched = true
				break
			}
		}
		if matched {
			out = append(out, e)
		}
	}
	m.filtered = out
	if m.cursor >= len(m.filtered) {
		m.cursor = max(0, len(m.filtered)-1)
	}
}

// filterByCategory returns only entries whose Category matches the tab at the
// given index, or all entries when tab < 0.
func filterByCategory(entries []keybindEntry, tab int) []keybindEntry {
	if tab < 0 || tab >= len(editorTabOrder) {
		return entries
	}
	cat := editorTabOrder[tab]
	out := make([]keybindEntry, 0, len(entries))
	for _, e := range entries {
		if e.Category == cat {
			out = append(out, e)
		}
	}
	return out
}

func (m *keybindEditor) update(msg tea.KeyPressMsg) (modal, tea.Cmd) {
	if m.capturing {
		return m.updateCapture(msg)
	}
	if m.confirm != nil {
		return m.updateConfirm(msg)
	}
	if m.unsavedPrompt {
		return m.updateUnsavedPrompt(msg)
	}
	if (isEscape(msg) || msg.String() == "q") && m.hasUnsaved() {
		m.unsavedPrompt = true
		return m, nil
	}
	if isEscape(msg) || msg.String() == "q" {
		return nil, nil
	}
	switch msg.String() {
	case "up", "k", "ctrl+p":
		if m.cursor > 0 {
			m.cursor--
		}
		return m, nil
	case "down", "j", "ctrl+n", "tab":
		if m.cursor < len(m.filtered)-1 {
			m.cursor++
		}
		return m, nil
	case "left", "ctrl+h":
		if m.tab < 0 {
			m.tab = len(editorTabOrder) - 1
		} else if m.tab == 0 {
			m.tab = -1
		} else {
			m.tab--
		}
		m.cursor = 0
		m.refilter()
		return m, nil
	case "right", "ctrl+l":
		if m.tab < 0 {
			m.tab = 0
		} else if m.tab >= len(editorTabOrder)-1 {
			m.tab = -1
		} else {
			m.tab++
		}
		m.cursor = 0
		m.refilter()
		return m, nil
	case "backspace":
		if m.filter != "" {
			m.filter = m.filter[:len(m.filter)-1]
			m.cursor = 0
			m.refilter()
		}
		return m, nil
	case "enter":
		return m.startCapture()
	case "ctrl+d":
		return m.resetOverride()
	case "alt+s":
		return m.savePending()
	default:
		if len(msg.Text) > 0 {
			m.filter += msg.Text
			m.cursor = 0
			m.refilter()
		}
		return m, nil
	}
}

func (m *keybindEditor) updateConfirm(msg tea.KeyPressMsg) (modal, tea.Cmd) {
	switch msg.String() {
	case "y", "Y":
		c := m.confirm
		chords := []string{c.Chord}
		m.pending[c.CaptureID] = chords
		m.dirty = true
		id := c.CaptureID
		m.confirm = nil
		return m, func() tea.Msg {
			return rebindAppliedMsg{ID: id, Chords: chords}
		}
	case "n", "N", "esc":
		m.confirm = nil
		return m, nil
	default:
		return m, nil
	}
}

func (m *keybindEditor) hasUnsaved() bool {
	return m.dirty
}

func (m *keybindEditor) updateUnsavedPrompt(msg tea.KeyPressMsg) (modal, tea.Cmd) {
	switch msg.String() {
	case "y", "Y":
		if !m.dirty {
			m.unsavedPrompt = false
			return nil, nil
		}
		m.closeAfterSave = true
		m.unsavedPrompt = false
		return m.savePending()
	case "n", "N":
		m.unsavedPrompt = false
		return nil, nil
	case "esc":
		m.unsavedPrompt = false
		return m, nil
	default:
		return m, nil
	}
}

func (m *keybindEditor) startCapture() (modal, tea.Cmd) {
	if m.cursor < 0 || m.cursor >= len(m.filtered) {
		return m, nil
	}
	m.capturing = true
	m.captureID = m.filtered[m.cursor].ID
	return m, nil
}

func (m *keybindEditor) updateCapture(msg tea.KeyPressMsg) (modal, tea.Cmd) {
	if isEscape(msg) {
		m.capturing = false
		m.captureID = ""
		return m, nil
	}
	chord := msg.String()
	if chord == "" {
		m.capturing = false
		m.captureID = ""
		return m, nil
	}
	// Check if this chord is already bound to a different entry.
	if conflict, conflictKeys := m.conflictingBind(chord); conflict != nil {
		m.capturing = false
		m.confirm = &conflictPending{
			Chord:      chord,
			CaptureID:  m.captureID,
			ConflictID: conflict.ID,
			Keys:       conflictKeys,
		}
		return m, nil
	}
	chords := []string{chord}
	m.pending[m.captureID] = chords
	m.dirty = true
	id := m.captureID
	m.capturing = false
	m.captureID = ""
	return m, func() tea.Msg {
		return rebindAppliedMsg{ID: id, Chords: chords}
	}
}

// conflictingBind returns the keybindEntry that already has the given chord as
// its active keys, excluding the entry being captured and any entry already
// overridden to a different chord. Nil means no conflict. activeKeys is the
// effective chord string displayed in the UI for the conflicting entry.
func (m *keybindEditor) conflictingBind(chord string) (conflict *keybindEntry, activeKeys string) {
	for _, e := range m.entries {
		if e.ID == m.captureID {
			continue
		}
		// Effective keys: pending override, then saved override, then default.
		effective := e.Keys
		if chords, ok := m.pending[e.ID]; ok && len(chords) > 0 {
			effective = joinChords(chords)
		} else if chords, ok := m.saved[e.ID]; ok && len(chords) > 0 {
			effective = joinChords(chords)
		}
		// activeKeys may be "ctrl+t/ctrl+down" — check each segment.
		for _, k := range strings.Split(effective, "/") {
			k = strings.TrimSpace(k)
			if k == chord {
				return &e, effective
			}
		}
	}
	return nil, ""
}

// joinChords joins multiple chord strings with "/".
func joinChords(chords []string) string {
	return strings.Join(chords, "/")
}

func (m *keybindEditor) resetOverride() (modal, tea.Cmd) {
	if m.cursor < 0 || m.cursor >= len(m.filtered) {
		return m, nil
	}
	id := m.filtered[m.cursor].ID
	delete(m.pending, id)
	delete(m.saved, id)
	m.dirty = true
	return m, func() tea.Msg {
		return rebindAppliedMsg{ID: id, Chords: nil}
	}
}

func (m *keybindEditor) saveComplete() {
	m.closeAfterSave = false
	m.dirty = false
	m.pending = make(map[string][]string)
}

func (m *keybindEditor) savePending() (modal, tea.Cmd) {
	if len(m.pending) == 0 {
		return m, nil
	}
	overrides := make(map[string][]string, len(m.pending))
	for id, chords := range m.pending {
		overrides[id] = append([]string(nil), chords...)
	}
	return m, saveKeybindsThroughCmd(m.settings, overrides)
}

// renderTabBar draws the category tab row, fitting within maxCells. tab < 0
// means "all" (default view). Excess categories are omitted from the right.
func renderTabBar(th theme.Theme, tab int, maxCells int) string {
	th = th.Resolve()
	sel := th.S().Accent
	unsel := th.S().Muted

	// Build "All" tab.
	var all string
	if tab < 0 {
		all = sel.Render("All")
	} else {
		all = unsel.Render("All")
	}

	// Build category tabs and measure total width.
	type ct struct {
		label string
		width int
	}
	var tabs []ct
	total := lipgloss.Width(all) + 1 // +1 for separator space
	for i, cat := range editorTabOrder {
		var label string
		if i == tab {
			label = sel.Render(cat)
		} else {
			label = unsel.Render(cat)
		}
		w := lipgloss.Width(label)
		tabs = append(tabs, ct{label: label, width: w})
		total += w + 1
	}

	// Drop from the right until total fits.
	for total > maxCells && len(tabs) > 1 {
		last := tabs[len(tabs)-1]
		total -= last.width + 1
		tabs = tabs[:len(tabs)-1]
	}

	// Join what fits.
	var parts []string
	parts = append(parts, all)
	for _, t := range tabs {
		parts = append(parts, t.label)
	}
	return strings.Join(parts, " ")
}

func (m *keybindEditor) view(width int, th theme.Theme) string {
	th = th.Resolve()
	s := th.S()
	list := m.filtered
	if m.cursor >= len(list) {
		m.cursor = max(0, len(list)-1)
	}
	inner := max(1, ui.PanelInnerWidth(th, width))
	if width < 4 {
		inner = max(1, width)
	}

	// Three columns: action | default | override.
	gap := 2
	defCol := 14 // wide enough for "ctrl+shift+down"
	ovrCol := 16 // wide enough for "ctrl+b / ctrl+down"
	actCol := inner - defCol - ovrCol - gap*2
	if actCol < 10 {
		// Narrow width: give action the lion's share, shrink override first.
		ovrCol = max(4, inner/5)
		defCol = max(4, inner/5)
		actCol = inner - defCol - ovrCol - gap*2
		if actCol < 4 {
			actCol = max(4, inner/3)
		}
	}

	var buf strings.Builder

	// Filter bar with count.
	filterLine := ""
	if m.filter != "" {
		filterLine = th.S().Muted.Render("filter: " + sanitizeDisplayData(m.filter))
	} else {
		filterLine = " " // keep vertical alignment
	}
	showing := len(list)
	total := len(m.entries)
	countStr := th.S().Muted.Render(itoa(showing) + "/" + itoa(total))
	buf.WriteString(filterLine + strings.Repeat(" ", max(1, inner-lipgloss.Width(filterLine)-lipgloss.Width(countStr))) + countStr + "\n")

	// Header row.
	col1 := s.Muted.Render(padRight("Action", actCol))
	col2 := s.Muted.Render(padRight("Default", defCol))
	col3 := s.Muted.Render(padRight("Override", ovrCol))
	buf.WriteString(col1 + "  " + col2 + "  " + col3 + "\n")

	// Separator line.
	// Data rows (visible window around cursor).
	half := keybindEditorVisible / 2
	start := max(0, m.cursor-half)
	end := min(len(list), start+keybindEditorVisible)
	if end-start < keybindEditorVisible && start > 0 {
		start = max(0, end-keybindEditorVisible)
	}
	for i := start; i < end; i++ {
		entry := list[i]
		selected := i == m.cursor

		// Action label.
		label := sanitizeDisplayData(entry.Action)
		label = padRight(label, actCol)

		// Default keys.
		defaultStr := entry.Keys

		// Override keys.
		overrideChords, hasOverride := m.pending[entry.ID]
		if !hasOverride {
			overrideChords, hasOverride = m.saved[entry.ID]
		}
		overrideStr := ""
		if hasOverride && len(overrideChords) > 0 {
			overrideStr = th.Icons.Bolt + " " + strings.Join(overrideChords, "/")
		}
		defaultStr = padRight(defaultStr, defCol)
		overrideStr = padRight(overrideStr, ovrCol)

		row := label + "  " + defaultStr + "  " + overrideStr
		if selected {
			row = s.Accent.Render(row)
		}
		buf.WriteString(row + "\n")
	}

	// Empty state.
	if len(list) == 0 {
		empty := th.S().Muted.Render("  no matching keybinds")
		buf.WriteString(empty + "\n")
	}

	// Scroll indicator.
	if end < len(list) {
		buf.WriteString(s.Muted.Render("  ↓ "+itoa(len(list)-end)+" more") + "\n")
	} else if start > 0 {
		buf.WriteString(s.Muted.Render("  ↑ "+itoa(start)+" hidden") + "\n")
	}

	body := buf.String()
	if width < 4 {
		return body
	}

	tabBar := renderTabBar(th, m.tab, inner)

	title := "Keyboard shortcuts"
	if m.capturing {
		title = "Press new key for " + m.captureID + th.Icons.Ellipsis
	} else if m.confirm != nil {
		title = "⚠  Key conflict"
	} else if m.unsavedPrompt {
		title = "⚠  Unsaved changes"
	}
	hint := "left/right switch tab | type to filter | up/down/j/k move | enter rebind | ctrl+d reset | alt+s save | esc close"
	if m.capturing {
		hint = "press any key to bind | esc cancel"
	} else if m.confirm != nil {
		hint = "y rebind anyway | n cancel"
	} else if m.unsavedPrompt {
		hint = "y save and close | n discard | esc back"
	}
	content := tabBar + "\n\n" + body
	if m.confirm != nil {
		label := m.confirm.ConflictID
		for _, e := range m.entries {
			if e.ID == m.confirm.ConflictID {
				label = e.Action + " (" + m.confirm.Keys + ")"
				break
			}
		}
		warn := m.confirm.Chord + " is used by \"" + label + "\". Rebinding will unassign it."
		for _, wl := range strings.Split(ui.WrapText(warn, inner), "\n") {
			content += "\n" + s.Warning.Render(wl)
		}
	} else if m.unsavedPrompt {
		warn := "You have unsaved keybind changes. Save before closing?"
		for _, wl := range strings.Split(ui.WrapText(warn, inner), "\n") {
			content += "\n" + s.Warning.Render(wl)
		}
	}
	if hint != "" {
		hintLines := strings.Split(ui.WrapText(hint, inner), "\n")
		if len(hintLines) > 2 {
			hintLines = hintLines[:2]
			hintLines[1] = s.Muted.Render(hintLines[1])
		}
		for _, hl := range hintLines {
			content += "\n" + s.Muted.Render(hl)
		}
	}
	return ui.Panel(th, ui.PanelOpts{
		Title:   title,
		Width:   width,
		Focused: true,
	}, content)
}

// padRight pads s with spaces on the right to the given width (measured in
// cell columns). If s is already wider, it is truncated.
func padRight(s string, width int) string {
	w := lipgloss.Width(s)
	if w >= width {
		return s
	}
	return s + strings.Repeat(" ", width-w)
}

func saveKeybindsThroughCmd(settings host.Settings, overrides map[string][]string) tea.Cmd {
	return func() tea.Msg {
		if settings == nil {
			return keybindsSavedMsg{err: errNoSettings}
		}
		return keybindsSavedMsg{err: settings.SaveKeybinds(overrides)}
	}
}
