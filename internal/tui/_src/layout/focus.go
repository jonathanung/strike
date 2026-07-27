package tui

import tea "github.com/charmbracelet/bubbletea"

type paneFocus uint8

const (
	focusLeft paneFocus = iota
	focusRight
)

func (m *Model) setPaneFocus(focus paneFocus) tea.Cmd {
	m.focus = focus
	if focus == focusRight {
		m.composer.Blur()
		return nil
	}
	return m.composer.Focus()
}

func (m *Model) togglePaneFocus() tea.Cmd {
	if m.focus == focusLeft {
		return m.setPaneFocus(focusRight)
	}
	return m.setPaneFocus(focusLeft)
}

func (m *Model) focusPane(focus paneFocus) tea.Cmd {
	if m.focus == focus {
		return nil
	}
	return m.setPaneFocus(focus)
}

// focusRightWindow activates the named right-pane window and moves focus there.
// Used by pane-jump slash commands (/agents, /files, …).
func (m Model) focusRightWindow(id string) (tea.Model, tea.Cmd) {
	m.resetComposer()
	m.clearNotice()
	var ok bool
	m.windows, ok = m.windows.activate(id)
	if !ok {
		m.setNotice("pane "+id+" missing", true)
		return m, nil
	}
	cmd := m.setPaneFocus(focusRight)
	m.reflow()
	return m, cmd
}
