package tui

import (
	"errors"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

const providerModalVisible = 8

// logoutConfirmWindow is how long the first "\" arms a logout confirmation.
const logoutConfirmWindow = 3 * time.Second

var (
	errBuiltinLogout = errors.New("builtin providers have no credentials to clear")
	errNoAuth        = errors.New("authentication is unavailable")
)

// providerLogoutMsg reports the outcome of "\\ \\" logout from the provider modal.
type providerLogoutMsg struct {
	provider string
	err      error
}

// providerModal is the centered picker opened by bare /provider: every
// provider with its credential state, sourced from host.Auth.Statuses(),
// plus a fixed trailing "Add custom provider…" action that is never filtered
// away. Type to filter; "\\" twice within 3s logs out the highlighted
// provider. Selecting an authenticated provider switches to it; selecting an
// unauthenticated one starts its login (multi-method providers open the
// method chooser) and switches once it succeeds.
type providerModal struct {
	statuses      []host.ProviderStatus
	filter        string
	cursor        int
	current       string
	auth          host.Auth
	settings      host.Settings
	services      host.Services
	ops           chan<- protocol.Op
	th            theme.Theme
	logoutArmedAt time.Time
	// now is time.Now in production; tests may override for the logout arm window.
	now func() time.Time
}

func (m *providerModal) clock() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

// filtered returns statuses matching the type-to-filter query (case-insensitive
// substring on name). An empty filter returns every status.
func (m *providerModal) filtered() []host.ProviderStatus {
	if m.filter == "" {
		return m.statuses
	}
	q := strings.ToLower(m.filter)
	var out []host.ProviderStatus
	for _, s := range m.statuses {
		if strings.Contains(strings.ToLower(s.Name), q) {
			out = append(out, s)
		}
	}
	return out
}

// rowCount is filtered statuses + 1 add-custom action (always present).
func (m *providerModal) rowCount() int {
	return len(m.filtered()) + 1
}

func (m *providerModal) clampCursor() {
	n := m.rowCount()
	if n <= 0 {
		m.cursor = 0
		return
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= n {
		m.cursor = n - 1
	}
}

// reloadStatuses refreshes credential state from the host after logout/login.
func (m *providerModal) reloadStatuses() {
	if m.auth == nil {
		m.statuses = nil
		return
	}
	m.statuses = m.auth.Statuses()
	m.clampCursor()
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

func (m *providerModal) clearLogoutArm() {
	m.logoutArmedAt = time.Time{}
}

func (m *providerModal) update(msg tea.KeyMsg) (modal, tea.Cmd) {
	list := m.filtered()
	n := len(list) + 1
	if isEscape(msg) || msg.String() == "q" {
		return nil, nil
	}
	switch msg.String() {
	case "up", "ctrl+p":
		m.clearLogoutArm()
		if m.cursor > 0 {
			m.cursor--
		}
		return m, nil
	case "down", "ctrl+n", "tab":
		m.clearLogoutArm()
		if m.cursor < n-1 {
			m.cursor++
		}
		return m, nil
	case "backspace":
		m.clearLogoutArm()
		if m.filter != "" {
			// Rune-safe trim (provider names / filter may be non-ASCII).
			runes := []rune(m.filter)
			m.filter = string(runes[:len(runes)-1])
			m.cursor = 0
		}
		return m, nil
	case "ctrl+d":
		m.clearLogoutArm()
		if m.cursor < len(list) {
			name := list[m.cursor].Name
			return m, saveDefaultsThroughCmd(m.settings, name, "", "", "", "provider "+name)
		}
		return m, nil
	case "enter":
		m.clearLogoutArm()
		return m.selectCurrent()
	case "\\":
		return m.handleLogoutKey()
	default:
		if msg.Type == tea.KeyRunes {
			m.clearLogoutArm()
			m.filter += string(msg.Runes)
			m.cursor = 0
		}
		return m, nil
	}
}

// handleLogoutKey implements "\\" twice within logoutConfirmWindow to deauth
// the highlighted provider. The first press arms; the second commits.
func (m *providerModal) handleLogoutKey() (modal, tea.Cmd) {
	list := m.filtered()
	now := m.clock()
	armed := !m.logoutArmedAt.IsZero() && now.Sub(m.logoutArmedAt) <= logoutConfirmWindow
	if !armed {
		m.logoutArmedAt = now
		return m, nil
	}
	m.clearLogoutArm()
	if m.cursor >= len(list) {
		// Add-custom row — nothing to log out of.
		return m, nil
	}
	s := list[m.cursor]
	if s.Builtin {
		return m, func() tea.Msg {
			return providerLogoutMsg{provider: s.Name, err: errBuiltinLogout}
		}
	}
	if m.auth == nil {
		return m, func() tea.Msg {
			return providerLogoutMsg{provider: s.Name, err: errNoAuth}
		}
	}
	// Multi-method unauthenticated providers open the method chooser on enter;
	// logout only applies when there is something to clear. Always call Logout
	// so host state and env-only rows stay consistent with /auth logout.
	name, authsvc := s.Name, m.auth
	return m, func() tea.Msg {
		err := authsvc.Logout(name)
		return providerLogoutMsg{provider: name, err: err}
	}
}

// selectCurrent switches to an authenticated provider, begins login for an
// unauthenticated one (multi-method → chooser via startLogin), or opens the
// add-custom form for the trailing row.
func (m *providerModal) selectCurrent() (modal, tea.Cmd) {
	list := m.filtered()
	if m.cursor >= len(list) {
		return newCustomProviderFormModal(m.services, m.ops, m.th, nil, true, nil), nil
	}
	s := list[m.cursor]
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
	list := m.filtered()
	m.clampCursor()
	items := make([]ui.ListItem, 0, len(list)+1)
	for _, s := range list {
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
		Items:      items,
		Cursor:     m.cursor,
		Width:      max(1, ui.PanelInnerWidth(th, width)),
		Visible:    providerModalVisible,
		ShowFilter: true,
		Filter:     m.filter,
		Total:      len(m.statuses) + 1, // + add-custom, always listed
		Empty:      "no matches for \"" + m.filter + "\"",
	})
	hints := []string{"type to filter", "↑/↓ move", "enter select or log in", `\\ \\ logout`, "ctrl+d set default", "esc close"}
	if !m.logoutArmedAt.IsZero() && m.clock().Sub(m.logoutArmedAt) <= logoutConfirmWindow {
		hints = []string{`\\ again to log out`, "esc close"}
	}
	return ui.Dialog(th, ui.DialogOpts{
		Title: "Select provider",
		Hint:  dotJoin(th, hints...),
		Width: width,
	}, body)
}
