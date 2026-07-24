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
// provider with its credential state, sourced from host.Auth.Statuses().
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
	ops      chan<- protocol.Op
	th       theme.Theme
}

func newProviderModal(services host.Services, current string, ops chan<- protocol.Op, th theme.Theme) *providerModal {
	m := &providerModal{current: current, auth: services.Auth, settings: services.Settings, ops: ops, th: th}
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
	if len(m.statuses) == 0 {
		if msg.String() == "esc" || msg.String() == "q" {
			return nil, nil
		}
		return m, nil
	}
	switch msg.String() {
	case "esc", "q":
		return nil, nil
	case "up", "k":
		m.cursor = (m.cursor + len(m.statuses) - 1) % len(m.statuses)
	case "down", "j", "tab":
		m.cursor = (m.cursor + 1) % len(m.statuses)
	case "ctrl+d":
		// Save the highlighted provider as the global default.
		name := m.statuses[m.cursor].Name
		return m, saveDefaultsThroughCmd(m.settings, name, "", "", "", "provider "+name)
	case "enter":
		return m.selectCurrent()
	}
	return m, nil
}

// selectCurrent switches to an authenticated provider or begins the login flow
// for an unauthenticated one, switching once it succeeds.
func (m *providerModal) selectCurrent() (modal, tea.Cmd) {
	s := m.statuses[m.cursor]
	if s.Authed {
		ops, name := m.ops, s.Name
		return nil, func() tea.Msg {
			ops <- protocol.SelectModel{Provider: name}
			return nil
		}
	}
	switch {
	case s.OAuth:
		return startOAuthModal(m.auth, s.Name, true)
	case s.Device:
		return startDeviceModal(m.auth, s.Name, true)
	case s.APIKey:
		return newAPIKeyModal(s.Name, m.auth, m.th, true), nil
	default:
		return m, nil
	}
}

func (m *providerModal) view(width int, th theme.Theme) string {
	items := make([]ui.ListItem, len(m.statuses))
	for i, s := range m.statuses {
		detail := s.Detail
		if !s.Authed && !s.Builtin {
			detail = "not authenticated — enter to log in"
		}
		items[i] = ui.ListItem{Label: s.Name, Detail: detail, Current: s.Name == m.current}
	}
	body := ui.List(th, ui.ListOpts{
		Items:   items,
		Cursor:  m.cursor,
		Width:   max(1, ui.InnerWidth(width)),
		Visible: providerModalVisible,
		Empty:   "no providers configured",
	})
	return ui.Dialog(th, ui.DialogOpts{
		Title: "Select provider",
		Hint:  "↑/↓ move · enter select or log in · ctrl+d set default · esc close",
		Width: width,
	}, body)
}
