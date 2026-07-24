package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/jonathanung/strike-cli/internal/auth"
	"github.com/jonathanung/strike-cli/internal/config"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

// providerModal is the centered picker opened by bare /provider: every
// provider with its auth status. Selecting an authenticated provider
// switches to it; selecting an unauthenticated one starts its login flow
// (browser OAuth or API-key entry) and switches once it succeeds.
type providerModal struct {
	entries []providerEntry
	cursor  int
	current string
	store   *auth.Store
	ops     chan<- protocol.Op
	th      theme.Theme
}

type providerEntry struct {
	name   string
	status string
	authed bool
}

func newProviderModal(store *auth.Store, current string, ops chan<- protocol.Op, th theme.Theme) *providerModal {
	m := &providerModal{current: current, store: store, ops: ops, th: th}
	for _, name := range []string{"anthropic", "openai", "xai"} {
		status := auth.Describe(name, store)
		m.entries = append(m.entries, providerEntry{
			name:   name,
			status: status,
			authed: status != "none",
		})
	}
	m.entries = append(m.entries, providerEntry{name: "echo", status: "offline dev provider", authed: true})
	for i, e := range m.entries {
		if e.name == current {
			m.cursor = i
		}
	}
	return m
}

func (m *providerModal) update(msg tea.KeyMsg) (modal, tea.Cmd) {
	switch msg.String() {
	case "esc", "q":
		return nil, nil
	case "up", "k":
		m.cursor = (m.cursor + len(m.entries) - 1) % len(m.entries)
	case "down", "j", "tab":
		m.cursor = (m.cursor + 1) % len(m.entries)
	case "ctrl+d":
		// Save the highlighted provider as the global default.
		name := m.entries[m.cursor].name
		return m, func() tea.Msg {
			err := config.SetGlobalDefaults(name, "", "")
			return defaultsSavedMsg{text: "provider " + name, err: err}
		}
	case "enter":
		e := m.entries[m.cursor]
		if e.authed {
			ops, name := m.ops, e.name
			return nil, func() tea.Msg {
				ops <- protocol.SelectModel{Provider: name}
				return nil
			}
		}
		// Not authenticated: run the provider's login flow, then switch.
		switch e.name {
		case "anthropic":
			return newAPIKeyModal(e.name, m.store, m.th, true), nil
		default:
			return startOAuthModal(e.name, true)
		}
	}
	return m, nil
}

func (m *providerModal) view(width int, th theme.Theme) string {
	title := lipgloss.NewStyle().Foreground(th.Accent).Bold(true).Render("Select provider")
	okStyle := lipgloss.NewStyle().Foreground(th.Success)
	noStyle := lipgloss.NewStyle().Foreground(th.Error)
	muted := lipgloss.NewStyle().Foreground(th.TextMuted)

	var rows []string
	for i, e := range m.entries {
		cursor := "  "
		nameStyle := lipgloss.NewStyle().Foreground(th.Text)
		if i == m.cursor {
			cursor = "▸ "
			nameStyle = nameStyle.Foreground(th.Accent).Bold(true)
		}
		status := okStyle.Render(e.status)
		if !e.authed {
			status = noStyle.Render("not authenticated — enter to log in")
		}
		name := e.name
		if e.name == m.current {
			name += " (current)"
		}
		rows = append(rows, cursor+nameStyle.Render(padRight(name, 22))+status)
	}
	hint := muted.Render("↑/↓ move · enter select or log in · ctrl+d set default · esc close")

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(th.BorderFocus).
		Padding(0, 1).
		Width(width)
	return box.Render(title + "\n\n" + strings.Join(rows, "\n") + "\n\n" + hint)
}

func padRight(s string, n int) string {
	if len(s) >= n {
		return s + " "
	}
	return s + strings.Repeat(" ", n-len(s))
}
