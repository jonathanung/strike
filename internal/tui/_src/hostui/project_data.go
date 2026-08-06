package tui

import (
	tea "charm.land/bubbletea/v2"
)

// projectDataRefreshMsg asks memory/issues right-pane windows to reload from host.
type projectDataRefreshMsg struct{}

// projectDataMutatedMsg is emitted by pane key actions after a local mutation so
// the model can show a notice and refresh sibling panes.
type projectDataMutatedMsg struct {
	kind   string
	notice string
}

func isProjectDataTool(name string) bool {
	switch name {
	case "memory_write", "memory_read", "issue_write", "issue_read", "plan_write", "plan_read":
		return true
	default:
		return false
	}
}

// refreshProjectDataWindows reloads memory and issues panes from their host backends.
func refreshProjectDataWindows(r windowRegistry) windowRegistry {
	if len(r.windows) == 0 {
		return r
	}
	windows := append([]window(nil), r.windows...)
	changed := false
	for i, w := range windows {
		switch tw := w.(type) {
		case memoryWindow:
			windows[i] = tw.reload()
			changed = true
		case issuesWindow:
			windows[i] = tw.reload()
			changed = true
		}
	}
	if !changed {
		return r
	}
	r.windows = windows
	return r
}

func (m *Model) applyProjectDataMutation(msg projectDataMutatedMsg) tea.Cmd {
	m.windows = refreshProjectDataWindows(m.windows)
	if msg.notice != "" {
		m.setNotice(msg.notice, false)
	}
	return nil
}

// openMemoryBrowser activates the memory right pane (optionally tag-filtered).
func (m Model) openMemoryBrowser(tag string) (tea.Model, tea.Cmd) {
	m.clearNotice()
	for i, w := range m.windows.windows {
		mw, ok := w.(memoryWindow)
		if !ok {
			continue
		}
		next := mw.bind(m.services.Memory, tag)
		windows := append([]window(nil), m.windows.windows...)
		windows[i] = next
		m.windows.windows = windows
		break
	}
	var ok bool
	m.windows, ok = m.windows.activate(memoryWindowID)
	if !ok {
		m.setNotice("memory: browser window missing", true)
		return m, nil
	}
	cmd := m.setPaneFocus(focusRight)
	m.reflow()
	return m, cmd
}

// openIssuesBrowser activates the issues right pane (optionally status-filtered).
func (m Model) openIssuesBrowser(status string) (tea.Model, tea.Cmd) {
	m.clearNotice()
	for i, w := range m.windows.windows {
		iw, ok := w.(issuesWindow)
		if !ok {
			continue
		}
		next := iw.bind(m.services.Issues, status)
		windows := append([]window(nil), m.windows.windows...)
		windows[i] = next
		m.windows.windows = windows
		break
	}
	var ok bool
	m.windows, ok = m.windows.activate(issuesWindowID)
	if !ok {
		m.setNotice("issues: browser window missing", true)
		return m, nil
	}
	cmd := m.setPaneFocus(focusRight)
	m.reflow()
	return m, cmd
}
