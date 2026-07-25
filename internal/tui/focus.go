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
