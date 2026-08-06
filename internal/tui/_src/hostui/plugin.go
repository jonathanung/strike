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
