package tui

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

// TestOAuthLoginCompletesAndSwitchesProviderWhenSelectAfter drives the full
// decoupled OAuth flow through the host service: picking an unauthenticated
// multi-method provider opens the method chooser, selecting browser OAuth
// opens the wait modal, the started/done messages carry only an outcome
// string, and success switches to the provider (selectAfter).
func TestOAuthLoginCompletesAndSwitchesProviderWhenSelectAfter(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	picker := newProviderModal(m.services, "", m.ops, m.th)
	picker.cursor = 1 // openai: unauthenticated, OAuth+APIKey
	m.modal = picker

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if cmd != nil {
		t.Fatalf("opening method chooser returned unexpected command %T", cmd)
	}
	chooser, ok := m.modal.(*authMethodModal)
	if !ok {
		t.Fatalf("selecting multi-method provider did not open method chooser: %T", m.modal)
	}
	if chooser.provider != "openai" || len(chooser.items) < 1 || chooser.items[0].kind != authMethodOAuth {
		t.Fatalf("chooser = %#v, want openai with OAuth first", chooser)
	}

	// Enter on the first method (ChatGPT subscription / browser OAuth).
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if _, ok := m.modal.(*authWaitModal); !ok {
		t.Fatalf("selecting browser OAuth did not open the wait modal: %T", m.modal)
	}

	started := runAppCmd(t, cmd)
	if _, ok := started.(authStartedMsg); !ok {
		t.Fatalf("begin-oauth command produced %T, want authStartedMsg", started)
	}
	updated, cmd = m.Update(started)
	m = updated.(Model)
	if wm, ok := m.modal.(*authWaitModal); !ok || wm.url == "" || wm.oauth == nil {
		t.Fatalf("wait modal did not receive the login URL/oauth handle: %#v", m.modal)
	}

	done := runAppCmd(t, cmd)
	if _, ok := done.(authDoneMsg); !ok {
		t.Fatalf("wait command produced %T, want authDoneMsg", done)
	}
	updated, cmd = m.Update(done)
	m = updated.(Model)
	if m.modal != nil {
		t.Errorf("successful login left a modal open: %T", m.modal)
	}
	if !strings.Contains(m.notice, "Signed in to openai") {
		t.Errorf("login outcome notice = %q, want the host outcome string", m.notice)
	}

	runAppCmd(t, cmd)
	if got := receiveAppOp(t, ops); got != (protocol.SelectModel{Provider: "openai"}) {
		t.Errorf("selectAfter did not switch provider: op = %#v", got)
	}
}

func TestAPIKeyModalGoogleGuide(t *testing.T) {
	// Canonical provider id is google; guide mentions the gemini alias.
	if got := apiKeyModalTitle("google"); got != "Enter Google AI Studio API key" {
		t.Errorf("title = %q", got)
	}
	if got := apiKeyModalTitle("anthropic"); got != "Enter anthropic API key" {
		t.Errorf("title = %q", got)
	}
	th := theme.Default()
	guide := apiKeyGuide("google", th)
	for _, want := range []string{"Google AI Studio", "aistudio.google.com", "GEMINI_API_KEY", "GOOGLE_API_KEY", "google", "alias gemini"} {
		if !strings.Contains(guide, want) {
			t.Errorf("guide missing %q: %q", want, guide)
		}
	}
	if apiKeyGuide("anthropic", th) != "" {
		t.Errorf("anthropic guide should be empty")
	}
	m, _ := newAppTestModel(nil, nil)
	modal := newAPIKeyModal("google", m.services.Auth, m.th, false)
	view := ansi.Strip(modal.view(60, m.th))
	if !strings.Contains(view, "Google AI Studio") {
		t.Errorf("view missing Google AI Studio guidance:\n%s", view)
	}
	if !strings.Contains(view, "alias gemini") {
		t.Errorf("view missing gemini alias mention:\n%s", view)
	}
}

