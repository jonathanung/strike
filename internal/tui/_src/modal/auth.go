package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

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

// authPasteErrMsg reports a failed CompleteWithPaste without closing the wait
// modal. message is user-facing only — never a token or secret.
type authPasteErrMsg struct {
	provider string
	message  string
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
		m.setNotice(dotJoin(m.th, parts...), false)
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
	if provider == "gemini" {
		provider = "google"
	}
	st, ok := findStatus(statuses, provider)
	if !ok || st.Builtin {
		m.setNotice("unknown provider "+provider+" — usage: /auth <"+strings.Join(names, "|")+"> [key|device|oauth]", true)
		return m, nil
	}
	method := ""
	if len(args) > 1 {
		method = args[1]
	}
	var cmd tea.Cmd
	m.modal, cmd = startLogin(authsvc, m.th, provider, method, false)
	return m, cmd
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

func findStatus(statuses []host.ProviderStatus, name string) (host.ProviderStatus, bool) {
	for _, s := range statuses {
		if s.Name == name {
			return s, true
		}
	}
	return host.ProviderStatus{}, false
}

// authMethodKind is one selectable login path for a provider.
type authMethodKind int

const (
	authMethodOAuth authMethodKind = iota
	authMethodDevice
	authMethodAPIKey
)

type authMethodItem struct {
	kind  authMethodKind
	label string
}

// authMethodsFor lists the login methods a provider status advertises, with
// provider-specific labels for browser OAuth where it matters.
func authMethodsFor(st host.ProviderStatus) []authMethodItem {
	var items []authMethodItem
	if st.OAuth {
		label := "Browser sign-in"
		switch st.Name {
		case "openai":
			label = "ChatGPT subscription (browser)"
		case "xai":
			label = "Grok subscription (browser)"
		}
		items = append(items, authMethodItem{authMethodOAuth, label})
	}
	if st.Device {
		items = append(items, authMethodItem{authMethodDevice, "Device code"})
	}
	if st.APIKey {
		items = append(items, authMethodItem{authMethodAPIKey, "API key"})
	}
	return items
}

// startLogin begins provider login. method "" = auto (chooser if >1, else
// direct). Explicit "key"|"device"|"oauth" skips the chooser. selectAfter
// switches provider on success.
func startLogin(authsvc host.Auth, th theme.Theme, provider, method string, selectAfter bool) (modal, tea.Cmd) {
	st, ok := findStatus(authsvc.Statuses(), provider)
	if !ok {
		return nil, authDoneErrCmd(provider, fmt.Errorf("unknown provider %s", provider), selectAfter)
	}
	switch method {
	case "key":
		if !st.APIKey {
			return nil, authDoneErrCmd(provider, fmt.Errorf("%s does not accept an API key", provider), selectAfter)
		}
		return newAPIKeyModal(provider, authsvc, th, selectAfter), nil
	case "device":
		if !st.Device {
			return nil, authDoneErrCmd(provider, fmt.Errorf("device flow is not available for %s", provider), selectAfter)
		}
		return startDeviceModal(authsvc, provider, selectAfter)
	case "oauth":
		if !st.OAuth {
			return nil, authDoneErrCmd(provider, fmt.Errorf("%s does not support OAuth", provider), selectAfter)
		}
		return startOAuthModal(authsvc, provider, selectAfter, th)
	case "":
		methods := authMethodsFor(st)
		switch len(methods) {
		case 0:
			return nil, authDoneErrCmd(provider, fmt.Errorf("%s has no supported login method", provider), selectAfter)
		case 1:
			return startAuthMethod(authsvc, th, provider, methods[0], selectAfter)
		default:
			return &authMethodModal{
				provider:    provider,
				items:       methods,
				auth:        authsvc,
				th:          th,
				selectAfter: selectAfter,
			}, nil
		}
	default:
		return nil, authDoneErrCmd(provider, fmt.Errorf("unknown login method %q", method), selectAfter)
	}
}

func authDoneErrCmd(provider string, err error, selectAfter bool) tea.Cmd {
	return func() tea.Msg {
		return authDoneMsg{provider: provider, err: err, selectAfter: selectAfter}
	}
}

func startAuthMethod(authsvc host.Auth, th theme.Theme, provider string, item authMethodItem, selectAfter bool) (modal, tea.Cmd) {
	switch item.kind {
	case authMethodOAuth:
		return startOAuthModal(authsvc, provider, selectAfter, th)
	case authMethodDevice:
		return startDeviceModal(authsvc, provider, selectAfter)
	case authMethodAPIKey:
		return newAPIKeyModal(provider, authsvc, th, selectAfter), nil
	default:
		return nil, nil
	}
}

// authMethodModal is the multi-method chooser (A1) when a provider offers more
// than one login path.
type authMethodModal struct {
	provider    string
	items       []authMethodItem
	cursor      int
	auth        host.Auth
	th          theme.Theme
	selectAfter bool
}

func (m *authMethodModal) update(msg tea.KeyMsg) (modal, tea.Cmd) {
	if len(m.items) == 0 {
		if isEscape(msg) {
			return nil, nil
		}
		return m, nil
	}
	if isEscape(msg) {
		return nil, nil
	}
	switch msg.String() {
	case "up", "k":
		m.cursor = (m.cursor + len(m.items) - 1) % len(m.items)
	case "down", "j":
		m.cursor = (m.cursor + 1) % len(m.items)
	case "enter":
		return startAuthMethod(m.auth, m.th, m.provider, m.items[m.cursor], m.selectAfter)
	}
	return m, nil
}

func (m *authMethodModal) view(width int, th theme.Theme) string {
	items := make([]ui.ListItem, len(m.items))
	for i, it := range m.items {
		items[i] = ui.ListItem{Label: it.label}
	}
	body := ui.List(th, ui.ListOpts{
		Items:   items,
		Cursor:  m.cursor,
		Width:   max(1, ui.PanelInnerWidth(th, width)),
		Visible: max(1, len(m.items)),
		Empty:   "no login methods",
	})
	return ui.Dialog(th, ui.DialogOpts{
		Title: "Sign in to " + m.provider,
		Hint:  dotJoin(th, "↑/↓ move", "enter select", "esc cancel"),
		Width: width,
	}, body)
}

// authWaitModal is the centered dialog shown while an OAuth or device flow is
// in flight: it carries the login link (in case the browser did not open) or
// the device code, and esc cancels the flow. OAuth waits also accept a pasted
// callback URL or authorization code.
type authWaitModal struct {
	provider      string
	url           string // authorize URL once the flow has started
	userCode      string // device flow
	verifyURI     string // device flow
	selectAfter   bool
	copyRequested bool
	copyOSC       string // one-shot OSC52 sequence prepended to next view
	oauth         *host.OAuthLogin
	paste         textinput.Model
	pasteErr      string // never contains a secret
	ctx           context.Context
	cancel        context.CancelFunc
}

// startOAuthModal opens the wait dialog and begins the browser flow through the
// host auth service.
func startOAuthModal(authsvc host.Auth, provider string, selectAfter bool, th theme.Theme) (modal, tea.Cmd) {
	ctx, cancel := context.WithCancel(context.Background())
	in := newTextInput(th, "paste here")
	in.Focus()
	wm := &authWaitModal{
		provider:    provider,
		selectAfter: selectAfter,
		paste:       in,
		ctx:         ctx,
		cancel:      cancel,
	}
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
	if isEscape(msg) {
		m.cancel() // the in-flight Wait/Poll returns promptly with ctx error
		return nil, nil
	}
	switch msg.String() {
	case "enter":
		if m.oauth == nil {
			return m, nil
		}
		raw := strings.TrimSpace(m.paste.Value())
		if raw == "" {
			return m, nil
		}
		oauth, provider := m.oauth, m.provider
		return m, func() tea.Msg {
			err := oauth.CompleteWithPaste(raw)
			if err != nil {
				return authPasteErrMsg{provider: provider, message: err.Error()}
			}
			// Wait completes on the host side and delivers authDoneMsg.
			return nil
		}
	case "ctrl+y":
		// ctrl+y (yank) copies the authorize URL without stealing printable
		// paste input (bare codes may start with "c").
		if m.url != "" {
			m.copyRequested = true
			m.copyOSC = ansi.SetSystemClipboard(m.url)
			return m, nil
		}
	}
	// Forward remaining keys to the paste field on OAuth waits.
	if m.oauth != nil || m.url != "" {
		var cmd tea.Cmd
		m.paste, cmd = m.paste.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *authWaitModal) view(width int, th theme.Theme) string {
	th = th.Resolve()
	st := th.S()
	inner := max(1, ui.PanelInnerWidth(th, width))
	body := st.Muted.Render("starting login flow…")
	hint := "esc cancel"
	switch {
	case m.userCode != "":
		body = wrapToWidth(st.Text.Render("Open "+m.verifyURI+" on any device and enter code:"), inner) +
			"\n" + st.WarningStrong.Render(m.userCode) +
			"\n" + st.Muted.Render("waiting for authorization…")
	case m.url != "":
		status := "waiting for the callback…"
		if m.copyRequested {
			status = "copy requested"
		}
		cursorWidth := max(1, ansi.StringWidth(m.paste.Cursor.View()))
		m.paste.Width = max(1, inner-ansi.StringWidth(m.paste.Prompt)-cursorWidth)
		m.paste.SetValue(m.paste.Value())
		body = st.Text.Render("Complete the login in your browser.") +
			"\n" + st.Muted.Render("If it did not open, visit:") +
			"\n" + renderAuthURL(m.url, inner, th) +
			"\n" + st.Muted.Render(oauthPasteInstruction(m.provider)) +
			"\n" + m.paste.View()
		if m.pasteErr != "" {
			body += "\n" + st.Error.Render(m.pasteErr)
		}
		body += "\n" + st.Muted.Render(status)
		hint = dotJoin(th, "enter submit", "ctrl+y copy", "esc cancel")
	}
	// OSC52 is emitted from Model.View after OverlayCenter/Canvas so ansi.Cut
	// cannot strip the clipboard sequence from the dialog substring.
	return ui.Dialog(th, ui.DialogOpts{
		Title: "Logging in to " + m.provider,
		Hint:  hint,
		Width: width,
	}, body)
}

// TakeCopyOSC returns and clears a one-shot OSC52 clipboard sequence staged by
// the copy key. Model.View prepends it to the final frame.
func (m *authWaitModal) TakeCopyOSC() string {
	if m == nil || m.copyOSC == "" {
		return ""
	}
	osc := m.copyOSC
	m.copyOSC = ""
	return osc
}

// oauthPasteInstruction is the muted guidance line above the paste field.
func oauthPasteInstruction(provider string) string {
	switch provider {
	case "openai":
		return "Paste the callback URL from your browser address bar"
	case "xai":
		return "Paste the code from the Grok login page"
	default:
		return "Paste the authorization code or callback URL"
	}
}

// renderAuthURL wraps the unstyled URL before making each visual line a link,
// so every part opens the complete authorization URL.
func renderAuthURL(url string, width int, th theme.Theme) string {
	wrapped := ansi.Hardwrap(url, max(1, width), false)
	st := th.S()
	lines := strings.Split(wrapped, "\n")
	for i, line := range lines {
		lines[i] = ansi.SetHyperlink(url) + st.Accent.Render(line) + ansi.ResetHyperlink()
	}
	return strings.Join(lines, "\n")
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
			wm.oauth = msg.login
			wm.paste.Focus()
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

	case authPasteErrMsg:
		if wm, ok := m.modal.(*authWaitModal); ok && wm.provider == msg.provider {
			wm.pasteErr = msg.message
		}
		return nil, true

	case authDoneMsg:
		var promote tea.Cmd
		if wm, ok := m.modal.(*authWaitModal); ok && wm.provider == msg.provider {
			wm.cancel()
			m.modal = nil
			promote = m.afterModalClosed()
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
				return tea.Batch(promote, func() tea.Msg {
					ops <- protocol.SelectModel{Provider: name}
					return nil
				}), true
			}
			m.setNotice(msg.message+" — /provider "+msg.provider+" to use it", false)
		}
		return promote, true
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
	in := newTextInput(th, "paste key")
	in.EchoMode = textinput.EchoPassword
	in.Focus()
	return &apiKeyModal{provider: provider, auth: authsvc, input: in, th: th, selectAfter: selectAfter}
}

func (m *apiKeyModal) update(msg tea.KeyMsg) (modal, tea.Cmd) {
	if isEscape(msg) {
		return nil, nil
	}
	switch msg.String() {
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
	th = th.Resolve()
	st := th.S()
	inner := ui.PanelInnerWidth(th, width)
	cursorWidth := max(1, ansi.StringWidth(m.input.Cursor.View()))
	m.input.Width = max(1, inner-ansi.StringWidth(m.input.Prompt)-cursorWidth)
	m.input.SetValue(m.input.Value())
	body := m.input.View()
	if guide := apiKeyGuide(m.provider, th); guide != "" {
		body = st.Muted.Render(guide) + "\n" + body
	}
	return ui.Dialog(th, ui.DialogOpts{
		Title: apiKeyModalTitle(m.provider),
		Hint:  dotJoin(th, "enter save", "esc cancel (input is hidden)"),
		Width: width,
	}, body)
}

// apiKeyModalTitle is the dialog title when pasting a provider API key.
func apiKeyModalTitle(provider string) string {
	switch provider {
	case "google":
		return "Enter Google AI Studio API key"
	default:
		return "Enter " + provider + " API key"
	}
}

// apiKeyGuide is optional muted copy above the key field (how to get a key).
func apiKeyGuide(provider string, th theme.Theme) string {
	switch provider {
	case "google":
		return dotJoin(th,
			"Google AI Studio key (aistudio.google.com/apikey)",
			"env GEMINI_API_KEY or GOOGLE_API_KEY",
			"provider id google (alias gemini)",
		)
	default:
		return ""
	}
}
