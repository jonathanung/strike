package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

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
}

func TestProviderModalDoubleBackslashLogout(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	fake := m.services.Auth.(*fakeAuth)
	fake.statuses = []host.ProviderStatus{
		{Name: "openai", Detail: "oauth", Authed: true, OAuth: true, APIKey: true},
		{Name: "echo", Detail: "offline dev provider", Authed: true, Builtin: true},
	}

	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	pm := newProviderModal(m.services, "openai", m.ops, m.th)
	pm.now = func() time.Time { return now }
	pm.cursor = 0

	// First "\" arms; no logout yet.
	next, cmd := pm.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'\\'}})
	pm = next.(*providerModal)
	if cmd != nil {
		t.Fatal("first \\ should arm without a command")
	}
	if len(fake.logoutCalls) != 0 {
		t.Fatalf("logoutCalls after arm = %v", fake.logoutCalls)
	}
	view := ansi.Strip(pm.view(80, m.th))
	if !strings.Contains(view, "again to log out") {
		t.Fatalf("armed hint missing: %q", view)
	}

	// Second "\" within window commits logout.
	now = now.Add(2 * time.Second)
	next, cmd = pm.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'\\'}})
	if next != pm {
		t.Fatalf("logout kept modal open, got %T", next)
	}
	if cmd == nil {
		t.Fatal("second \\ expected logout command")
	}
	msg := cmd()
	got, ok := msg.(providerLogoutMsg)
	if !ok || got.provider != "openai" || got.err != nil {
		t.Fatalf("logout msg = %#v", msg)
	}
	if len(fake.logoutCalls) != 1 || fake.logoutCalls[0] != "openai" {
		t.Fatalf("logoutCalls = %v, want [openai]", fake.logoutCalls)
	}

	// App applies the message: notice + refreshed statuses.
	m.modal = pm
	updated, _ := m.Update(got)
	m = updated.(Model)
	if !strings.Contains(m.notice, "logged out of openai") {
		t.Fatalf("notice = %q", m.notice)
	}
	if pm.statuses[0].Authed {
		t.Fatal("status not refreshed after logout")
	}
}

func TestProviderModalLogoutArmExpires(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	fake := m.services.Auth.(*fakeAuth)
	fake.statuses = []host.ProviderStatus{
		{Name: "anthropic", Detail: "api key", Authed: true, APIKey: true},
	}
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	pm := newProviderModal(m.services, "", m.ops, m.th)
	pm.now = func() time.Time { return now }
	pm.cursor = 0

	next, _ := pm.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'\\'}})
	pm = next.(*providerModal)
	now = now.Add(logoutConfirmWindow + time.Millisecond)
	next, cmd := pm.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'\\'}})
	pm = next.(*providerModal)
	if cmd != nil {
		t.Fatal("expired arm should re-arm, not logout")
	}
	if len(fake.logoutCalls) != 0 {
		t.Fatalf("logoutCalls = %v", fake.logoutCalls)
	}
	// Immediate second after re-arm works.
	now = now.Add(time.Second)
	_, cmd = pm.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'\\'}})
	if cmd == nil {
		t.Fatal("expected logout after re-arm")
	}
	if _, ok := cmd().(providerLogoutMsg); !ok {
		t.Fatal("want providerLogoutMsg")
	}
}

func TestProviderModalLogoutBuiltinAndAddRow(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	pm := newProviderModal(m.services, "echo", m.ops, m.th)
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	pm.now = func() time.Time { return now }

	// Builtin echo.
	for i, s := range pm.statuses {
		if s.Name == "echo" {
			pm.cursor = i
			break
		}
	}
	pm.logoutArmedAt = now
	_, cmd := pm.handleLogoutKey()
	msg := cmd().(providerLogoutMsg)
	if !errors.Is(msg.err, errBuiltinLogout) {
		t.Fatalf("builtin logout err = %v", msg.err)
	}

	// Add-custom trailing row: armed double-\ is a no-op.
	pm.cursor = pm.rowCount() - 1
	pm.logoutArmedAt = now
	_, cmd = pm.handleLogoutKey()
	if cmd != nil {
		t.Fatalf("add-row logout cmd = %v", cmd)
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