func TestAPIKeyModalStoresKeyThroughAuthServiceAndIgnoresEmptySubmit(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	fake := m.services.Auth.(*fakeAuth)

	modal := newAPIKeyModal("anthropic", m.services.Auth, m.th, false)
	modal.input.SetValue("sk-test-123")
	next, cmd := modal.update(tea.KeyMsg{Type: tea.KeyEnter})
	if next != nil {
		t.Fatal("enter with a key did not close the modal")
	}
	if _, ok := runAppCmd(t, cmd).(authDoneMsg); !ok {
		t.Fatal("storing a key did not emit authDoneMsg")
	}
	if len(fake.setCalls) != 1 || fake.setCalls[0] != (recordedAPIKey{provider: "anthropic", key: "sk-test-123"}) {
		t.Errorf("SetAPIKey calls = %+v, want one anthropic/sk-test-123 call", fake.setCalls)
	}

	empty := newAPIKeyModal("anthropic", m.services.Auth, m.th, false)
	empty.input.SetValue("   ")
	next, cmd = empty.update(tea.KeyMsg{Type: tea.KeyEnter})
	if next != nil || cmd != nil {
		t.Fatal("empty submit should be ignored without closing or emitting a command")
	}
	if len(fake.setCalls) != 1 {
		t.Errorf("empty submit reached SetAPIKey: %+v", fake.setCalls)
	}
}

func TestAPIKeyModalMaskedInputFitsDialogForThemeXSGaps(t *testing.T) {
	const width = 40
	for _, tt := range []struct {
		name string
		th   theme.Theme
	}{
		{"default", theme.Default()},
		{"explicit zero", theme.Theme{Spacing: theme.NewSpacing(0, 2, 3, 4)}},
		{"custom XS", theme.Theme{Spacing: theme.NewSpacing(4, 2, 3, 4)}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			modal := newAPIKeyModal("anthropic", nil, tt.th, false)
			modal.input.SetValue("key")
			rendered := modal.view(width, tt.th)
			inner := ui.PanelInnerWidth(tt.th, width)
			inputView := modal.input.View()
			if got := ansi.StringWidth(inputView); got > inner {
				t.Errorf("masked input width = %d, want <= dialog inner width %d", got, inner)
			}
			for i, line := range strings.Split(rendered, "\n") {
				if got := ansi.StringWidth(line); got != width {
					t.Errorf("dialog line %d width = %d, want exact outer width %d: %q", i, got, width, line)
				}
			}
			if strings.Contains(ansi.Strip(inputView), "…") {
				t.Errorf("masked input unexpectedly ellipsized because of prompt geometry: %q", ansi.Strip(inputView))
			}
		})
	}
}

func TestAuthCommandsDataDrivenFromStatuses(t *testing.T) {
	t.Run("logout goes through the auth service", func(t *testing.T) {
		m, _ := newAppTestModel(nil, nil)
		fake := m.services.Auth.(*fakeAuth)
		m.composer.SetValue("/auth logout xai")
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = updated.(Model)
		if len(fake.logoutCalls) != 1 || fake.logoutCalls[0] != "xai" {
			t.Errorf("logout calls = %v, want [xai]", fake.logoutCalls)
		}
		if !strings.Contains(m.notice, "logged out of xai") {
			t.Errorf("logout notice = %q", m.notice)
		}
	})

	t.Run("status reports every credential provider", func(t *testing.T) {
		m, _ := newAppTestModel(nil, nil)
		m.composer.SetValue("/auth status")
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = updated.(Model)
		for _, want := range []string{"anthropic: none", "openai: none", "xai: none"} {
			if !strings.Contains(m.notice, want) {
				t.Errorf("status notice %q missing %q", m.notice, want)
			}
		}
		if strings.Contains(m.notice, "echo") {
			t.Errorf("status notice listed the builtin echo provider: %q", m.notice)
		}
	})

	t.Run("gemini login alias opens google key modal", func(t *testing.T) {
		m, _ := newAppTestModel(nil, nil)
		m.composer.SetValue("/auth gemini key")
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = updated.(Model)
		modal, ok := m.modal.(*apiKeyModal)
		if !ok || modal.provider != "google" {
			t.Fatalf("/auth gemini modal = %#v, want google API key modal", m.modal)
		}
	})

	t.Run("unknown provider usage lists credential providers", func(t *testing.T) {
		m, _ := newAppTestModel(nil, nil)
		m.composer.SetValue("/auth nope")
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = updated.(Model)
		if !m.noticeErr || !strings.Contains(m.notice, "anthropic|openai|xai") {
			t.Errorf("unknown-provider notice = %q (err=%v), want data-driven usage", m.notice, m.noticeErr)
		}
	})
}

