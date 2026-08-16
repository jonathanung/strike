package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/jonathanung/strike-cli/internal/frontend/tui/theme"
	"github.com/jonathanung/strike-cli/internal/frontend/tui/ui"
)

// rootSwitcherEntry is one entry in the session switcher.
type rootSwitcherEntry struct {
	id    string
	label string
	state string // brief status text
}

// rootSwitcherModal is the centered session switcher (ctrl+s). It lists
// concurrent root sessions with numbered shortcuts (1-9). Number keys jump
// directly; enter selects the highlighted entry; esc dismisses. Type to filter
// (same pattern as the command palette).
type rootSwitcherModal struct {
	roots  []rootSwitcherEntry
	filter string
	cursor int
}

func newRootSwitcherModal(roots []rootSwitcherEntry) *rootSwitcherModal {
	return &rootSwitcherModal{roots: roots}
}

func (m *rootSwitcherModal) filtered() []rootSwitcherEntry {
	query := strings.ToLower(strings.TrimSpace(m.filter))
	if query == "" {
		return m.roots
	}
	matches := make([]rootSwitcherEntry, 0, len(m.roots))
	for _, r := range m.roots {
		if strings.Contains(strings.ToLower(r.label), query) ||
			strings.Contains(strings.ToLower(r.state), query) {
			matches = append(matches, r)
		}
	}
	return matches
}

func (m *rootSwitcherModal) update(msg tea.KeyPressMsg) (modal, tea.Cmd) {
	list := m.filtered()
	if isEscape(msg) {
		return nil, nil
	}
	switch msg.String() {
	case "ctrl+n":
		return nil, func() tea.Msg { return agentsSpawnMsg{} }
	}
	// Direct number shortcut: 1-9 jump to the Nth filtered entry.
	if len([]rune(msg.Text)) == 1 {
		r := []rune(msg.Text)[0]
		if r >= '1' && r <= '9' {
			idx := int(r - '1')
			if idx < len(list) {
				return nil, func() tea.Msg { return activateRootMsg{id: list[idx].id} }
			}
			return m, nil
		}
	}
	switch msg.String() {
	case "up", "ctrl+p":
		if len(list) > 0 {
			m.cursor = (m.cursor + len(list) - 1) % len(list)
		}
	case "down":
		if len(list) > 0 {
			m.cursor = (m.cursor + 1) % len(list)
		}
	case "backspace":
		runes := []rune(m.filter)
		if len(runes) > 0 {
			m.filter = string(runes[:len(runes)-1])
			m.cursor = 0
		}
	case "enter":
		if m.cursor >= 0 && m.cursor < len(list) {
			return nil, func() tea.Msg { return activateRootMsg{id: list[m.cursor].id} }
		}
		return m, nil
	default:
		if len(msg.Text) > 0 {
			m.filter += msg.Text
			m.cursor = 0
		}
	}
	return m, nil
}

func (m *rootSwitcherModal) view(width int, th theme.Theme) string {
	list := m.filtered()
	if m.cursor >= len(list) {
		m.cursor = max(0, len(list)-1)
	}
	items := make([]ui.ListItem, len(list))
	for i, r := range list {
		prefix := ""
		if i < 9 {
			prefix = string(rune('1'+i)) + ") "
		}
		items[i] = ui.ListItem{
			Label:  prefix + r.label,
			Detail: r.state,
		}
	}
	inner := max(1, ui.PanelInnerWidth(th, width))
	body := ui.List(th, ui.ListOpts{
		Items:      items,
		Cursor:     m.cursor,
		Width:      inner,
		Visible:    len(items),
		ShowFilter: true,
		Filter:     m.filter,
		Total:      len(m.roots),
		Empty:      "no matching sessions",
	})
	hint := dotJoin(th, "ctrl+n new", "type to filter", "1-9 jump", "↑/↓ move", "enter select", "esc close")
	return ui.Dialog(th, ui.DialogOpts{
		Title: "Switch session",
		Hint:  hint,
		Width: width,
	}, body)
}
