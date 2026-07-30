package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

func TestProviderModalAddCustomRow(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.composer.SetValue("/provider")
	m = updateApp(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	pm, ok := m.modal.(*providerModal)
	if !ok {
		t.Fatalf("modal = %T", m.modal)
	}
	// Last row is Add custom provider…
	pm.cursor = pm.rowCount() - 1
	next, _ := pm.update(tea.KeyPressMsg{Code: tea.KeyEnter})
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
	m = updateApp(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	sm, ok := m.modal.(*settingsModal)
	if !ok {
		t.Fatalf("modal = %T", m.modal)
	}
	// Menu → Custom providers.
	sm.cursor = int(settingsMenuProviders)
	next, _ := sm.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	sm, ok = next.(*settingsModal)
	if !ok {
		t.Fatalf("after enter providers = %T", next)
	}
	sm.reload()
	view := sm.view(80, m.th)
	if !strings.Contains(view, "ollama") || !strings.Contains(view, "Add custom provider") {
		t.Errorf("settings view = %q", view)
	}

	// Remove ollama (cursor on first custom = index 1).
	sm.cursor = 1
	_, cmd := sm.update(tea.KeyPressMsg{Text: "d"})
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

func TestCustomProviderFormUpdateAdvanceAndBack(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	form := newCustomProviderFormModal(m.services, ops, m.th, nil, false, nil)
	if form.step != 0 {
		t.Fatalf("new form step = %d, want 0", form.step)
	}

	// Empty name rejected.
	next, _ := form.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	form = next.(*customProviderFormModal)
	if form.step != 0 || form.err == "" {
		t.Fatalf("empty name: step=%d err=%q", form.step, form.err)
	}

	form.name.SetValue("  Kimi  ")
	next, _ = form.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	form = next.(*customProviderFormModal)
	if form.step != 1 || form.name.Value() != "kimi" {
		t.Fatalf("after name: step=%d name=%q", form.step, form.name.Value())
	}

	// Empty URL rejected.
	next, _ = form.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	form = next.(*customProviderFormModal)
	if form.step != 1 || form.err == "" {
		t.Fatalf("empty url: step=%d err=%q", form.step, form.err)
	}

	form.url.SetValue("https://api.example.com/v1")
	next, _ = form.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	form = next.(*customProviderFormModal)
	if form.step != 2 {
		t.Fatalf("after url step=%d", form.step)
	}

	// API step: j/k cycle wire API.
	next, _ = form.update(tea.KeyPressMsg{Text: "j"})
	form = next.(*customProviderFormModal)
	if form.apiCursor != 1 {
		t.Fatalf("j apiCursor=%d, want 1", form.apiCursor)
	}
	next, _ = form.update(tea.KeyPressMsg{Text: "k"})
	form = next.(*customProviderFormModal)
	if form.apiCursor != 0 {
		t.Fatalf("k apiCursor=%d, want 0", form.apiCursor)
	}
	next, _ = form.update(tea.KeyPressMsg{Code: tea.KeyDown})
	form = next.(*customProviderFormModal)
	if form.apiCursor != 1 {
		t.Fatalf("down apiCursor=%d, want 1", form.apiCursor)
	}
	next, _ = form.update(tea.KeyPressMsg{Code: tea.KeyUp})
	form = next.(*customProviderFormModal)
	if form.apiCursor != 0 {
		t.Fatalf("up apiCursor=%d, want 0", form.apiCursor)
	}

	next, _ = form.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	form = next.(*customProviderFormModal)
	if form.step != 3 {
		t.Fatalf("after api step=%d", form.step)
	}
	next, _ = form.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	form = next.(*customProviderFormModal)
	if form.step != 4 {
		t.Fatalf("after key step=%d", form.step)
	}

	// shift+tab backs up (not past name when creating).
	next, _ = form.update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	form = next.(*customProviderFormModal)
	if form.step != 3 {
		t.Fatalf("shift+tab step=%d, want 3", form.step)
	}
	for form.step > 0 {
		next, _ = form.update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
		form = next.(*customProviderFormModal)
	}
	if form.step != 0 {
		t.Fatalf("backed to step=%d", form.step)
	}
	// shift+tab at step 0 is noop.
	next, _ = form.update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	form = next.(*customProviderFormModal)
	if form.step != 0 {
		t.Fatalf("shift+tab at 0 moved to %d", form.step)
	}
}

func TestCustomProviderFormEscReturnsToParent(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	parent := &settingsModal{}
	form := newCustomProviderFormModal(m.services, ops, m.th, nil, false, parent)
	next, cmd := form.update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if next != parent {
		t.Fatalf("esc → %T, want parent settingsModal", next)
	}
	if cmd != nil {
		t.Fatal("esc should not emit cmd")
	}
}

func TestCustomProviderFormEditSkipsName(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	existing := &host.CustomProvider{
		Name: "ollama", BaseURL: "http://localhost:11434/v1", API: "anthropic", Models: []string{"llama3"},
	}
	form := newCustomProviderFormModal(m.services, ops, m.th, existing, false, nil)
	if !form.editing {
		t.Fatal("expected editing")
	}
	if form.step != 1 {
		t.Fatalf("edit starts at step %d, want 1 (url)", form.step)
	}
	if form.name.Value() != "ollama" || form.url.Value() != existing.BaseURL {
		t.Fatalf("prefill name=%q url=%q", form.name.Value(), form.url.Value())
	}
	if form.apiCursor != 1 { // anthropic
		t.Fatalf("apiCursor=%d, want 1 anthropic", form.apiCursor)
	}
	// shift+tab must not retreat past url when editing.
	next, _ := form.update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	form = next.(*customProviderFormModal)
	if form.step != 1 {
		t.Fatalf("edit shift+tab step=%d, want 1", form.step)
	}
	plain := ansi.Strip(form.view(80, m.th))
	if !strings.Contains(plain, "Edit ollama") {
		t.Errorf("edit title missing: %q", plain)
	}
}

func TestCustomProviderFormViewSteps(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	form := newCustomProviderFormModal(m.services, ops, m.th, nil, false, nil)
	tests := []struct {
		step int
		want []string
	}{
		{0, []string{"Add custom provider", "Provider name", "step name"}},
		{1, []string{"Base URL", "step base URL", "openai:"}},
		{2, []string{"Wire API", "openai", "anthropic", "chat-completions", "messages"}},
		{3, []string{"API key", "optional"}},
		{4, []string{"Model ids", "comma-separated"}},
	}
	for _, tt := range tests {
		form.step = tt.step
		form.focusStep()
		if tt.step == 0 {
			form.err = "name is required"
		} else {
			form.err = ""
		}
		plain := ansi.Strip(form.view(80, theme.Default()))
		for _, w := range tt.want {
			if !strings.Contains(plain, w) {
				t.Errorf("step %d missing %q:\n%s", tt.step, w, plain)
			}
		}
		if tt.step == 0 && !strings.Contains(plain, "name is required") {
			t.Errorf("step 0 missing err: %q", plain)
		}
	}
}

func TestCustomProviderFormSaveErrors(t *testing.T) {
	t.Run("nil providers", func(t *testing.T) {
		form := &customProviderFormModal{
			name:   newTextInput(theme.Default(), ""),
			url:    newTextInput(theme.Default(), ""),
			key:    newTextInput(theme.Default(), ""),
			models: newTextInput(theme.Default(), ""),
		}
		form.name.SetValue("x")
		form.url.SetValue("http://x")
		next, cmd := form.save()
		if next != form || cmd != nil || form.err == "" {
			t.Fatalf("nil providers: next=%T cmd=%v err=%q", next, cmd != nil, form.err)
		}
	})
	t.Run("upsert error", func(t *testing.T) {
		m, ops := newAppTestModel(nil, nil)
		fp := m.services.Providers.(*fakeProviders)
		fp.err = errors.New("disk full")
		form := newCustomProviderFormModal(m.services, ops, m.th, nil, false, nil)
		form.name.SetValue("x")
		form.url.SetValue("http://x")
		form.step = 4
		_, cmd := form.save()
		msg := cmd().(customProviderSavedMsg)
		if msg.err == nil || !strings.Contains(msg.err.Error(), "disk full") {
			t.Fatalf("msg = %#v", msg)
		}
	})
	t.Run("api key error", func(t *testing.T) {
		m, ops := newAppTestModel(nil, nil)
		fa := m.services.Auth.(*fakeAuth)
		fa.setErr = errors.New("auth locked")
		form := newCustomProviderFormModal(m.services, ops, m.th, nil, false, nil)
		form.name.SetValue("x")
		form.url.SetValue("http://x")
		form.key.SetValue("sk-x")
		form.step = 4
		_, cmd := form.save()
		msg := cmd().(customProviderSavedMsg)
		if msg.err == nil || !strings.Contains(msg.err.Error(), "auth locked") {
			t.Fatalf("msg = %#v", msg)
		}
	})
	t.Run("save without key skips auth", func(t *testing.T) {
		m, ops := newAppTestModel(nil, nil)
		fa := m.services.Auth.(*fakeAuth)
		form := newCustomProviderFormModal(m.services, ops, m.th, nil, false, nil)
		form.name.SetValue("local")
		form.url.SetValue("http://localhost")
		form.step = 4
		_, cmd := form.save()
		msg := cmd().(customProviderSavedMsg)
		if msg.err != nil {
			t.Fatalf("err = %v", msg.err)
		}
		if len(fa.setCalls) != 0 {
			t.Fatalf("setCalls = %+v", fa.setCalls)
		}
	})
	t.Run("enter on last step saves", func(t *testing.T) {
		m, ops := newAppTestModel(nil, nil)
		form := newCustomProviderFormModal(m.services, ops, m.th, nil, true, nil)
		form.name.SetValue("z")
		form.url.SetValue("http://z")
		form.models.SetValue("a, b; c, d")
		form.step = 4
		next, cmd := form.update(tea.KeyPressMsg{Code: tea.KeyEnter})
		if next != form || cmd == nil {
			t.Fatalf("enter save: next=%T cmd=%v", next, cmd != nil)
		}
		msg := cmd().(customProviderSavedMsg)
		if msg.err != nil || msg.name != "z" || !msg.selectAfter {
			t.Fatalf("msg = %#v", msg)
		}
		fp := m.services.Providers.(*fakeProviders)
		if len(fp.items) != 1 {
			t.Fatalf("items = %+v", fp.items)
		}
		wantModels := []string{"a", "b", "c", "d"}
		if got := fp.items[0].Models; len(got) != len(wantModels) {
			t.Fatalf("models = %#v, want %#v", got, wantModels)
		}
		for i, w := range wantModels {
			if fp.items[0].Models[i] != w {
				t.Fatalf("models = %#v, want %#v", fp.items[0].Models, wantModels)
			}
		}
		select {
		case op := <-ops:
			if sm, ok := op.(protocol.SelectModel); !ok || sm.Provider != "z" {
				t.Fatalf("op = %#v", op)
			}
		default:
			t.Fatal("expected SelectModel")
		}
	})
}

func TestSplitModelIDs(t *testing.T) {
	tests := []struct {
		raw  string
		want []string
	}{
		{"", nil},
		{"a", []string{"a"}},
		{"a, b; c\nd", []string{"a", "b", "c", "d"}},
		{"  x  ,  , y ", []string{"x", "y"}},
	}
	for _, tt := range tests {
		got := splitModelIDs(tt.raw)
		if len(got) != len(tt.want) {
			t.Errorf("splitModelIDs(%q) = %v, want %v", tt.raw, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("splitModelIDs(%q)[%d] = %q, want %q", tt.raw, i, got[i], tt.want[i])
			}
		}
	}
}