func TestRenderAuthURLPreservesCompleteLinkedURLAtEveryWidth(t *testing.T) {
	urls := []struct {
		name string
		url  string
	}{
		{
			name: "OpenAI authorize URL",
			url:  "https://auth.openai.com/authorize?response_type=code&client_id=strike-cli&redirect_uri=http%3A%2F%2Flocalhost%3A1455%2Fcallback&scope=openid%20profile%20offline_access&state=openai-state%2Fwith%2Bpercent&code_challenge=OpenAI_Challenge-0123456789&code_challenge_method=S256&nonce=openai-nonce",
		},
		{
			name: "xAI authorize URL",
			url:  "https://console.x.ai/api/oauth/authorize?response_type=code&client_id=strike-cli&redirect_uri=http%3A%2F%2Flocalhost%3A1455%2Fcallback&scope=openid%20email&state=xai%3Astate%2Fwith%252Fencoding&code_challenge=xAI_Challenge-abcdefghijklmnopqrstuvwxyz0123456789&code_challenge_method=S256&nonce=xai-nonce",
		},
	}
	widths := []struct {
		name  string
		width int
	}{
		{name: "zero width", width: 0},
		{name: "one column", width: 1},
		{name: "two columns", width: 2},
		{name: "narrow inner width", width: 6},
		{name: "normal capped width", width: 80},
	}

	for _, urlCase := range urls {
		for _, widthCase := range widths {
			t.Run(urlCase.name+" at "+widthCase.name, func(t *testing.T) {
				rendered := renderAuthURL(urlCase.url, widthCase.width, theme.Default())
				lines := strings.Split(rendered, "\n")
				wantWidth := max(1, widthCase.width)
				var visible strings.Builder
				for i, line := range lines {
					if got := ansi.StringWidth(ansi.Strip(line)); got > wantWidth {
						t.Errorf("line %d display width = %d, want <= %d: %q", i, got, wantWidth, line)
					}
					opens, resets, linked := osc8Links(line)
					if opens != 1 || resets != 1 {
						t.Errorf("line %d OSC8 open/resets = %d/%d, want 1/1: %q", i, opens, resets, line)
					}
					if len(linked) != 1 || linked[0].target != urlCase.url {
						t.Errorf("line %d OSC8 target = %#v, want complete URL %q", i, linked, urlCase.url)
					}
					visible.WriteString(ansi.Strip(line))
				}
				if got := visible.String(); got != urlCase.url {
					t.Errorf("stripped URL = %q, want complete untruncated URL %q", got, urlCase.url)
				}
			})
		}
	}
}

func TestAuthWaitModalOAuthURLCopyUX(t *testing.T) {
	url := "https://auth.openai.com/authorize?response_type=code&client_id=strike-cli&state=copy%2Fstate&code_challenge=complete-challenge&nonce=complete-nonce"
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.composer.SetValue("leave composer unchanged")
	wm := newAuthWaitModalForTest(url)
	m.modal = wm

	before := wm.view(80, m.th)
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlY})
	m = updated.(Model)
	if got := m.composer.Value(); got != "leave composer unchanged" {
		t.Errorf("composer after modal copy key = %q, want unchanged", got)
	}
	if m.modal != wm {
		t.Errorf("copy key closed or replaced OAuth modal: %T", m.modal)
	}
	if cmd != nil {
		t.Fatalf("OAuth URL copy key returned command %T, want nil (OSC52 is view one-shot)", cmd)
	}
	if !wm.copyRequested {
		t.Error("OAuth URL copy key did not record a copy request")
	}
	if wm.copyOSC == "" {
		t.Fatal("OAuth URL copy key did not stage OSC52 for the next view")
	}

	// OSC52 is prepended to the full app frame (after OverlayCenter/Canvas).
	afterFrame := m.View()
	if wm.copyOSC != "" {
		t.Error("Model.View did not consume one-shot OSC52")
	}
	if reqs := osc52Payloads(afterFrame); len(reqs) != 1 {
		t.Fatalf("app View OSC52 requests = %d, want 1", len(reqs))
	}
	// Modal-only view must not carry OSC52 (it would be stripped by Canvas).
	afterModal := wm.view(80, m.th)
	if reqs := osc52Payloads(afterModal); len(reqs) != 0 {
		t.Errorf("modal.view unexpectedly emitted OSC52: %v", reqs)
	}
	beforeHeight, beforeWidth := dimensions(before)
	afterHeight, afterWidth := dimensions(afterModal)
	if afterHeight != beforeHeight || afterWidth != beforeWidth {
		t.Errorf("copy feedback changed modal dimensions: before %dx%d, after %dx%d", beforeWidth, beforeHeight, afterWidth, afterHeight)
	}
	stripped := ansi.Strip(afterModal)
	if !strings.Contains(strings.ToLower(stripped), "copy requested") {
		t.Errorf("copy feedback missing from view: %q", stripped)
	}
	if strings.Contains(strings.ToLower(stripped), "copied") || strings.Contains(strings.ToLower(stripped), "success") {
		t.Errorf("copy feedback claims completion rather than request: %q", stripped)
	}
	if !strings.Contains(stripped, "ctrl+y copy") {
		t.Errorf("OAuth wait hint missing ctrl+y copy: %q", stripped)
	}
	assertModalURL(t, afterModal, url)

	// Repeated ctrl+y stages a fresh OSC52; printable c/C do not.
	updated, nextCmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlY})
	m = updated.(Model)
	if nextCmd != nil {
		t.Errorf("ctrl+y returned unexpected command %T", nextCmd)
	}
	if wm.copyOSC == "" {
		t.Error("repeated ctrl+y did not stage a fresh OSC52")
	}
	_ = m.View() // consume
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune("c")},
		{Type: tea.KeyRunes, Runes: []rune("C")},
	} {
		updated, nextCmd = m.Update(key)
		m = updated.(Model)
		if nextCmd != nil {
			t.Errorf("%q returned unexpected command %T", key.String(), nextCmd)
		}
		if wm.copyOSC != "" {
			t.Errorf("%q unexpectedly staged OSC52", key.String())
		}
	}

	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(Model)
	if m.modal != nil || cmd != nil {
		t.Errorf("escape after copy = modal %T, command %v; want closed with no command", m.modal, cmd)
	}
}

