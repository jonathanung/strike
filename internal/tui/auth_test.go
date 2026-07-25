package tui

import (
	"bytes"
	"context"
	"encoding/base64"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

// TestOAuthLoginCompletesAndSwitchesProviderWhenSelectAfter drives the full
// decoupled OAuth flow through the host service: picking an unauthenticated
// provider opens the wait modal, the started/done messages carry only an
// outcome string, and success switches to the provider (selectAfter).
func TestOAuthLoginCompletesAndSwitchesProviderWhenSelectAfter(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	picker := newProviderModal(m.services, "", m.ops, m.th)
	picker.cursor = 1 // openai: unauthenticated, OAuth-capable
	m.modal = picker

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if _, ok := m.modal.(*authWaitModal); !ok {
		t.Fatalf("selecting an unauthenticated OAuth provider did not open the wait modal: %T", m.modal)
	}

	started := runAppCmd(t, cmd)
	if _, ok := started.(authStartedMsg); !ok {
		t.Fatalf("begin-oauth command produced %T, want authStartedMsg", started)
	}
	updated, cmd = m.Update(started)
	m = updated.(Model)
	if wm, ok := m.modal.(*authWaitModal); !ok || wm.url == "" {
		t.Fatalf("wait modal did not receive the login URL: %#v", m.modal)
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
	m.composer.SetValue("leave composer unchanged")
	wm := newAuthWaitModalForTest(url)
	m.modal = wm

	before := wm.view(80, m.th)
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	m = updated.(Model)
	if got := m.composer.Value(); got != "leave composer unchanged" {
		t.Errorf("composer after modal copy key = %q, want unchanged", got)
	}
	if m.modal != wm {
		t.Errorf("copy key closed or replaced OAuth modal: %T", m.modal)
	}
	if cmd == nil {
		t.Fatal("OAuth URL copy key returned nil command")
	}
	if !wm.copyRequested {
		t.Error("OAuth URL copy key did not record a copy request")
	}

	after := wm.view(80, m.th)
	beforeHeight, beforeWidth := dimensions(before)
	afterHeight, afterWidth := dimensions(after)
	if afterHeight != beforeHeight || afterWidth != beforeWidth {
		t.Errorf("copy feedback changed modal dimensions: before %dx%d, after %dx%d", beforeWidth, beforeHeight, afterWidth, afterHeight)
	}
	stripped := ansi.Strip(after)
	if !strings.Contains(strings.ToLower(stripped), "copy requested") {
		t.Errorf("copy feedback missing from view: %q", stripped)
	}
	if strings.Contains(strings.ToLower(stripped), "copied") || strings.Contains(strings.ToLower(stripped), "success") {
		t.Errorf("copy feedback claims completion rather than request: %q", stripped)
	}
	if !strings.Contains(stripped, "c copy") {
		t.Errorf("OAuth wait hint missing c copy: %q", stripped)
	}
	assertModalURL(t, after, url)

	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune("c")},
		{Type: tea.KeyRunes, Runes: []rune("C")},
	} {
		updated, nextCmd := m.Update(key)
		m = updated.(Model)
		if key.String() == "c" && nextCmd == nil {
			t.Error("repeated lowercase c did not produce a fresh copy command")
		}
		if key.String() == "C" && nextCmd != nil {
			t.Error("uppercase C unexpectedly requested a copy")
		}
	}

	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(Model)
	if m.modal != nil || cmd != nil {
		t.Errorf("escape after copy = modal %T, command %v; want closed with no command", m.modal, cmd)
	}
}

func TestAuthWaitModalCopyKeyOnlyAppliesToOAuthURL(t *testing.T) {
	t.Run("device flow c is ignored", func(t *testing.T) {
		wm := newAuthWaitModalForTest("")
		wm.userCode, wm.verifyURI = "ABCD-EFGH", "https://login.example/device"
		next, cmd := wm.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
		if next != wm || cmd != nil || wm.copyRequested {
			t.Errorf("device flow c = modal %T command %v requested=%v, want no-op", next, cmd, wm.copyRequested)
		}
	})

	t.Run("OAuth wait without a URL c is ignored", func(t *testing.T) {
		wm := newAuthWaitModalForTest("")
		next, cmd := wm.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
		if next != wm || cmd != nil || wm.copyRequested {
			t.Errorf("empty OAuth wait c = modal %T command %v requested=%v, want no-op", next, cmd, wm.copyRequested)
		}
	})

	t.Run("normal composer c still inserts", func(t *testing.T) {
		m, _ := newAppTestModel(nil, nil)
		m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
		if got := m.composer.Value(); got != "c" {
			t.Errorf("composer c = %q, want c", got)
		}
	})
}

func TestOAuthURLCopyCommandWritesOneExactOSC52Request(t *testing.T) {
	url := "https://console.x.ai/api/oauth/authorize?state=osc52%2Fstate&code_challenge=copy-challenge&nonce=copy-nonce"
	var output bytes.Buffer
	command := &osc52CopyCommand{text: url}
	command.SetStdout(&output)
	if err := command.Run(); err != nil {
		t.Fatalf("OSC52 copy command returned error: %v", err)
	}

	requests := osc52Payloads(output.String())
	if len(requests) != 1 {
		t.Fatalf("OSC52 requests = %d, want 1: %q", len(requests), output.String())
	}
	payload, err := base64.StdEncoding.DecodeString(requests[0])
	if err != nil {
		t.Fatalf("OSC52 payload is not base64: %q: %v", requests[0], err)
	}
	if got := string(payload); got != url {
		t.Errorf("OSC52 decoded payload = %q, want complete URL %q", got, url)
	}
}

func newAuthWaitModalForTest(url string) *authWaitModal {
	ctx, cancel := context.WithCancel(context.Background())
	return &authWaitModal{provider: "openai", url: url, ctx: ctx, cancel: cancel}
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
