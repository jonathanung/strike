package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// projectDataRefreshMsg asks memory/issues/plans right-pane windows to reload from host.
type projectDataRefreshMsg struct{}

// projectDataMutatedMsg is emitted by pane key actions after a local mutation so
// the model can show a notice and refresh sibling panes.
type projectDataMutatedMsg struct {
	kind   string
	notice string
}

func isProjectDataTool(name string) bool {
	switch name {
	case "memory_write", "memory_read", "issue_write", "issue_read",
		// Plan tools (#721): refresh the plans pane when agents mutate artifacts.
		"plan_write", "plan_read":
		return true
	default:
		return false
	}
}

// refreshProjectDataWindows reloads memory, issues, and plans panes from host backends.
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
		case plansWindow:
			windows[i] = tw.reloadPreserve()
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

// openPlansBrowser activates the plans right pane for the current root.
// When listOnly is false, the root's active plan opens in detail when present.
func (m Model) openPlansBrowser(listOnly bool) (tea.Model, tea.Cmd) {
	m.clearNotice()
	for i, w := range m.windows.windows {
		pw, ok := w.(plansWindow)
		if !ok {
			continue
		}
		var next plansWindow
		if listOnly {
			next = pw.bindList(m.services.Plans, m.sessionID)
		} else {
			next = pw.bind(m.services.Plans, m.sessionID)
		}
		windows := append([]window(nil), m.windows.windows...)
		windows[i] = next
		m.windows.windows = windows
		break
	}
	var ok bool
	m.windows, ok = m.windows.activate(plansWindowID)
	if !ok {
		m.setNotice("plans: browser window missing", true)
		return m, nil
	}
	cmd := m.setPaneFocus(focusRight)
	m.reflow()
	return m, cmd
}

// openPlanByID loads one plan into the plans pane (detail mode).
func (m Model) openPlanByID(id string) (tea.Model, tea.Cmd) {
	m.clearNotice()
	id = strings.TrimSpace(id)
	for i, w := range m.windows.windows {
		pw, ok := w.(plansWindow)
		if !ok {
			continue
		}
		next := pw.bindList(m.services.Plans, m.sessionID)
		next = next.openPlan(id)
		windows := append([]window(nil), m.windows.windows...)
		windows[i] = next
		m.windows.windows = windows
		break
	}
	var ok bool
	m.windows, ok = m.windows.activate(plansWindowID)
	if !ok {
		m.setNotice("plans: browser window missing", true)
		return m, nil
	}
	cmd := m.setPaneFocus(focusRight)
	m.reflow()
	return m, cmd
}
