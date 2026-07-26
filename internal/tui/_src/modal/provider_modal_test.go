package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/host"
)

func TestProviderModalFilterAndNoJKMove(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	fake := m.services.Auth.(*fakeAuth)
	fake.statuses = []host.ProviderStatus{
		{Name: "anthropic", Detail: "none", APIKey: true},
		{Name: "openai", Detail: "none", OAuth: true, APIKey: true},
		{Name: "xai", Detail: "none", OAuth: true, Device: true, APIKey: true},
		{Name: "kimi", Detail: "api key", Authed: true, Custom: true, APIKey: true},
		{Name: "echo", Detail: "offline dev provider", Authed: true, Builtin: true},
	}

	pm := newProviderModal(m.services, "", m.ops, m.th)
	// j/k type into the filter instead of moving the cursor.
	start := pm.cursor
	next, _ := pm.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	pm = next.(*providerModal)
	if pm.cursor != 0 {
		t.Fatalf("j moved cursor to %d, want filter-only (cursor 0)", pm.cursor)
	}
	if pm.filter != "j" {
		t.Fatalf("filter = %q, want j", pm.filter)
	}
	next, _ = pm.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	pm = next.(*providerModal)
	if pm.filter != "jk" {
		t.Fatalf("filter = %q, want jk", pm.filter)
	}
	if pm.cursor == start+1 || pm.cursor == start-1 {
		t.Fatal("j/k still act as movement keybinds")
	}

	// Clear and filter to custom provider.
	pm.filter = ""
	pm.cursor = 0
	next, _ = pm.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("kim")})
	pm = next.(*providerModal)
	list := pm.filtered()
	if len(list) != 1 || list[0].Name != "kimi" {
		t.Fatalf("filtered = %+v, want [kimi]", list)
	}
	// Add-custom row remains after filtered matches.
	if pm.rowCount() != 2 {
		t.Fatalf("rowCount = %d, want 2 (kimi + add)", pm.rowCount())
	}
	view := ansi.Strip(pm.view(80, m.th))
	if !strings.Contains(view, "kimi") {
		t.Fatalf("view missing kimi: %q", view)
	}
	if !strings.Contains(view, "Add custom provider") {
		t.Fatalf("add-custom row filtered away: %q", view)
	}
	if !strings.Contains(view, "filter:") {
		t.Fatalf("view missing filter header: %q", view)
	}
	if strings.Contains(view, "j/k") {
		t.Fatalf("hint still advertises j/k: %q", view)
	}
	if !strings.Contains(view, "ctrl+x logout") {
		t.Fatalf("view missing logout hint: %q", view)
	}
}

func TestProviderModalLogoutConfirmAccept(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	fake := m.services.Auth.(*fakeAuth)
	fake.statuses = []host.ProviderStatus{
		{Name: "openai", Detail: "oauth", Authed: true, OAuth: true, APIKey: true},
		{Name: "echo", Detail: "offline dev provider", Authed: true, Builtin: true},
	}

	pm := newProviderModal(m.services, "openai", m.ops, m.th)
	pm.cursor = 0

	// ctrl+x opens confirm; no logout yet.
	next, cmd := pm.update(tea.KeyMsg{Type: tea.KeyCtrlX})
	pm = next.(*providerModal)
	if cmd != nil {
		t.Fatal("ctrl+x should open confirm without a command")
	}
	if pm.phase != providerPhaseConfirmLogout {
		t.Fatalf("phase = %v, want confirm", pm.phase)
	}
	if len(fake.logoutCalls) != 0 {
		t.Fatalf("logoutCalls after arm = %v", fake.logoutCalls)
	}
	view := ansi.Strip(pm.view(80, m.th))
	if !strings.Contains(view, "Log out of openai?") {
		t.Fatalf("confirm missing title line: %q", view)
	}
	if !strings.Contains(view, "all stored credentials") {
		t.Fatalf("multi-method confirm missing all-credentials line: %q", view)
	}
	if !strings.Contains(view, "y/enter confirm") {
		t.Fatalf("confirm missing y hint: %q", view)
	}

	// y commits logout.
	next, cmd = pm.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if next != pm {
		t.Fatalf("logout kept modal open, got %T", next)
	}
	if pm.phase != providerPhaseBrowse {
		t.Fatalf("phase after y = %v, want browse", pm.phase)
	}
	if cmd == nil {
		t.Fatal("y expected logout command")
	}
	msg := cmd()
	got, ok := msg.(providerLogoutMsg)
	if !ok || got.provider != "openai" || got.err != nil {
		t.Fatalf("logout msg = %#v", msg)
	}
	if len(fake.logoutCalls) != 1 || fake.logoutCalls[0] != "openai" {
		t.Fatalf("logoutCalls = %v, want [openai]", fake.logoutCalls)
	}

	// App applies the message: notice + refreshed statuses (no secrets).
	m.modal = pm
	updated, _ := m.Update(got)
	m = updated.(Model)
	if !strings.Contains(m.notice, "logged out of openai") {
		t.Fatalf("notice = %q", m.notice)
	}
	if strings.Contains(strings.ToLower(m.notice), "token") || strings.Contains(m.notice, "sk-") {
		t.Fatalf("notice leaked secret material: %q", m.notice)
	}
	if pm.statuses[0].Authed {
		t.Fatal("status not refreshed after logout")
	}
	if pm.statuses[0].Detail != "none" {
		t.Fatalf("detail after logout = %q, want none", pm.statuses[0].Detail)
	}
}