func TestAuthWaitModalCopyKeyOnlyAppliesToOAuthURL(t *testing.T) {
	t.Run("device flow ctrl+y is ignored", func(t *testing.T) {
		wm := newAuthWaitModalForTest("")
		wm.userCode, wm.verifyURI = "ABCD-EFGH", "https://login.example/device"
		next, cmd := wm.update(tea.KeyMsg{Type: tea.KeyCtrlY})
		if next != wm || cmd != nil || wm.copyRequested {
			t.Errorf("device flow ctrl+y = modal %T command %v requested=%v, want no-op", next, cmd, wm.copyRequested)
		}
	})

	t.Run("OAuth wait without a URL ctrl+y is ignored", func(t *testing.T) {
		wm := newAuthWaitModalForTest("")
		next, cmd := wm.update(tea.KeyMsg{Type: tea.KeyCtrlY})
		if next != wm || cmd != nil || wm.copyRequested {
			t.Errorf("empty OAuth wait ctrl+y = modal %T command %v requested=%v, want no-op", next, cmd, wm.copyRequested)
		}
	})

	t.Run("normal composer c still inserts", func(t *testing.T) {
		m, _ := newAppTestModel(nil, nil)
		m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
		if got := m.composer.Value(); got != "c" {
			t.Errorf("composer c = %q, want c", got)
		}
	})

	t.Run("OAuth paste field accepts leading c", func(t *testing.T) {
		wm := newAuthWaitModalForTest("https://login.example/authorize")
		wm.oauth = host.NewOAuthLogin(wm.url, nil).WithPaste(func(string) error { return nil })
		next, cmd := wm.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
		if next != wm || cmd != nil {
			t.Fatalf("leading c = modal %T cmd %v", next, cmd)
		}
		if wm.copyRequested || wm.copyOSC != "" {
			t.Error("leading c staged a copy instead of typing")
		}
		if got := wm.paste.Value(); got != "c" {
			t.Errorf("paste after c = %q, want c", got)
		}
	})
}

func TestOAuthURLCopyCommandWritesOneExactOSC52Request(t *testing.T) {
	url := "https://console.x.ai/api/oauth/authorize?state=osc52%2Fstate&code_challenge=copy-challenge&nonce=copy-nonce"
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	wm := newAuthWaitModalForTest(url)
	m.modal = wm
	next, cmd := wm.update(tea.KeyMsg{Type: tea.KeyCtrlY})
	if next != wm || cmd != nil {
		t.Fatalf("copy key = modal %T cmd %v, want same modal and nil cmd", next, cmd)
	}

	// Full app View must carry OSC52; modal.view alone must not.
	if n := len(osc52Payloads(wm.view(80, theme.Default()))); n != 0 {
		t.Fatalf("modal.view OSC52 count = %d, want 0 (emitted at Model.View)", n)
	}
	if wm.copyOSC == "" {
		t.Fatal("modal.view consumed OSC52; should leave it for Model.View")
	}
	rendered := m.View()
	requests := osc52Payloads(rendered)
	if len(requests) != 1 {
		t.Fatalf("app View OSC52 requests = %d, want 1: %q", len(requests), rendered)
	}
	payload, err := base64.StdEncoding.DecodeString(requests[0])
	if err != nil {
		t.Fatalf("OSC52 payload is not base64: %q: %v", requests[0], err)
	}
	if got := string(payload); got != url {
		t.Errorf("OSC52 decoded payload = %q, want complete URL %q", got, url)
	}

	// One-shot: a subsequent frame must not re-emit the clipboard sequence.
	if second := osc52Payloads(m.View()); len(second) != 0 {
		t.Errorf("second app View re-emitted OSC52: %v", second)
	}
}

