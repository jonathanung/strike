package tui

import (
	"context"
	"errors"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

// Async messages for the auth flows. Logins run in tea.Cmd goroutines so the
// event loop stays responsive while the user is in the browser; the centered
// authWaitModal shows progress and the login link meanwhile. Credentials are
// persisted inside the host login handles — these messages carry only
// user-facing outcome strings, never tokens.

type authStartedMsg struct {
	provider string
	login    *host.OAuthLogin
}

type authDeviceMsg struct {
	provider string
	login    *host.DeviceLogin
}

type authDoneMsg struct {
	provider    string
	message     string
	err         error
	selectAfter bool // switch to the provider once login succeeds
}

// handleAuth dispatches "/auth ..." commands. Bare /auth is an alias for
// bare /provider: the same centered picker with auth status, where selecting
// an unauthenticated provider starts its login. Provider names, usage strings,
// and per-provider login methods are all data-driven from host.Auth.Statuses().
func (m Model) handleAuth(args []string) (tea.Model, tea.Cmd) {
	m.composer.Reset()
	authsvc := m.services.Auth
	if authsvc == nil {
		m.setNotice("authentication is unavailable", true)
		return m, nil
	}
	statuses := authsvc.Statuses()
	names := credentialProviderNames(statuses)

	if len(args) == 0 {
		m.modal = newProviderModal(m.services, m.providerName, m.ops, m.th)
		return m, nil
	}
	switch args[0] {
	case "status":
		var parts []string
		for _, s := range statuses {
			if s.Builtin {
				continue
			}
			parts = append(parts, s.Name+": "+authsvc.Describe(s.Name))
		}
		m.setNotice(strings.Join(parts, " · "), false)
		return m, nil
	case "logout":
		if len(args) < 2 {
			m.setNotice("usage: /auth logout <"+strings.Join(names, "|")+">", true)
			return m, nil
		}
		if err := authsvc.Logout(args[1]); err != nil {
			m.setNotice("logout failed: "+err.Error(), true)
			return m, nil
		}
		m.setNotice("logged out of "+args[1], false)
		return m, nil
	}

	provider := args[0]
	st, ok := findStatus(statuses, provider)
	if !ok || st.Builtin {
		m.setNotice("unknown provider "+provider+" — usage: /auth <"+strings.Join(names, "|")+"> [key|device]", true)
		return m, nil
	}
	method := ""
	if len(args) > 1 {
		method = args[1]
	}
	switch method {
	case "key":
		if !st.APIKey {
			m.setNotice(provider+" does not accept an API key", true)
			return m, nil
		}
		m.modal = newAPIKeyModal(provider, authsvc, m.th, false)
		return m, nil
	case "device":
		if !st.Device {
			m.setNotice("device flow is only available for "+strings.Join(deviceProviderNames(statuses), ", "), true)
			return m, nil
		}
		var cmd tea.Cmd
		m.modal, cmd = startDeviceModal(authsvc, provider, false)
		return m, cmd
	default:
		switch {
		case st.OAuth:
			var cmd tea.Cmd
			m.modal, cmd = startOAuthModal(authsvc, provider, false)
			return m, cmd
		case st.APIKey:
			m.modal = newAPIKeyModal(provider, authsvc, m.th, false)
			return m, nil
		default:
			m.setNotice(provider+" has no supported login method", true)
			return m, nil
		}
	}
}

// credentialProviderNames is the ordered list of non-builtin provider names,
// the ones /auth can log into.
func credentialProviderNames(statuses []host.ProviderStatus) []string {
	var names []string
	for _, s := range statuses {
		if !s.Builtin {
			names = append(names, s.Name)
		}
	}
	return names
}

// deviceProviderNames lists the providers that support the RFC 8628 device flow.
func deviceProviderNames(statuses []host.ProviderStatus) []string {
	var names []string
	for _, s := range statuses {
		if s.Device {
			names = append(names, s.Name)
		}
	}
	return names
}

func findStatus(statuses []host.ProviderStatus, name string) (host.ProviderStatus, bool) {
	for _, s := range statuses {
		if s.Name == name {
			return s, true
		}
	}
	return host.ProviderStatus{}, false
}

// authWaitModal is the centered dialog shown while an OAuth or device flow is
// in flight: it carries the login link (in case the browser did not open) or
// the device code, and esc cancels the flow.
type authWaitModal struct {
	provider    string
	url         string // authorize URL once the flow has started
	userCode    string // device flow
	verifyURI   string // device flow
	selectAfter bool
	ctx         context.Context
	cancel      context.CancelFunc
}

// startOAuthModal opens the wait dialog and begins the browser flow through the
// host auth service.
func startOAuthModal(authsvc host.Auth, provider string, selectAfter bool) (modal, tea.Cmd) {
	ctx, cancel := context.WithCancel(context.Background())
	wm := &authWaitModal{provider: provider, selectAfter: selectAfter, ctx: ctx, cancel: cancel}
	return wm, func() tea.Msg {
		login, err := authsvc.BeginOAuth(ctx, provider)
		if err != nil {
			cancel()
			return authDoneMsg{provider: provider, err: err, selectAfter: selectAfter}
		}
		return authStartedMsg{provider: provider, login: login}
	}
}

// startDeviceModal opens the wait dialog and begins the device flow through the
// host auth service.
func startDeviceModal(authsvc host.Auth, provider string, selectAfter bool) (modal, tea.Cmd) {
	ctx, cancel := context.WithCancel(context.Background())
	wm := &authWaitModal{provider: provider, selectAfter: selectAfter, ctx: ctx, cancel: cancel}
	return wm, func() tea.Msg {
		login, err := authsvc.BeginDevice(ctx, provider)
		if err != nil {
			cancel()
			return authDoneMsg{provider: provider, err: err, selectAfter: selectAfter}
		}
		return authDeviceMsg{provider: provider, login: login}
	}
}

func (m *authWaitModal) update(msg tea.KeyMsg) (modal, tea.Cmd) {
	if msg.String() == "esc" {
		m.cancel() // the in-flight Wait/Poll returns promptly with ctx error
		return nil, nil
	}
	return m, nil
}

func (m *authWaitModal) view(width int, th theme.Theme) string {
	st := th.S()
	inner := max(1, ui.InnerWidth(width))
	body := st.Muted.Render("starting login flow…")
	switch {
	case m.userCode != "":
		body = wrapToWidth(st.Text.Render("Open "+m.verifyURI+" on any device and enter code:"), inner) +
			"\n" + st.Warning.Bold(true).Render(m.userCode) +
			"\n" + st.Muted.Render("waiting for authorization…")
	case m.url != "":
		body = st.Text.Render("Complete the login in your browser.") +
			"\n" + st.Muted.Render("If it did not open, visit:") +
			"\n" + wrapToWidth(st.Accent.Render(m.url), inner) +
			"\n" + st.Muted.Render("waiting for the callback…")
	}
	return ui.Dialog(th, ui.DialogOpts{
		Title: "Logging in to " + m.provider,
		Hint:  "esc cancel",
		Width: width,
	}, body)
}

// applyAuthMsg handles the async auth messages; returns false if msg is not
// auth-related.
func (m *Model) applyAuthMsg(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case authStartedMsg:
		ctx := context.Background()
		selectAfter := false
		if wm, ok := m.modal.(*authWaitModal); ok && wm.provider == msg.provider {
			wm.url = msg.login.URL
			ctx, selectAfter = wm.ctx, wm.selectAfter
		}
		login, provider := msg.login, msg.provider
		return func() tea.Msg {
			outcome, err := login.Wait(ctx)
			return authDoneMsg{provider: provider, message: outcome, err: err, selectAfter: selectAfter}
		}, true

	case authDeviceMsg:
		ctx := context.Background()
		selectAfter := false
		if wm, ok := m.modal.(*authWaitModal); ok && wm.provider == msg.provider {
			wm.userCode = msg.login.UserCode
			wm.verifyURI = msg.login.VerificationURI
			ctx, selectAfter = wm.ctx, wm.selectAfter
		}
		login, provider := msg.login, msg.provider
		return func() tea.Msg {
			outcome, err := login.Poll(ctx)
			return authDoneMsg{provider: provider, message: outcome, err: err, selectAfter: selectAfter}
		}, true

	case authDoneMsg:
		if wm, ok := m.modal.(*authWaitModal); ok && wm.provider == msg.provider {
			wm.cancel()
			m.modal = nil
		}
		switch {
		case errors.Is(msg.err, context.Canceled):
			m.setNotice(msg.provider+" login canceled", false)
		case msg.err != nil:
			m.setNotice(msg.provider+" login failed: "+msg.err.Error(), true)
		default:
			m.setNotice(msg.message, false)
			if msg.selectAfter {
				ops, name := m.ops, msg.provider
				return func() tea.Msg {
					ops <- protocol.SelectModel{Provider: name}
					return nil
				}, true
			}
			m.setNotice(msg.message+" — /provider "+msg.provider+" to use it", false)
		}
		return nil, true
	}
	return nil, false
}

