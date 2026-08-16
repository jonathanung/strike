package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

func TestDiagnosticsWindowListsAndOpens(t *testing.T) {
	fake := &fakeLSP{diags: []host.Diagnostic{
		{Path: "/tmp/proj/a.go", Line: 3, Character: 5, Severity: "error", Source: "gopls", Message: "undefined: x"},
		{Path: "/tmp/proj/b.go", Line: 1, Character: 1, Severity: "warning", Message: "unused"},
	}}
	w := newDiagnosticsWindow().bind(fake, "/tmp/proj").resize(48, 6).(diagnosticsWindow)
	view := ansi.Strip(w.view(theme.Default()))
	if !strings.Contains(view, "error") || !strings.Contains(view, "a.go:3:5") {
		t.Fatalf("view = %q", view)
	}
	if !strings.Contains(view, "undefined: x") {
		t.Fatalf("view missing message: %q", view)
	}

	next, cmd := w.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	w = next.(diagnosticsWindow)
	if cmd == nil {
		t.Fatal("enter should open file")
	}
	msg := runAppCmd(t, cmd)
	open, ok := msg.(filesOpenMsg)
	if !ok || open.path != "a.go" || open.line != 3 {
		t.Fatalf("open = %#v, want a.go:3", msg)
	}

	next, _ = w.update(tea.KeyPressMsg{Code: 'j'})
	w = next.(diagnosticsWindow)
	if w.cursor != 1 {
		t.Fatalf("cursor = %d, want 1", w.cursor)
	}
}

func TestDiagnosticsWindowEmptyAndUnavailable(t *testing.T) {
	w := newDiagnosticsWindow().resize(20, 3).(diagnosticsWindow)
	view := ansi.Strip(w.view(theme.Default()))
	if !strings.Contains(view, "unavailable") {
		t.Fatalf("unbound = %q", view)
	}
	w = w.bind(&fakeLSP{}, "/tmp").resize(20, 3).(diagnosticsWindow)
	view = ansi.Strip(w.view(theme.Default()))
	if !strings.Contains(view, "no diagnostics") {
		t.Fatalf("empty = %q", view)
	}
}

func TestDiagnosticsWindowConfigureAndRefresh(t *testing.T) {
	fake := &fakeLSP{diags: []host.Diagnostic{
		{Path: "/p/a.go", Line: 1, Character: 1, Severity: "error", Message: "boom"},
	}}
	r := newWindowRegistry()
	r = configureDiagnosticsWindow(r, "/p", fake)
	var dw diagnosticsWindow
	found := false
	for _, w := range r.windows {
		if d, ok := w.(diagnosticsWindow); ok {
			dw = d
			found = true
			break
		}
	}
	if !found {
		t.Fatal("diagnostics window missing")
	}
	if len(dw.items) != 1 {
		t.Fatalf("items = %d", len(dw.items))
	}
	fake.diags = nil
	r = refreshDiagnosticsWindows(r)
	for _, w := range r.windows {
		if d, ok := w.(diagnosticsWindow); ok {
			if len(d.items) != 0 {
				t.Fatalf("after refresh items = %d", len(d.items))
			}
			return
		}
	}
	t.Fatal("diagnostics window missing after refresh")
}

func TestDiagnosticsSlashFocusesPane(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m.services.LSP = &fakeLSP{}
	m.windows = configureDiagnosticsWindow(m.windows, m.workDir, m.services.LSP)
	m.composer.SetValue("/diagnostics")
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	if cmd != nil {
		runAppCmd(t, cmd)
	}
	if m.windows.active().id() != diagnosticsWindowID {
		t.Fatalf("active = %q, want diagnostics", m.windows.active().id())
	}
	if m.focus != focusRight {
		t.Fatalf("focus = %v, want right", m.focus)
	}
}
