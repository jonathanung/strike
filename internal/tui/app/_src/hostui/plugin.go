package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

func (m Model) handlePluginCommand(args []string) (tea.Model, tea.Cmd) {
	m.resetComposer()
	m.clearNotice()
	if m.services.Plugins == nil {
		m.setNotice("plugin manager unavailable", true)
		return m, nil
	}
	if len(args) > 0 {
		switch strings.ToLower(strings.TrimSpace(args[0])) {
		case "help", "-h", "--help":
			m.setNotice("usage: /plugin — open plugin manager (install, trust, update, remove)", false)
			return m, nil
		}
	}
	m.modal = newPluginModal(m.services.Plugins)
	m.reflow()
	return m, nil
}

func (m Model) handlePaneCommand(args []string) (tea.Model, tea.Cmd) {
	m.resetComposer()
	m.clearNotice()
	ids := pluginPaneIDs(m.windows)
	if len(args) == 0 {
		if len(ids) == 0 {
			m.setNotice("no plugin panes loaded — enable a pane plugin via /plugin", true)
			return m, nil
		}
		m.setNotice("plugin panes: "+strings.Join(ids, ", ")+" — /pane <id>", false)
		return m, nil
	}
	id := strings.TrimSpace(args[0])
	if id == "" {
		m.setNotice("usage: /pane <id>", true)
		return m, nil
	}
	return m.focusRightWindow(id)
}