func newAuthWaitModalForTest(url string) *authWaitModal {
	ctx, cancel := context.WithCancel(context.Background())
	in := newTextInput(theme.Default(), "paste here")
	in.Focus()
	return &authWaitModal{provider: "openai", url: url, paste: in, ctx: ctx, cancel: cancel}
}

func TestAuthMethodsForLabels(t *testing.T) {
	cases := []struct {
		st    host.ProviderStatus
		want  []string
		kinds []authMethodKind
	}{
		{
			st:    host.ProviderStatus{Name: "openai", OAuth: true, APIKey: true},
			want:  []string{"ChatGPT subscription (browser)", "API key"},
			kinds: []authMethodKind{authMethodOAuth, authMethodAPIKey},
		},
		{
			st:    host.ProviderStatus{Name: "xai", OAuth: true, Device: true, APIKey: true},
			want:  []string{"Grok subscription (browser)", "Device code", "API key"},
			kinds: []authMethodKind{authMethodOAuth, authMethodDevice, authMethodAPIKey},
		},
		{
			st:    host.ProviderStatus{Name: "anthropic", APIKey: true},
			want:  []string{"API key"},
			kinds: []authMethodKind{authMethodAPIKey},
		},
		{
			st:    host.ProviderStatus{Name: "google", APIKey: true},
			want:  []string{"API key"},
			kinds: []authMethodKind{authMethodAPIKey},
		},
	}
	for _, tc := range cases {
		t.Run(tc.st.Name, func(t *testing.T) {
			items := authMethodsFor(tc.st)
			if len(items) != len(tc.want) {
				t.Fatalf("len = %d, want %d: %+v", len(items), len(tc.want), items)
			}
			for i := range items {
				if items[i].label != tc.want[i] || items[i].kind != tc.kinds[i] {
					t.Errorf("item[%d] = {%v %q}, want {%v %q}", i, items[i].kind, items[i].label, tc.kinds[i], tc.want[i])
				}
			}
		})
	}
}

func TestAuthMethodChooserFromProviderModal(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	picker := newProviderModal(m.services, "", m.ops, m.th)
	picker.cursor = 1 // openai
	m.modal = picker

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if cmd != nil {
		t.Fatalf("chooser open returned cmd %T", cmd)
	}
	chooser, ok := m.modal.(*authMethodModal)
	if !ok {
		t.Fatalf("modal = %T, want authMethodModal", m.modal)
	}
	if len(chooser.items) != 2 {
		t.Fatalf("openai chooser items = %d, want 2", len(chooser.items))
	}
	view := ansi.Strip(chooser.view(60, m.th))
	for _, label := range []string{"ChatGPT subscription (browser)", "API key"} {
		if !strings.Contains(view, label) {
			t.Errorf("chooser view missing %q:\n%s", label, view)
		}
	}

	// Enter on subscription (OAuth) → wait modal.
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if _, ok := m.modal.(*authWaitModal); !ok {
		t.Fatalf("OAuth row → %T, want authWaitModal", m.modal)
	}
	if cmd == nil {
		t.Fatal("OAuth start returned nil command")
	}

	// Re-open chooser and select API key row.
	m.modal = &authMethodModal{
		provider: "openai",
		items:    authMethodsFor(host.ProviderStatus{Name: "openai", OAuth: true, APIKey: true}),
		auth:     m.services.Auth,
		th:       m.th,
	}
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyDown})
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if cmd != nil {
		t.Fatalf("API key row returned unexpected cmd %T", cmd)
	}
	if _, ok := m.modal.(*apiKeyModal); !ok {
		t.Fatalf("API key row → %T, want apiKeyModal", m.modal)
	}
}

