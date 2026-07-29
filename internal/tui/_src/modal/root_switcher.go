package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

// rootSwitcherEntry is one entry in the session switcher.
type rootSwitcherEntry struct {
	id    string
	label string
	state string // brief status text
}

// rootSwitcherModal is the centered session switcher (ctrl+s). It lists
// concurrent root sessions with numbered shortcuts (1-9). Number keys jump
// directly; enter/j selects the highlighted entry; esc dismisses.
type rootSwitcherModal struct {
	roots  []rootSwitcherEntry
	cursor int
}

func newRootSwitcherModal(roots []rootSwitcherEntry) *rootSwitcherModal {
	return &rootSwitcherModal{roots: roots}
}

func (m *rootSwitcherModal) update(msg tea.KeyMsg) (modal, tea.Cmd) {
	if isEscape(msg) {
		return nil, nil
	}
	// Direct number shortcut: 1-9 jump to that root.
	if len(msg.Runes) == 1 {
		r := msg.Runes[0]
		if r >= '1' && r <= '9' {
			idx := int(r - '1')
			if idx < len(m.roots) {
				return nil, func() tea.Msg { return activateRootMsg{id: m.roots[idx].id} }
			}
			return m, nil
		}
	}
	switch msg.String() {
	case "up", "k", "ctrl+p":
		if m.cursor > 0 {
			m.cursor--
		}
		return m, nil
	case "down", "j", "ctrl+n":
		if m.cursor < len(m.roots)-1 {
			m.cursor++
		}
		return m, nil
	case "enter":
		if m.cursor >= 0 && m.cursor < len(m.roots) {
			return nil, func() tea.Msg { return activateRootMsg{id: m.roots[m.cursor].id} }
		}
		return m, nil
	}
	return m, nil
}

func (m *rootSwitcherModal) view(width int, th theme.Theme) string {
	inner := max(1, ui.PanelInnerWidth(th, width))
	items := make([]ui.ListItem, len(m.roots))
	for i, r := range m.roots {
		prefix := ""
		if i < 9 {
			prefix = string(rune('1'+i)) + ") "
		}
		items[i] = ui.ListItem{
			Label: prefix + r.label,
		}
	}
	body := ui.List(th, ui.ListOpts{
		Items:   items,
		Cursor:  m.cursor,
		Width:   inner,
		Visible: len(items),
		Empty:   "no concurrent sessions",
	})
	hint := dotJoin(th, "1-9 jump", "up/down/j/k move", "enter select", "esc close")
	return ui.Dialog(th, ui.DialogOpts{
		Title: "Switch session",
		Hint:  hint,
		Width: width,
	}, body)
}
