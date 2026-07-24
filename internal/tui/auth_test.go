package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jonathanung/strike-cli/internal/protocol"
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
