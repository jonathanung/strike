package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/protocol"
)

func TestProviderModalAddCustomRow(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.composer.SetValue("/provider")
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	pm, ok := m.modal.(*providerModal)
	if !ok {
		t.Fatalf("modal = %T", m.modal)
	}
	// Last row is Add custom provider…
	pm.cursor = pm.rowCount() - 1
	next, _ := pm.update(tea.KeyMsg{Type: tea.KeyEnter})
	form, ok := next.(*customProviderFormModal)
	if !ok {
		t.Fatalf("enter add row → %T", next)
	}
	view := form.view(80, m.th)
	if strings.Contains(view, "sk-") {
		t.Error("view must not show secrets")
	}
	if !strings.Contains(strings.ToLower(view), "provider name") {
		t.Errorf("form missing name step: %q", view)
	}
}

func TestCustomProviderFormSavesWithoutLeakingKey(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	fp := m.services.Providers.(*fakeProviders)
	fa := m.services.Auth.(*fakeAuth)

	form := newCustomProviderFormModal(m.services, ops, m.th, nil, true, nil)
	form.name.SetValue("kimi")
	form.url.SetValue("https://api.moonshot.cn/v1")
	form.apiCursor = 0 // openai
	form.key.SetValue("sk-secret-key")
	form.models.SetValue("moonshot-v1")
	form.step = 4

	next, cmd := form.save()
	if next != form {
		t.Fatalf("save should keep form until cmd completes, got %T", next)
	}
	msg := cmd()
	saved, ok := msg.(customProviderSavedMsg)
	if !ok || saved.err != nil {
		t.Fatalf("msg = %#v", msg)
	}
	if saved.name != "kimi" {
		t.Errorf("name = %q", saved.name)
	}
	if len(fp.items) != 1 || fp.items[0].Name != "kimi" || fp.items[0].API != "openai" {
		t.Fatalf("providers = %+v", fp.items)
	}
	if len(fa.setCalls) != 1 || fa.setCalls[0].key != "sk-secret-key" {
		t.Fatalf("setCalls = %+v", fa.setCalls)
	}
	// Key must not appear in form view (password echo).
	v := form.view(80, m.th)
	if strings.Contains(v, "sk-secret-key") {
		t.Error("secret visible in view")
	}
	// selectAfter enqueued SelectModel
	select {
	case op := <-ops:
		sm, ok := op.(protocol.SelectModel)
		if !ok || sm.Provider != "kimi" {
			t.Fatalf("op = %#v", op)
		}
	default:
		t.Fatal("expected SelectModel")
	}
}

func TestSettingsModalCRUD(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	fp := m.services.Providers.(*fakeProviders)
	fp.items = []host.CustomProvider{{
		Name: "ollama", BaseURL: "http://localhost:11434/v1", API: "openai", Models: []string{"llama3"},
	}}

	m.composer.SetValue("/settings")
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	sm, ok := m.modal.(*settingsModal)
	if !ok {
		t.Fatalf("modal = %T", m.modal)
	}
	sm.reload()
	view := sm.view(80, m.th)
	if !strings.Contains(view, "ollama") || !strings.Contains(view, "Add custom provider") {
		t.Errorf("settings view = %q", view)
	}

	// Remove ollama (cursor on first custom = index 1).
	sm.cursor = 1
	_, cmd := sm.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	if cmd == nil {
		t.Fatal("expected remove cmd")
	}
	msg := cmd()
	if _, ok := msg.(customProviderRemovedMsg); !ok {
		t.Fatalf("msg = %#v", msg)
	}
	if len(fp.items) != 0 {
		t.Fatalf("items after remove = %+v", fp.items)
	}
}

func TestCommandsIncludeSettings(t *testing.T) {
	catalog := commandCatalog(nil)
	found := false
	for _, c := range catalog {
		if c.Name == "/settings" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("/settings missing from catalog")
	}
}
