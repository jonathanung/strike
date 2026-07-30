package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

const themeModalVisible = 10

// themeModal is the centered /theme picker: bundled JSON themes plus any
// user/project files under ~/.strike/themes and ./.strike/themes.
type themeModal struct {
	entries  []theme.Entry
	filtered []theme.Entry
	cursor   int
	filter   string
	current  string
	settings host.Settings
}

func newThemeModal(entries []theme.Entry, current string, settings host.Settings) *themeModal {
	m := &themeModal{entries: entries, current: current, settings: settings}
	m.refilter()
	for i, e := range m.filtered {
		if e.ID == current {
			m.cursor = i
			break
		}
	}
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
		if strings.Contains(strings.ToLower(e.ID), q) || strings.Contains(strings.ToLower(e.Name), q) {
			out = append(out, e)
		}
	}
	m.filtered = out
	if m.cursor >= len(m.filtered) {
		m.cursor = max(0, len(m.filtered)-1)
	}
}

func (m *themeModal) update(msg tea.KeyPressMsg) (modal, tea.Cmd) {
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
	case "backspace":
		if m.filter != "" {
			m.filter = m.filter[:len(m.filter)-1]
			m.cursor = 0
			m.refilter()
		}
		return m, nil
	case "enter":
		if m.cursor < 0 || m.cursor >= len(m.filtered) {
			return m, nil
		}
		e := m.filtered[m.cursor]
		return nil, func() tea.Msg { return themeSelectedMsg{entry: e} }
	case "ctrl+d":
		if m.cursor < 0 || m.cursor >= len(m.filtered) {
			return m, nil
		}
		id := m.filtered[m.cursor].ID
		return m, saveThemeThroughCmd(m.settings, id)
	default:
		if len(msg.Text) > 0 {
			m.filter += msg.Text
			m.cursor = 0
			m.refilter()
		}
		return m, nil
	}
}

func (m *themeModal) view(width int, th theme.Theme) string {
	th = th.Resolve()
	inner := max(1, ui.PanelInnerWidth(th, width))
	items := make([]ui.ListItem, len(m.filtered))
	for i, e := range m.filtered {
		detail := e.Source
		if e.Name != e.ID {
			detail = e.ID + themedSpace(th.Spacing.XS) + th.Icons.Dot + themedSpace(th.Spacing.XS) + e.Source
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
		Hint:  dotJoin(th, "up/down/j/k move", "type to filter", "enter select", "ctrl+d set default", "esc close"),
		Width: width,
	}, body)
}

// themeSelectedMsg applies a catalog entry to the root model.
type themeSelectedMsg struct {
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
