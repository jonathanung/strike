package tui

import (
	"context"
	"errors"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/jonathanung/strike-cli/internal/auth"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

// Async messages for the auth flows. OAuth runs in tea.Cmd goroutines so
// the event loop stays responsive while the user is in the browser; the
// centered authWaitModal shows progress and the login link meanwhile.

type authStartedMsg struct {
	provider string
	pending  *auth.PendingLogin
}

type authDeviceMsg struct {
	provider string
	flow     auth.DeviceConfig
	code     *auth.DeviceCode
}

type authDoneMsg struct {
	provider    string
	message     string
	err         error
	selectAfter bool // switch to the provider once login succeeds
}

const authProviders = "anthropic|openai|xai"

// handleAuth dispatches "/auth ..." commands. Bare /auth is an alias for
// bare /provider: the same centered picker with auth status, where
// selecting an unauthenticated provider starts its login.
func (m Model) handleAuth(args []string) (tea.Model, tea.Cmd) {
	m.composer.Reset()
	if len(args) == 0 {
		m.modal = newProviderModal(m.authStore, m.providerName, m.ops, m.th)
		return m, nil
	}
	if args[0] == "status" {
		var parts []string
		for _, p := range []string{"anthropic", "openai", "xai"} {
			parts = append(parts, p+": "+auth.Describe(p, m.authStore))
		}
		m.setNotice(strings.Join(parts, " · "), false)
		return m, nil
	}
	if args[0] == "logout" {
		if len(args) < 2 {
			m.setNotice("usage: /auth logout <"+authProviders+">", true)
			return m, nil
		}
		if err := m.authStore.Delete(args[1]); err != nil {
			m.setNotice("logout failed: "+err.Error(), true)
			return m, nil
		}
		m.setNotice("logged out of "+args[1], false)
		return m, nil
	}

	provider := args[0]
	method := ""
	if len(args) > 1 {
		method = args[1]
	}
	switch provider {
	case "anthropic":
		m.modal = newAPIKeyModal(provider, m.authStore, m.th, false)
		return m, nil
	case "openai", "xai":
		switch method {
		case "key":
			m.modal = newAPIKeyModal(provider, m.authStore, m.th, false)
			return m, nil
		case "device":
			if provider != "xai" {
				m.setNotice("device flow is only available for xai", true)
				return m, nil
			}
			var cmd tea.Cmd
			m.modal, cmd = startDeviceModal(provider, false)
			return m, cmd
		default:
			var cmd tea.Cmd
			m.modal, cmd = startOAuthModal(provider, false)
			return m, cmd
		}
	default:
		m.setNotice("unknown provider "+provider+" — usage: /auth <"+authProviders+"> [key|device]", true)
		return m, nil
	}
}

// authWaitModal is the centered dialog shown while an OAuth or device flow
// is in flight: it carries the login link (in case the browser didn't
// open) or the device code, and esc cancels the flow.
type authWaitModal struct {
	provider    string
	url         string // authorize URL once the flow has started
	userCode    string // device flow
	verifyURI   string // device flow
	selectAfter bool
	ctx         context.Context
	cancel      context.CancelFunc
}

// startOAuthModal opens the wait dialog and begins the browser flow.
func startOAuthModal(provider string, selectAfter bool) (modal, tea.Cmd) {
	ctx, cancel := context.WithCancel(context.Background())
	wm := &authWaitModal{provider: provider, selectAfter: selectAfter, ctx: ctx, cancel: cancel}
	return wm, func() tea.Msg {
		flow := auth.OpenAIFlow()
		if provider == "xai" {
			flow = auth.XAIFlow()
		}
		pending, err := flow.Begin()
		if err != nil {
			cancel()
			return authDoneMsg{provider: provider, err: err, selectAfter: selectAfter}
		}
		return authStartedMsg{provider: provider, pending: pending}
	}
}

// startDeviceModal opens the wait dialog and begins the device flow.
func startDeviceModal(provider string, selectAfter bool) (modal, tea.Cmd) {
	ctx, cancel := context.WithCancel(context.Background())
	wm := &authWaitModal{provider: provider, selectAfter: selectAfter, ctx: ctx, cancel: cancel}
	return wm, func() tea.Msg {
		flow := auth.XAIDeviceFlow()
		code, err := flow.RequestCode(ctx)
		if err != nil {
			cancel()
			return authDoneMsg{provider: provider, err: err, selectAfter: selectAfter}
		}
		return authDeviceMsg{provider: provider, flow: flow, code: code}
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
	title := lipgloss.NewStyle().Foreground(th.Accent).Bold(true).
		Render("Logging in to " + m.provider)
	muted := lipgloss.NewStyle().Foreground(th.TextMuted)
	body := muted.Render("starting login flow…")
	switch {
	case m.userCode != "":
		body = lipgloss.NewStyle().Foreground(th.Text).Render("Open "+m.verifyURI+" on any device\nand enter code: ") +
			lipgloss.NewStyle().Foreground(th.Warning).Bold(true).Render(m.userCode) +
			"\n" + muted.Render("waiting for authorization…")
	case m.url != "":
		body = lipgloss.NewStyle().Foreground(th.Text).Render("Complete the login in your browser.") +
			"\n" + muted.Render("If it did not open, visit:") +
			"\n" + lipgloss.NewStyle().Foreground(th.Accent).Width(width-4).Render(m.url) +
			"\n" + muted.Render("waiting for the callback…")
	}
	hint := muted.Render("esc cancel")
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(th.BorderFocus).
		Padding(0, 1).
		Width(width)
	return box.Render(title + "\n\n" + body + "\n\n" + hint)
}

// applyAuthMsg handles the async auth messages; returns false if msg is not
// auth-related.
func (m *Model) applyAuthMsg(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case authStartedMsg:
		ctx := context.Background()
		selectAfter := false
		if wm, ok := m.modal.(*authWaitModal); ok && wm.provider == msg.provider {
			wm.url = msg.pending.URL
			ctx, selectAfter = wm.ctx, wm.selectAfter
		}
		store := m.authStore
		return func() tea.Msg {
			tokens, err := msg.pending.Wait(ctx)
			if err != nil {
				return authDoneMsg{provider: msg.provider, err: err, selectAfter: selectAfter}
			}
			outcome, err := auth.CompleteLogin(context.Background(), store, msg.provider, tokens)
			return authDoneMsg{provider: msg.provider, message: outcome, err: err, selectAfter: selectAfter}
		}, true

	case authDeviceMsg:
		ctx := context.Background()
		selectAfter := false
		if wm, ok := m.modal.(*authWaitModal); ok && wm.provider == msg.provider {
			wm.userCode = msg.code.UserCode
			wm.verifyURI = msg.code.VerificationURI
			ctx, selectAfter = wm.ctx, wm.selectAfter
		}
		store := m.authStore
		return func() tea.Msg {
			tokens, err := msg.flow.Poll(ctx, msg.code)
			if err != nil {
				return authDoneMsg{provider: msg.provider, err: err, selectAfter: selectAfter}
			}
			outcome, err := auth.CompleteLogin(context.Background(), store, msg.provider, tokens)
			return authDoneMsg{provider: msg.provider, message: outcome, err: err, selectAfter: selectAfter}
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

// apiKeyModal is a masked input for pasting an API key, stored on enter.
type apiKeyModal struct {
	provider    string
	store       *auth.Store
	input       textinput.Model
	th          theme.Theme
	selectAfter bool
}

func newAPIKeyModal(provider string, store *auth.Store, th theme.Theme, selectAfter bool) *apiKeyModal {
	in := textinput.New()
	in.Placeholder = "paste key"
	in.EchoMode = textinput.EchoPassword
	in.Prompt = "> "
	in.Focus()
	return &apiKeyModal{provider: provider, store: store, input: in, th: th, selectAfter: selectAfter}
}

func (m *apiKeyModal) update(msg tea.KeyMsg) (modal, tea.Cmd) {
	switch msg.String() {
	case "esc":
		return nil, nil
	case "enter":
		key := strings.TrimSpace(m.input.Value())
		if key == "" {
			return nil, nil
		}
		provider, store, selectAfter := m.provider, m.store, m.selectAfter
		return nil, func() tea.Msg {
			err := store.Set(provider, auth.Credential{Type: auth.TypeAPIKey, APIKey: key})
			return authDoneMsg{provider: provider, message: "Stored " + provider + " API key", err: err, selectAfter: selectAfter}
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *apiKeyModal) view(width int, th theme.Theme) string {
	title := lipgloss.NewStyle().Foreground(th.Accent).Bold(true).
		Render("Enter " + m.provider + " API key")
	hint := lipgloss.NewStyle().Foreground(th.TextMuted).
		Render("enter save · esc cancel (input is hidden)")
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(th.Accent).
		Padding(0, 1).
		Width(width)
	return box.Render(title + "\n" + m.input.View() + "\n" + hint)
}