func TestAuthSlashCommandsOpenCorrectModals(t *testing.T) {
	t.Run("/auth openai opens chooser", func(t *testing.T) {
		m, _ := newAppTestModel(nil, nil)
		m.composer.SetValue("/auth openai")
		updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = updated.(Model)
		if cmd != nil {
			t.Fatalf("unexpected cmd %T", cmd)
		}
		ch, ok := m.modal.(*authMethodModal)
		if !ok || ch.provider != "openai" || len(ch.items) != 2 {
			t.Fatalf("modal = %#v, want openai chooser with 2 methods", m.modal)
		}
	})

	t.Run("/auth openai key skips chooser", func(t *testing.T) {
		m, _ := newAppTestModel(nil, nil)
		m.composer.SetValue("/auth openai key")
		updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = updated.(Model)
		if cmd != nil {
			t.Fatalf("unexpected cmd %T", cmd)
		}
		ak, ok := m.modal.(*apiKeyModal)
		if !ok || ak.provider != "openai" {
			t.Fatalf("modal = %#v, want openai apiKeyModal", m.modal)
		}
	})

	t.Run("/auth anthropic opens api key directly", func(t *testing.T) {
		m, _ := newAppTestModel(nil, nil)
		m.composer.SetValue("/auth anthropic")
		updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = updated.(Model)
		if cmd != nil {
			t.Fatalf("unexpected cmd %T", cmd)
		}
		ak, ok := m.modal.(*apiKeyModal)
		if !ok || ak.provider != "anthropic" {
			t.Fatalf("modal = %#v, want anthropic apiKeyModal", m.modal)
		}
	})
}

func TestAuthWaitModalPasteSuccess(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	fake := m.services.Auth.(*fakeAuth)
	const secretPaste = "SECRET_PASTE_CODE_should_not_appear"
	fake.oauth = map[string]*host.OAuthLogin{
		"openai": oauthLoginBlockingPaste(
			"https://login.test/openai",
			"Signed in to openai via paste",
			func(raw string) error {
				if raw != secretPaste {
					return errors.New("unexpected paste payload")
				}
				return nil
			},
		),
	}

	wm, beginCmd := startOAuthModal(fake, "openai", false, m.th)
	m.modal = wm
	started := runAppCmd(t, beginCmd)
	updated, waitCmd := m.Update(started)
	m = updated.(Model)
	if waitCmd == nil {
		t.Fatal("authStartedMsg did not schedule Wait")
	}

	waitDone := make(chan tea.Msg, 1)
	go func() { waitDone <- waitCmd() }()

	// Give Wait a moment to block on the paste channel.
	time.Sleep(20 * time.Millisecond)

	modal := m.modal.(*authWaitModal)
	modal.paste.SetValue(secretPaste)
	updated, pasteCmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if pasteCmd == nil {
		t.Fatal("enter with paste returned nil command")
	}
	if msg := runAppCmd(t, pasteCmd); msg != nil {
		t.Fatalf("successful paste cmd returned %T %#v, want nil", msg, msg)
	}

	var done tea.Msg
	select {
	case done = <-waitDone:
	case <-time.After(appCmdTimeout):
		t.Fatal("Wait did not complete after successful paste")
	}
	updated, _ = m.Update(done)
	m = updated.(Model)
	if m.modal != nil {
		t.Errorf("modal still open after paste success: %T", m.modal)
	}
	if !strings.Contains(m.notice, "Signed in to openai via paste") {
		t.Errorf("notice = %q, want paste success outcome", m.notice)
	}
	if strings.Contains(m.notice, secretPaste) {
		t.Errorf("notice leaked paste secret: %q", m.notice)
	}
}

