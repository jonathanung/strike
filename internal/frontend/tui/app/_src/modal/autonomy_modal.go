package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/jonathanung/strike-cli/internal/frontend/tui/theme"
	"github.com/jonathanung/strike-cli/internal/frontend/tui/ui"
	"github.com/jonathanung/strike-cli/internal/protocol"
)

// autonomyModal is the centered exit-gate policy picker. The ladder is a fixed
// protocol vocabulary, so there is no loading or error state.
type autonomyModal struct {
	current protocol.Autonomy
	modes   []protocol.Autonomy
	cursor  int
	ops     chan<- protocol.Op
}

func newAutonomyModal(current protocol.Autonomy, ops chan<- protocol.Op) *autonomyModal {
	modes := protocol.Autonomies()
	m := &autonomyModal{current: current.Normalize(), modes: modes, ops: ops}
	for i, mode := range modes {
		if mode == m.current {
			m.cursor = i
			break
		}
	}
	return m
}

func (m *autonomyModal) update(msg tea.KeyPressMsg) (modal, tea.Cmd) {
	if isEscape(msg) {
		return nil, nil
	}
	switch msg.String() {
	case "up", "k", "ctrl+p":
		if m.cursor > 0 {
			m.cursor--
		}
		return m, nil
	case "down", "j", "ctrl+n":
		if m.cursor < len(m.modes)-1 {
			m.cursor++
		}
		return m, nil
	case "enter":
		if m.cursor >= len(m.modes) {
			return m, nil
		}
		ops, mode := m.ops, m.modes[m.cursor]
		return nil, func() tea.Msg {
			ops <- protocol.SetAutonomy{Mode: mode}
			return nil
		}
	default:
		return m, nil
	}
}

func (m *autonomyModal) view(width int, th theme.Theme) string {
	inner := max(1, ui.PanelInnerWidth(th, width))
	items := make([]ui.ListItem, len(m.modes))
	for i, mode := range m.modes {
		items[i] = ui.ListItem{
			Label:   string(mode),
			Detail:  mode.Describe(),
			Current: mode == m.current,
		}
	}
	body := ui.List(th, ui.ListOpts{
		Items:   items,
		Cursor:  m.cursor,
		Width:   inner,
		Visible: len(m.modes),
	})
	return ui.Dialog(th, ui.DialogOpts{
		Title: "Autonomy",
		Hint:  dotJoin(th, "up/down/j/k move", "enter select", "esc close"),
		Width: width,
	}, body)
}