func TestProviderModalLogoutConfirmCancel(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	fake := m.services.Auth.(*fakeAuth)
	fake.statuses = []host.ProviderStatus{
		{Name: "anthropic", Detail: "api key", Authed: true, APIKey: true},
	}
	pm := newProviderModal(m.services, "", m.ops, m.th)
	pm.cursor = 0

	next, _ := pm.update(tea.KeyMsg{Type: tea.KeyCtrlX})
	pm = next.(*providerModal)
	if pm.phase != providerPhaseConfirmLogout {
		t.Fatalf("phase = %v", pm.phase)
	}
	view := ansi.Strip(pm.view(80, m.th))
	if !strings.Contains(view, "Log out of anthropic?") {
		t.Fatalf("confirm view = %q", view)
	}
	if strings.Contains(view, "all stored credentials") {
		t.Fatalf("single-method should not claim all methods: %q", view)
	}
	if !strings.Contains(view, "Current: api key") {
		t.Fatalf("confirm missing current detail: %q", view)
	}

	// n cancels — credentials intact.
	next, cmd := pm.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	pm = next.(*providerModal)
	if cmd != nil {
		t.Fatal("cancel should not run logout")
	}
	if pm.phase != providerPhaseBrowse {
		t.Fatalf("phase after n = %v, want browse", pm.phase)
	}
	if len(fake.logoutCalls) != 0 {
		t.Fatalf("logoutCalls after cancel = %v", fake.logoutCalls)
	}
	if !fake.statuses[0].Authed || fake.statuses[0].Detail != "api key" {
		t.Fatalf("credentials changed on cancel: %+v", fake.statuses[0])
	}

	// esc also cancels.
	next, _ = pm.update(tea.KeyMsg{Type: tea.KeyCtrlX})
	pm = next.(*providerModal)
	next, cmd = pm.update(tea.KeyMsg{Type: tea.KeyEscape})
	pm = next.(*providerModal)
	if cmd != nil || pm.phase != providerPhaseBrowse {
		t.Fatalf("esc cancel: phase=%v cmd=%v", pm.phase, cmd != nil)
	}
	if len(fake.logoutCalls) != 0 {
		t.Fatalf("logoutCalls after esc = %v", fake.logoutCalls)
	}
}

func TestProviderModalLogoutConfirmEnterAccept(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	fake := m.services.Auth.(*fakeAuth)
	fake.statuses = []host.ProviderStatus{
		{Name: "xai", Detail: "oauth+key", Authed: true, OAuth: true, Device: true, APIKey: true},
	}
	pm := newProviderModal(m.services, "", m.ops, m.th)
	pm.cursor = 0
	next, _ := pm.update(tea.KeyMsg{Type: tea.KeyCtrlX})
	pm = next.(*providerModal)
	_, cmd := pm.update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter should confirm logout")
	}
	msg := cmd().(providerLogoutMsg)
	if msg.provider != "xai" || msg.err != nil {
		t.Fatalf("msg = %#v", msg)
	}
	if len(fake.logoutCalls) != 1 || fake.logoutCalls[0] != "xai" {
		t.Fatalf("logoutCalls = %v", fake.logoutCalls)
	}
}

func TestProviderModalLogoutBuiltinAndAddRow(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	pm := newProviderModal(m.services, "echo", m.ops, m.th)

	// Builtin echo.
	for i, s := range pm.statuses {
		if s.Name == "echo" {
			pm.cursor = i
			break
		}
	}
	_, cmd := pm.beginLogoutConfirm()
	msg := cmd().(providerLogoutMsg)
	if !errors.Is(msg.err, errBuiltinLogout) {
		t.Fatalf("builtin logout err = %v", msg.err)
	}

	// Add-custom trailing row: ctrl+x is a no-op.
	pm.cursor = pm.rowCount() - 1
	next, cmd := pm.beginLogoutConfirm()
	if cmd != nil {
		t.Fatalf("add-row logout cmd = %v", cmd)
	}
	if next.(*providerModal).phase != providerPhaseBrowse {
		t.Fatal("add-row should stay in browse")
	}
}

func TestProviderModalMultiAuthEnterOpensChooser(t *testing.T) {
	// multi-auth → chooser: unauthenticated multi-method provider still opens
	// the method chooser on enter (unchanged by filter/logout work).
	m, _ := newAppTestModel(nil, nil)
	pm := newProviderModal(m.services, "", m.ops, m.th)
	for i, s := range pm.statuses {
		if s.Name == "openai" {
			pm.cursor = i
			break
		}
	}
	next, cmd := pm.selectCurrent()
	if cmd != nil {
		t.Fatalf("chooser open returned cmd %T", cmd)
	}
	chooser, ok := next.(*authMethodModal)
	if !ok || chooser.provider != "openai" {
		t.Fatalf("select unauth multi-method = %T %#v, want authMethodModal openai", next, next)
	}
	if len(chooser.items) < 2 {
		t.Fatalf("chooser items = %d, want multi-method", len(chooser.items))
	}
}

