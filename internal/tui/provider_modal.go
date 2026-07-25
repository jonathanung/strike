package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

const providerModalVisible = 8

// providerModal is the centered picker opened by bare /provider: every
// provider with its credential state, sourced from host.Auth.Statuses(),
// plus a fixed trailing "Add custom provider…" action.
// Selecting an authenticated provider switches to it; selecting an
// unauthenticated one starts its login (data-driven from the capability flags:
// API-key-only opens the key modal, OAuth opens the browser modal) and
// switches once it succeeds.
type providerModal struct {
	statuses []host.ProviderStatus
	cursor   int
	current  string
	auth     host.Auth
	settings host.Settings
	services host.Services
	ops      chan<- protocol.Op
	th       theme.Theme
}

// provider row count is statuses + 1 add-custom action.
func (m *providerModal) rowCount() int {
	return len(m.statuses) + 1
}

func newProviderModal(services host.Services, current string, ops chan<- protocol.Op, th theme.Theme) *providerModal {
	m := &providerModal{
		current:  current,
		auth:     services.Auth,
		settings: services.Settings,
		services: services,
		ops:      ops,
		th:       th,
	}
	if services.Auth != nil {
		m.statuses = services.Auth.Statuses()
	}
	for i, s := range m.statuses {
		if s.Name == current {
			m.cursor = i
		}
	}
	return m
}

func (m *providerModal) update(msg tea.KeyMsg) (modal, tea.Cmd) {
	n := m.rowCount()
	if n == 0 {
		if isEscape(msg) || msg.String() == "q" {
			return nil, nil
		}
		return m, nil
	}
	if isEscape(msg) || msg.String() == "q" {
		return nil, nil
	}
	switch msg.String() {
	case "up", "k":
		m.cursor = (m.cursor + n - 1) % n
	case "down", "j", "tab":
		m.cursor = (m.cursor + 1) % n
	case "ctrl+d":
		if m.cursor < len(m.statuses) {
			name := m.statuses[m.cursor].Name
			return m, saveDefaultsThroughCmd(m.settings, name, "", "", "", "provider "+name)
		}
	case "enter":
		return m.selectCurrent()
	}
	return m, nil
}

// selectCurrent switches to an authenticated provider, begins login for an
// unauthenticated one, or opens the add-custom form for the trailing row.
func (m *providerModal) selectCurrent() (modal, tea.Cmd) {
	if m.cursor >= len(m.statuses) {
		return newCustomProviderFormModal(m.services, m.ops, m.th, nil, true, nil), nil
	}
	s := m.statuses[m.cursor]
	if s.Authed {
		ops, name := m.ops, s.Name
		return nil, func() tea.Msg {
			ops <- protocol.SelectModel{Provider: name}
			return nil
		}
	}
	return startLogin(m.auth, m.th, s.Name, "", true)
}

func (m *providerModal) view(width int, th theme.Theme) string {
	items := make([]ui.ListItem, 0, m.rowCount())
	for _, s := range m.statuses {
		detail := s.Detail
		if !s.Authed && !s.Builtin {
			detail = "not authenticated — enter to log in"
		}
		items = append(items, ui.ListItem{Label: s.Name, Detail: detail, Current: s.Name == m.current})
	}
	ell := th.Resolve().Icons.Ellipsis
	items = append(items, ui.ListItem{
		Label:  "Add custom provider" + ell,
		Detail: "self-hosted / gateway (ollama, azure, kimi, " + ell + ")",
	})
	body := ui.List(th, ui.ListOpts{
		Items:   items,
		Cursor:  m.cursor,
		Width:   max(1, ui.PanelInnerWidth(th, width)),
		Visible: providerModalVisible,
		Empty:   "no providers configured",
	})
	return ui.Dialog(th, ui.DialogOpts{
		Title: "Select provider",
		Hint:  dotJoin(th, "up/down/j/k move", "enter select or log in", "ctrl+d set default", "esc close"),
		Width: width,
	}, body)
}