func TestAuthWaitModalPasteErrorStaysOpen(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	fake := m.services.Auth.(*fakeAuth)
	const secretPaste = "LEAKY_SECRET_PASTE_VALUE_xyz"
	fake.oauth = map[string]*host.OAuthLogin{
		"openai": oauthLoginBlockingPaste(
			"https://login.test/openai",
			"should-not-reach",
			func(string) error { return errors.New("state mismatch in OAuth callback (possible CSRF)") },
		),
	}

	wm, beginCmd := startOAuthModal(fake, "openai", false, m.th)
	m.modal = wm
	started := runAppCmd(t, beginCmd)
	updated, waitCmd := m.Update(started)
	m = updated.(Model)

	// Run Wait in the background so it does not block the test; it should stay
	// blocked because paste fails without completing.
	waitDone := make(chan tea.Msg, 1)
	if waitCmd != nil {
		go func() { waitDone <- waitCmd() }()
	}

	modal := m.modal.(*authWaitModal)
	modal.paste.SetValue(secretPaste)
	updated, pasteCmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	pasteMsg := runAppCmd(t, pasteCmd)
	pe, ok := pasteMsg.(authPasteErrMsg)
	if !ok {
		t.Fatalf("paste cmd = %T, want authPasteErrMsg", pasteMsg)
	}
	if strings.Contains(pe.message, secretPaste) {
		t.Errorf("paste error message leaked secret: %q", pe.message)
	}

	updated, _ = m.Update(pasteMsg)
	m = updated.(Model)
	still, ok := m.modal.(*authWaitModal)
	if !ok {
		t.Fatalf("modal closed on paste error: %T", m.modal)
	}
	if still.pasteErr == "" {
		t.Error("pasteErr not set on wait modal")
	}
	if strings.Contains(still.pasteErr, secretPaste) {
		t.Errorf("modal pasteErr leaked secret: %q", still.pasteErr)
	}
	view := ansi.Strip(still.view(80, m.th))
	if !strings.Contains(view, still.pasteErr) {
		t.Errorf("view missing paste error: %q", view)
	}
	// The paste field echoes what the user typed (not password mode). The
	// secret must not appear in the error line, notice, or pasteErr itself.
	if strings.Contains(still.pasteErr, secretPaste) {
		t.Errorf("pasteErr leaked secret: %q", still.pasteErr)
	}
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, still.pasteErr) && strings.Contains(line, secretPaste) {
			t.Errorf("error line leaked paste secret: %q", line)
		}
	}
	if strings.Contains(m.notice, secretPaste) {
		t.Errorf("notice leaked paste secret: %q", m.notice)
	}

	select {
	case msg := <-waitDone:
		t.Fatalf("Wait completed unexpectedly after failed paste: %#v", msg)
	case <-time.After(50 * time.Millisecond):
		// expected: still waiting
	}
	// Cancel so the background Wait exits.
	still.cancel()
	select {
	case <-waitDone:
	case <-time.After(appCmdTimeout):
		t.Fatal("Wait did not return after cancel")
	}
}

func TestAuthWaitModalCtrlYCopiesEvenWithPasteContent(t *testing.T) {
	url := "https://auth.example/authorize?state=copy-gate"
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	wm := newAuthWaitModalForTest(url)
	m.modal = wm

	// Empty paste: ctrl+y stages OSC52; Model.View emits once then is clean.
	next, cmd := wm.update(tea.KeyMsg{Type: tea.KeyCtrlY})
	if next != wm || cmd != nil || wm.copyOSC == "" {
		t.Fatalf("empty paste ctrl+y = modal=%T cmd=%v osc empty=%v", next, cmd, wm.copyOSC == "")
	}
	first := m.View()
	if n := len(osc52Payloads(first)); n != 1 {
		t.Fatalf("first app View OSC52 count = %d, want 1", n)
	}
	if n := len(osc52Payloads(m.View())); n != 0 {
		t.Fatalf("second app View OSC52 count = %d, want 0", n)
	}

	// Non-empty paste: printable c types; ctrl+y still copies.
	wm.paste.SetValue("partial")
	wm.copyRequested = false
	wm.copyOSC = ""
	next, cmd = wm.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	if next != wm || cmd != nil {
		t.Fatalf("non-empty paste c = modal=%T cmd=%v", next, cmd)
	}
	if wm.copyRequested || wm.copyOSC != "" {
		t.Error("c with non-empty paste staged a copy")
	}
	if got := wm.paste.Value(); got != "partialc" {
		t.Errorf("paste after c = %q, want partialc", got)
	}
	next, cmd = wm.update(tea.KeyMsg{Type: tea.KeyCtrlY})
	if next != wm || cmd != nil || wm.copyOSC == "" {
		t.Fatalf("ctrl+y with paste content = modal=%T cmd=%v osc empty=%v", next, cmd, wm.copyOSC == "")
	}
	if !wm.copyRequested {
		t.Error("ctrl+y with paste content did not record copy request")
	}
}

func TestXAIAuthMethodChooserHasThreeRows(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.composer.SetValue("/auth xai")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if cmd != nil {
		t.Fatalf("unexpected cmd %T", cmd)
	}
	ch, ok := m.modal.(*authMethodModal)
	if !ok {
		t.Fatalf("modal = %T, want authMethodModal", m.modal)
	}
	if len(ch.items) != 3 {
		t.Fatalf("xai chooser items = %d, want 3", len(ch.items))
	}
	view := ansi.Strip(ch.view(70, m.th))
	for _, label := range []string{"Grok subscription (browser)", "Device code", "API key"} {
		if !strings.Contains(view, label) {
			t.Errorf("xai chooser missing %q:\n%s", label, view)
		}
	}
}