// apiKeyModal is a masked input for pasting an API key, stored on enter through
// the host auth service.
type apiKeyModal struct {
	provider    string
	auth        host.Auth
	input       textinput.Model
	th          theme.Theme
	selectAfter bool
}

func newAPIKeyModal(provider string, authsvc host.Auth, th theme.Theme, selectAfter bool) *apiKeyModal {
	in := textinput.New()
	in.Placeholder = "paste key"
	in.EchoMode = textinput.EchoPassword
	in.Prompt = "> "
	in.Focus()
	return &apiKeyModal{provider: provider, auth: authsvc, input: in, th: th, selectAfter: selectAfter}
}

func (m *apiKeyModal) update(msg tea.KeyMsg) (modal, tea.Cmd) {
	switch msg.String() {
	case "esc":
		return nil, nil
	case "enter":
		// An empty submit is ignored without touching the auth service.
		key := strings.TrimSpace(m.input.Value())
		if key == "" {
			return nil, nil
		}
		provider, authsvc, selectAfter := m.provider, m.auth, m.selectAfter
		return nil, func() tea.Msg {
			err := authsvc.SetAPIKey(provider, key)
			return authDoneMsg{provider: provider, message: "Stored " + provider + " API key", err: err, selectAfter: selectAfter}
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *apiKeyModal) view(width int, th theme.Theme) string {
	m.input.Width = max(1, ui.InnerWidth(width)-2)
	return ui.Dialog(th, ui.DialogOpts{
		Title: "Enter " + m.provider + " API key",
		Hint:  "enter save · esc cancel (input is hidden)",
		Width: width,
	}, m.input.View())
}
