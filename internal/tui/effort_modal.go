package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

// effortModal is the centered reasoning-effort picker. Unlike the model
// picker there is nothing to fetch — the ladder is a fixed protocol
// vocabulary — so it has no loading or error state. Enter switches, ctrl+d
// saves the level as a global default.
type effortModal struct {
	current  protocol.Effort
	levels   []protocol.Effort
	cursor   int
	ops      chan<- protocol.Op
	settings host.Settings
}

func newEffortModal(current protocol.Effort, ops chan<- protocol.Op, settings host.Settings) *effortModal {
	levels := protocol.Efforts()
	m := &effortModal{current: current, levels: levels, ops: ops, settings: settings}
	// Open on the active level so enter is a no-op rather than a surprise.
	for i, level := range levels {
		if level == current {
			m.cursor = i
			break
		}
	}
	return m
}

func (m *effortModal) update(msg tea.KeyMsg) (modal, tea.Cmd) {
	switch msg.String() {
	case "esc":
		return nil, nil
	case "up", "ctrl+p":
		if m.cursor > 0 {
			m.cursor--
		}
		return m, nil
	case "down", "ctrl+n":
		if m.cursor < len(m.levels)-1 {
			m.cursor++
		}
		return m, nil
	case "enter":
		if m.cursor >= len(m.levels) {
			return m, nil
		}
		ops, level := m.ops, m.levels[m.cursor]
		return nil, func() tea.Msg {
			ops <- protocol.SetEffort{Level: level}
			return nil
		}
	case "ctrl+d":
		if m.cursor >= len(m.levels) {
			return m, nil
		}
		level := m.levels[m.cursor]
		return m, saveDefaultsThroughCmd(m.settings, "", "", "", string(level), "effort "+string(level))
	default:
		return m, nil
	}
}

func (m *effortModal) view(width int, th theme.Theme) string {
	inner := max(1, ui.PanelInnerWidth(th, width))
	items := make([]ui.ListItem, len(m.levels))
	for i, level := range m.levels {
		items[i] = ui.ListItem{
			Label:   string(level),
			Detail:  level.Describe(),
			Current: level == m.current,
		}
	}
	body := ui.List(th, ui.ListOpts{
		Items:   items,
		Cursor:  m.cursor,
		Width:   inner,
		Visible: len(m.levels),
	})
	return ui.Dialog(th, ui.DialogOpts{
		Title: "Reasoning effort",
		Hint:  dotJoin(th, "↑/↓ move", "enter select", "ctrl+d set default", "esc close"),
		Width: width,
	}, body)
}
