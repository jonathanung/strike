package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/jonathanung/strike-cli/internal/frontend/host"
	"github.com/jonathanung/strike-cli/internal/frontend/tui/theme"
	"github.com/jonathanung/strike-cli/internal/frontend/tui/ui"
)

const themeModalVisible = 10

// themeModal is the centered /theme picker: bundled JSON themes, user/project
// dirs, and plugin contributions (docs/plugins.md §7.4). Cursor movement
// previews without persisting; esc always reverts to the theme active when the
// modal opened; enter applies the session theme; ctrl+d saves the default.
type themeModal struct {
	entries  []theme.Entry
	filtered []theme.Entry
	cursor   int
	filter   string
	current  string // session theme id when opened / last applied
	// original is the palette to restore on cancel (esc/q).
	original theme.Entry
	settings host.Settings
	// previewed tracks the last live-preview id to avoid redundant cmds.
	previewed string
}

func newThemeModal(entries []theme.Entry, current string, settings host.Settings) *themeModal {
	m := &themeModal{entries: entries, current: current, settings: settings}
	// Capture original for revert — prefer catalog entry, else bare id.
	if e, ok := theme.Lookup(entries, current); ok {
		m.original = e
	} else {
		m.original = theme.Entry{ID: current, Name: current, Theme: theme.Default(), Source: theme.SourceBuiltin}
	}
	m.refilter()
	for i, e := range m.filtered {
		if e.ID == current {
			m.cursor = i
			break
		}
	}
	m.previewed = current
	return m
}

func (m *themeModal) refilter() {
	q := strings.ToLower(strings.TrimSpace(m.filter))
	if q == "" {
		m.filtered = append([]theme.Entry(nil), m.entries...)
		return
	}
	out := make([]theme.Entry, 0, len(m.entries))
	for _, e := range m.entries {
		if strings.Contains(strings.ToLower(e.ID), q) ||
			strings.Contains(strings.ToLower(e.Name), q) ||
			strings.Contains(strings.ToLower(e.Provenance()), q) ||
			strings.Contains(strings.ToLower(e.PluginID), q) {
			out = append(out, e)
		}
	}
	m.filtered = out
	if m.cursor >= len(m.filtered) {
		m.cursor = max(0, len(m.filtered)-1)
	}
}

func (m *themeModal) selected() (theme.Entry, bool) {
	if m.cursor < 0 || m.cursor >= len(m.filtered) {
		return theme.Entry{}, false
	}
	return m.filtered[m.cursor], true
}

func (m *themeModal) previewCmd() tea.Cmd {
	e, ok := m.selected()
	if !ok || e.ID == m.previewed {
		return nil
	}
	m.previewed = e.ID
	return func() tea.Msg { return themePreviewMsg{entry: e} }
}

func (m *themeModal) update(msg tea.KeyPressMsg) (modal, tea.Cmd) {
	if isEscape(msg) || msg.String() == "q" {
		// Always revert to the theme that was active when the modal opened.
		orig := m.original
		return nil, func() tea.Msg { return themeRevertMsg{entry: orig} }
	}
	switch msg.String() {
	case "up", "k", "ctrl+p":
		if m.cursor > 0 {
			m.cursor--
		}
		return m, m.previewCmd()
	case "down", "j", "ctrl+n", "tab":
		if m.cursor < len(m.filtered)-1 {
			m.cursor++
		}
		return m, m.previewCmd()
	case "backspace":
		if m.filter != "" {
			m.filter = m.filter[:len(m.filter)-1]
			m.cursor = 0
			m.refilter()
			return m, m.previewCmd()
		}
		return m, nil
	case "enter":
		e, ok := m.selected()
		if !ok {
			return m, nil
		}
		// Apply session theme (already previewed); does not persist default.
		return nil, func() tea.Msg { return themeSelectedMsg{entry: e} }
	case "ctrl+d":
		e, ok := m.selected()
		if !ok {
			return m, nil
		}
		// Persist default; keep modal open so user can still cancel preview.
		return m, tea.Batch(
			m.previewCmd(),
			saveThemeThroughCmd(m.settings, e.ID),
		)
	default:
		if len(msg.Text) > 0 {
			m.filter += msg.Text
			m.cursor = 0
			m.refilter()
			return m, m.previewCmd()
		}
		return m, nil
	}
}

func (m *themeModal) view(width int, th theme.Theme) string {
	th = th.Resolve()
	inner := max(1, ui.PanelInnerWidth(th, width))
	items := make([]ui.ListItem, len(m.filtered))
	for i, e := range m.filtered {
		detail := e.Provenance()
		if e.Name != e.ID {
			detail = e.ID + themedSpace(th.Spacing.XS) + th.Icons.Dot + themedSpace(th.Spacing.XS) + e.Provenance()
		}
		if e.Overrode != "" {
			detail = detail + themedSpace(th.Spacing.XS) + th.Icons.Dot + themedSpace(th.Spacing.XS) + "over " + e.Overrode
		}
		items[i] = ui.ListItem{
			Label:   e.Name,
			Detail:  detail,
			Current: e.ID == m.current,
		}
	}
	body := ui.List(th, ui.ListOpts{
		Items:      items,
		Cursor:     m.cursor,
		Width:      inner,
		Visible:    themeModalVisible,
		ShowFilter: true,
		Filter:     m.filter,
		Total:      len(m.entries),
		Empty:      "no themes found",
	})
	return ui.Dialog(th, ui.DialogOpts{
		Title: "Select theme",
		Hint:  dotJoin(th, "↑/↓ preview", "type filter", "enter apply", "ctrl+d default", "esc revert"),
		Width: width,
	}, body)
}

// themeSelectedMsg applies a catalog entry to the root model (session only).
type themeSelectedMsg struct {
	entry theme.Entry
}

// themePreviewMsg live-previews a theme while the picker stays open.
// Must not persist settings.
type themePreviewMsg struct {
	entry theme.Entry
}

// themeRevertMsg restores the theme that was active when the picker opened.
type themeRevertMsg struct {
	entry theme.Entry
}

// themeSavedMsg reports the outcome of persisting a theme id as the default.
type themeSavedMsg struct {
	id  string
	err error
}

func saveThemeThroughCmd(settings host.Settings, id string) tea.Cmd {
	return func() tea.Msg {
		if settings == nil {
			return themeSavedMsg{id: id, err: errNoSettings}
		}
		return themeSavedMsg{id: id, err: settings.SaveTheme(id)}
	}
}