func TestOAuthPasteInstructionInWaitModalView(t *testing.T) {
	cases := []struct {
		provider string
		want     string
	}{
		{"openai", "Paste the callback URL from your browser address bar"},
		{"xai", "Paste the code from the Grok login page"},
		{"other", "Paste the authorization code or callback URL"},
	}
	for _, tc := range cases {
		t.Run(tc.provider, func(t *testing.T) {
			if got := oauthPasteInstruction(tc.provider); got != tc.want {
				t.Errorf("instruction = %q, want %q", got, tc.want)
			}
			wm := newAuthWaitModalForTest("https://login.test/" + tc.provider)
			wm.provider = tc.provider
			wm.oauth = host.NewOAuthLogin(wm.url, nil).WithPaste(func(string) error { return nil })
			view := ansi.Strip(wm.view(80, theme.Default()))
			if !strings.Contains(view, tc.want) {
				t.Errorf("wait modal view missing instruction %q:\n%s", tc.want, view)
			}
		})
	}
}

func TestSelectAfterThroughChooserAPIKey(t *testing.T) {
	// Provider modal → chooser → API key → success still switches provider.
	m, ops := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	picker := newProviderModal(m.services, "", m.ops, m.th)
	picker.cursor = 1 // openai
	m.modal = picker

	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyEnter}) // chooser
	ch, ok := m.modal.(*authMethodModal)
	if !ok || !ch.selectAfter {
		t.Fatalf("chooser selectAfter = %#v", m.modal)
	}
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyDown}) // API key row
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	ak, ok := m.modal.(*apiKeyModal)
	if !ok || !ak.selectAfter || cmd != nil {
		t.Fatalf("api key modal = %#v cmd=%T", m.modal, cmd)
	}

	ak.input.SetValue("sk-select-after")
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	done := runAppCmd(t, cmd)
	updated, cmd = m.Update(done)
	m = updated.(Model)
	runAppCmd(t, cmd)
	if got := receiveAppOp(t, ops); got != (protocol.SelectModel{Provider: "openai"}) {
		t.Errorf("selectAfter op = %#v", got)
	}
}

type osc8Link struct{ target string }

func osc8Links(line string) (opens, resets int, links []osc8Link) {
	for rest := line; ; {
		start := strings.Index(rest, "\x1b]8;;")
		if start < 0 {
			return opens, resets, links
		}
		rest = rest[start+5:]
		end, terminator := oscTerminator(rest)
		if end < 0 {
			return opens, resets, links
		}
		target := rest[:end]
		rest = rest[end+terminator:]
		if target == "" {
			resets++
			continue
		}
		opens++
		links = append(links, osc8Link{target: target})
	}
}

func oscTerminator(s string) (int, int) {
	if i := strings.IndexByte(s, '\a'); i >= 0 {
		return i, 1
	}
	if i := strings.Index(s, "\x1b\\"); i >= 0 {
		return i, 2
	}
	return -1, 0
}

func assertModalURL(t *testing.T, rendered, url string) {
	t.Helper()
	var linked strings.Builder
	for _, line := range strings.Split(rendered, "\n") {
		opens, resets, links := osc8Links(line)
		if len(links) > 0 && (opens != 1 || resets != 1) {
			t.Errorf("modal URL line OSC8 open/resets = %d/%d, want 1/1: %q", opens, resets, line)
		}
		for _, link := range links {
			if link.target != url {
				t.Errorf("modal OSC8 target = %q, want complete URL %q", link.target, url)
			}
		}
		if len(links) > 0 {
			// The link's visible text is the complete fallback URL fragment; panel
			// padding and borders are outside its OSC8 reset.
			start := strings.Index(line, "\x1b]8;;")
			_, terminator := oscTerminator(line[start+5:])
			afterOpen := line[start+5:]
			end, _ := oscTerminator(afterOpen)
			content := afterOpen[end+terminator:]
			reset := strings.Index(content, "\x1b]8;;")
			linked.WriteString(ansi.Strip(content[:reset]))
		}
	}
	if got := linked.String(); got != url {
		t.Errorf("modal linked fallback URL = %q, want complete URL %q", got, url)
	}
}

func dimensions(rendered string) (height, width int) {
	lines := strings.Split(rendered, "\n")
	for _, line := range lines {
		width = max(width, ansi.StringWidth(ansi.Strip(line)))
	}
	return len(lines), width
}

func osc52Payloads(s string) []string {
	var payloads []string
	for rest := s; ; {
		start := strings.Index(rest, "\x1b]52;c;")
		if start < 0 {
			return payloads
		}
		rest = rest[start+7:]
		end, terminator := oscTerminator(rest)
		if end < 0 {
			return payloads
		}
		payloads = append(payloads, rest[:end])
		rest = rest[end+terminator:]
	}
}
