package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

// agentModal is the centered agent picker. Enter switches, ctrl+d saves the
// agent as a global default. Invalid names are omitted (same floor as the
// palette and status chrome).
type agentModal struct {
	current  string
	agents   []string
	cursor   int
	ops      chan<- protocol.Op
	settings host.Settings
}

func newAgentModal(current string, agents []string, ops chan<- protocol.Op, settings host.Settings) *agentModal {
	safe := make([]string, 0, len(agents))
	for _, name := range agents {
		if validAgentName(name) {
			safe = append(safe, name)
		}
	}
	m := &agentModal{current: current, agents: safe, ops: ops, settings: settings}
	for i, name := range safe {
		if name == current {
			m.cursor = i
			break
		}
	}
	return m
}

func (m *agentModal) update(msg tea.KeyPressMsg) (modal, tea.Cmd) {
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
		if m.cursor < len(m.agents)-1 {
			m.cursor++
		}
		return m, nil
	case "enter":
		if m.cursor >= len(m.agents) {
			return m, nil
		}
		ops, name := m.ops, m.agents[m.cursor]
		return nil, func() tea.Msg {
			ops <- protocol.SelectAgent{Name: name}
			return nil
		}
	case "ctrl+d":
		if m.cursor >= len(m.agents) {
			return m, nil
		}
		name := m.agents[m.cursor]
		return m, saveDefaultsThroughCmd(m.settings, "", "", name, "", "", "agent "+name)
	default:
		return m, nil
	}
}

func (m *agentModal) view(width int, th theme.Theme) string {
	inner := max(1, ui.PanelInnerWidth(th, width))
	items := make([]ui.ListItem, len(m.agents))
	for i, name := range m.agents {
		items[i] = ui.ListItem{
			Label:   sanitizeDisplayData(name),
			Current: name == m.current,
		}
	}
	body := ui.List(th, ui.ListOpts{
		Items:   items,
		Cursor:  m.cursor,
		Width:   inner,
		Visible: len(m.agents),
		Empty:   "no agents configured",
	})
	return ui.Dialog(th, ui.DialogOpts{
		Title: "Select agent",
		Hint:  dotJoin(th, "up/down/j/k move", "enter select", "ctrl+d set default", "esc close"),
		Width: width,
	}, body)
}
