package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

func TestFilesWindowBindLoadsRootAndNavigates(t *testing.T) {
	ff := &fakeFiles{dirs: map[string][]host.DirEntry{
		"": {
			{Name: "pkg", IsDir: true},
			{Name: "README.md", IsDir: false},
		},
		"pkg": {
			{Name: "main.go", IsDir: false},
		},
	}}
	w := newFilesWindow().bind("/tmp/proj", ff).resize(40, 10).(filesWindow)
	plain := ansi.Strip(w.view(theme.Default()))
	if !strings.Contains(plain, "pkg") || !strings.Contains(plain, "README.md") {
		t.Fatalf("root listing missing entries:\n%s", plain)
	}

	// Cursor starts at 0 (pkg). Expand with enter.
	next, _ := w.update(tea.KeyMsg{Type: tea.KeyEnter})
	w = next.(filesWindow)
	plain = ansi.Strip(w.view(theme.Default()))
	if !strings.Contains(plain, "main.go") {
		t.Fatalf("expand did not load children:\n%s", plain)
	}
	rows := ui.FlattenTree(w.nodes)
	if len(rows) != 3 {
		t.Fatalf("visible rows = %d, want 3", len(rows))
	}

	// Move down to main.go.
	next, _ = w.update(tea.KeyMsg{Type: tea.KeyDown})
	w = next.(filesWindow)
	next, _ = w.update(tea.KeyMsg{Type: tea.KeyDown})
	w = next.(filesWindow)
	if w.cursor != 2 {
		t.Errorf("cursor = %d, want 2", w.cursor)
	}

	// Collapse pkg via left while on child — left on leaf is no-op; move to pkg first.
	next, _ = w.update(tea.KeyMsg{Type: tea.KeyUp})
	w = next.(filesWindow)
	next, _ = w.update(tea.KeyMsg{Type: tea.KeyUp})
	w = next.(filesWindow)
	if w.cursor != 0 {
		t.Fatalf("cursor on pkg = %d, want 0", w.cursor)
	}
	next, _ = w.update(tea.KeyMsg{Type: tea.KeyLeft})
	w = next.(filesWindow)
	plain = ansi.Strip(w.view(theme.Default()))
	if strings.Contains(plain, "main.go") {
		t.Fatalf("collapse left pkg visible children:\n%s", plain)
	}
}

func TestFilesWindowWidthSafeAndEmptyStates(t *testing.T) {
	t.Run("nil files", func(t *testing.T) {
		w := newFilesWindow().resize(10, 4).(filesWindow)
		plain := ansi.Strip(w.view(theme.Default()))
		joined := strings.ReplaceAll(plain, "\n", "")
		joined = strings.ReplaceAll(joined, " ", "")
		if !strings.Contains(joined, "unavailable") {
			t.Errorf("got %q", plain)
		}
		for _, line := range strings.Split(plain, "\n") {
			if got := lipgloss.Width(line); got > 10 {
				t.Errorf("width %d > 10: %q", got, line)
			}
		}
	})
	t.Run("empty root", func(t *testing.T) {
		w := newFilesWindow().bind("", &fakeFiles{dirs: map[string][]host.DirEntry{}}).resize(20, 3).(filesWindow)
		plain := ansi.Strip(w.view(theme.Default()))
		if !strings.Contains(plain, "no workspace") {
			t.Errorf("got %q", plain)
		}
	})
	t.Run("list error", func(t *testing.T) {
		w := newFilesWindow().bind("/tmp", &fakeFiles{err: errBoom("boom")}).resize(20, 3).(filesWindow)
		plain := ansi.Strip(w.view(theme.Default()))
		if !strings.Contains(plain, "boom") {
			t.Errorf("got %q", plain)
		}
	})
	t.Run("empty directory", func(t *testing.T) {
		w := newFilesWindow().bind("/tmp", &fakeFiles{dirs: map[string][]host.DirEntry{
			"": {},
		}}).resize(24, 3).(filesWindow)
		plain := ansi.Strip(w.view(theme.Default()))
		if !strings.Contains(plain, "empty directory") {
			t.Errorf("got %q", plain)
		}
	})
}

func TestConfigureFilesWindowOnModelNew(t *testing.T) {
	ff := &fakeFiles{dirs: map[string][]host.DirEntry{
		"": {{Name: "src", IsDir: true}, {Name: "go.mod", IsDir: false}},
	}}
	ops := make(chan protocol.Op, 8)
	events := make(chan protocol.Event)
	services := testServices(nil, nil)
	services.Files = ff
	m := New(ops, events, services, Options{WorkDir: "/workspace/proj"})
	var fw filesWindow
	found := false
	for _, w := range m.windows.windows {
		if f, ok := w.(filesWindow); ok {
			fw = f
			found = true
			break
		}
	}
	if !found {
		t.Fatal("files window missing from registry")
	}
	if fw.root != "/workspace/proj" {
		t.Errorf("root = %q, want /workspace/proj", fw.root)
	}
	if len(fw.nodes) != 2 {
		t.Fatalf("nodes = %d, want 2", len(fw.nodes))
	}
	reg, ok := m.windows.activate(filesWindowID)
	if !ok {
		t.Fatal("activate files failed")
	}
	m.windows = reg
	m.focus = focusRight
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 93, Height: 40})
	plain := ansi.Strip(m.View())
	if !strings.Contains(plain, "src") || !strings.Contains(plain, "go.mod") {
		t.Errorf("split view missing files tree:\n%s", plain)
	}
	if !strings.Contains(plain, "files") {
		t.Errorf("split view missing files title:\n%s", plain)
	}
}

func TestFilesWindowLazyLoadErrorKeepsTree(t *testing.T) {
	ff := &fakeFiles{dirs: map[string][]host.DirEntry{
		"": {{Name: "ok", IsDir: true}, {Name: "bad", IsDir: true}},
		// "ok" missing intentionally so expand errors
	}}
	w := newFilesWindow().bind("/tmp", ff).resize(30, 8).(filesWindow)
	// Expand first dir (ok) — should error
	next, _ := w.update(tea.KeyMsg{Type: tea.KeyEnter})
	w = next.(filesWindow)
	if w.err == "" {
		t.Fatal("expected expand error")
	}
	plain := ansi.Strip(w.view(theme.Default()))
	if !strings.Contains(plain, "ok") || !strings.Contains(plain, "bad") {
		t.Errorf("tree lost after error:\n%s", plain)
	}
}

type errBoom string

func (e errBoom) Error() string { return string(e) }