func TestProviderModalNavigationWithFilter(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	pm := newProviderModal(m.services, "", m.ops, m.th)
	pm.filter = "a" // anthropic, openai, xai may match depending on names; "a" hits anthropic/openai/xai
	list := pm.filtered()
	if len(list) == 0 {
		t.Fatal("expected some matches for filter a")
	}
	pm.cursor = 0
	next, _ := pm.update(tea.KeyMsg{Type: tea.KeyDown})
	pm = next.(*providerModal)
	if pm.cursor != 1 && len(list) > 1 {
		t.Fatalf("down cursor = %d, want 1", pm.cursor)
	}
	next, _ = pm.update(tea.KeyMsg{Type: tea.KeyCtrlP})
	pm = next.(*providerModal)
	if pm.cursor != 0 {
		t.Fatalf("ctrl+p cursor = %d, want 0", pm.cursor)
	}
}

func TestLogoutClearsMultipleMethods(t *testing.T) {
	cases := []struct {
		name string
		s    host.ProviderStatus
		want bool
	}{
		{name: "oauth+key detail", s: host.ProviderStatus{Detail: "oauth+key", Authed: true, OAuth: true, APIKey: true}, want: true},
		{name: "single api key", s: host.ProviderStatus{Detail: "api key", Authed: true, APIKey: true}, want: false},
		{name: "multi capability authed", s: host.ProviderStatus{Detail: "oauth", Authed: true, OAuth: true, APIKey: true}, want: true},
		{name: "multi capability unauthed", s: host.ProviderStatus{Detail: "none", OAuth: true, APIKey: true}, want: false},
	}
	for _, tc := range cases {
		if got := logoutClearsMultipleMethods(tc.s); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestProviderModalLogoutDeletesCustom(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	fake := m.services.Auth.(*fakeAuth)
	fp := m.services.Providers.(*fakeProviders)
	fp.items = []host.CustomProvider{{
		Name: "kimi", BaseURL: "https://api.moonshot.cn/v1", API: "openai",
	}}
	fake.statuses = []host.ProviderStatus{
		{Name: "kimi", Detail: "api key · openai · api.moonshot.cn", Authed: true, Custom: true, APIKey: true},
		{Name: "echo", Detail: "offline", Authed: true, Builtin: true},
	}
	pm := newProviderModal(m.services, "kimi", m.ops, m.th)
	pm.cursor = 0

	next, _ := pm.update(tea.KeyMsg{Type: tea.KeyCtrlX})
	pm = next.(*providerModal)
	view := ansi.Strip(pm.view(80, m.th))
	if !strings.Contains(view, "Deletes this custom provider") {
		t.Fatalf("confirm should say delete custom: %q", view)
	}

	_, cmd := pm.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if cmd == nil {
		t.Fatal("expected logout cmd")
	}
	msg := cmd()
	got, ok := msg.(providerLogoutMsg)
	if !ok || got.err != nil || !got.removed || got.provider != "kimi" {
		t.Fatalf("msg = %#v", msg)
	}
	if len(fp.items) != 0 {
		t.Fatalf("custom not removed: %+v", fp.items)
	}
	if len(fake.logoutCalls) != 1 || fake.logoutCalls[0] != "kimi" {
		t.Fatalf("logoutCalls = %v", fake.logoutCalls)
	}

	m.modal = pm
	updated, _ := m.Update(got)
	m = updated.(Model)
	if !strings.Contains(m.notice, "removed custom provider kimi") {
		t.Fatalf("notice = %q", m.notice)
	}
}

func TestCustomProviderFormEnvKeyRef(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	fp := m.services.Providers.(*fakeProviders)
	fa := m.services.Auth.(*fakeAuth)

	form := newCustomProviderFormModal(m.services, ops, m.th, nil, false, nil)
	form.name.SetValue("proxy")
	form.url.SetValue("{env:PROXY_BASE}")
	form.apiCursor = 0
	form.key.SetValue("{env:PROXY_API_KEY}")
	form.models.SetValue("m1")
	form.step = 4

	_, cmd := form.save()
	msg := cmd()
	saved, ok := msg.(customProviderSavedMsg)
	if !ok || saved.err != nil {
		t.Fatalf("msg = %#v", msg)
	}
	if len(fp.items) != 1 || fp.items[0].APIKeyEnv != "PROXY_API_KEY" {
		t.Fatalf("providers = %+v", fp.items)
	}
	if fp.items[0].BaseURL != "{env:PROXY_BASE}" {
		t.Errorf("BaseURL = %q", fp.items[0].BaseURL)
	}
	if len(fa.setCalls) != 0 {
		t.Fatalf("env ref must not SetAPIKey: %+v", fa.setCalls)
	}
}
