package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/jonathanung/strike-cli/internal/frontend/host"
	"github.com/jonathanung/strike-cli/internal/frontend/tui/theme"
	"github.com/jonathanung/strike-cli/internal/frontend/tui/ui"
	"github.com/jonathanung/strike-cli/internal/protocol"
)

// permissionModeModal is the centered tool-permission posture picker. Enter
// switches the session mode; ctrl+d saves the highlighted mode as the global
// default for new sessions (config permissionMode).
type permissionModeModal struct {
	current  protocol.PermissionMode
	modes    []protocol.PermissionMode
	cursor   int
	ops      chan<- protocol.Op
	settings host.Settings
}

func newPermissionModeModal(current protocol.PermissionMode, ops chan<- protocol.Op, settings host.Settings) *permissionModeModal {
	modes := protocol.PermissionModes()
	m := &permissionModeModal{current: current.Normalize(), modes: modes, ops: ops, settings: settings}
	for i, mode := range modes {
		if mode == m.current {
			m.cursor = i
			break
		}
	}
	return m
}

func (m *permissionModeModal) update(msg tea.KeyPressMsg) (modal, tea.Cmd) {
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
			ops <- protocol.SetPermissionMode{Mode: mode}
			return nil
		}
	case "ctrl+d":
		if m.cursor >= len(m.modes) {
			return m, nil
		}
		mode := m.modes[m.cursor]
		return m, saveDefaultsThroughCmd(m.settings, "", "", "", "", string(mode), "mode "+string(mode))
	default:
		return m, nil
	}
}

func (m *permissionModeModal) view(width int, th theme.Theme) string {
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
		Title: "Permission mode",
		Hint:  dotJoin(th, "up/down/j/k move", "enter select", "ctrl+d set default", "esc close"),
		Width: width,
	}, body)
}
