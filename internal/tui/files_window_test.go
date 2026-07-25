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

func TestFilesWindowEnterOnLeafEmitsOpenMsg(t *testing.T) {
	ff := &fakeFiles{dirs: map[string][]host.DirEntry{
		"": {
			{Name: "pkg", IsDir: true},
			{Name: "README.md", IsDir: false},
			{Name: "main.go", IsDir: false},
		},
		"pkg": {},
	}}
	w := newFilesWindow().bind("/tmp/proj", ff).resize(40, 10).(filesWindow)

	// Cursor on pkg (dir) — enter expands, no open msg.
	next, cmd := w.update(tea.KeyMsg{Type: tea.KeyEnter})
	w = next.(filesWindow)
	if cmd != nil {
		t.Fatal("enter on dir should not return a cmd")
	}

	// Move to README.md (cursor 1 after collapse still shows pkg + files)
	next, _ = w.update(tea.KeyMsg{Type: tea.KeyLeft}) // collapse pkg
	w = next.(filesWindow)
	next, _ = w.update(tea.KeyMsg{Type: tea.KeyDown})
	w = next.(filesWindow)
	if w.cursor != 1 {
		t.Fatalf("cursor = %d, want 1 (README.md)", w.cursor)
	}
	next, cmd = w.update(tea.KeyMsg{Type: tea.KeyEnter})
	w = next.(filesWindow)
	if cmd == nil {
		t.Fatal("enter on leaf should return open cmd")
	}
	msg := runAppCmd(t, cmd)
	om, ok := msg.(filesOpenMsg)
	if !ok || om.path != "README.md" {
		t.Fatalf("open msg = %#v, want filesOpenMsg{path:README.md}", msg)
	}

	// main.go via right key
	next, _ = w.update(tea.KeyMsg{Type: tea.KeyDown})
	w = next.(filesWindow)
	next, cmd = w.update(tea.KeyMsg{Type: tea.KeyRight})
	_ = next
	if cmd == nil {
		t.Fatal("right on leaf should return open cmd")
	}
	msg = runAppCmd(t, cmd)
	om, ok = msg.(filesOpenMsg)
	if !ok || om.path != "main.go" {
		t.Fatalf("open msg = %#v, want filesOpenMsg{path:main.go}", msg)
	}
}

func TestFilesExplorerOpenMarkdownAndCode(t *testing.T) {
	ff := &fakeFiles{
		dirs: map[string][]host.DirEntry{
			"": {
				{Name: "README.md", IsDir: false},
				{Name: "main.go", IsDir: false},
			},
		},
		files: map[string][]byte{
			"README.md": []byte("# Title\n\nbody"),
		},
	}
	ops := make(chan protocol.Op, 8)
	events := make(chan protocol.Event)
	services := testServices(nil, nil)
	services.Files = ff
	m := New(ops, events, services, Options{WorkDir: "/workspace/proj", VimMode: VimModeTakeover})
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 93, Height: 40})

	reg, ok := m.windows.activate(filesWindowID)
	if !ok {
		t.Fatal("activate files failed")
	}
	m.windows = reg
	m.focus = focusRight

	// Open markdown leaf → markdown window.
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if cmd != nil {
		msg := runAppCmd(t, cmd)
		if msg != nil {
			updated, cmd = m.Update(msg)
			m = updated.(Model)
			if cmd != nil {
				runAppCmd(t, cmd)
			}
		}
	}
	if m.windows.active().id() != markdownWindowID {
		t.Fatalf("active = %q, want markdown after opening .md", m.windows.active().id())
	}
	mw := m.windows.active().(markdownWindow)
	if mw.path != "README.md" {
		t.Errorf("md path = %q, want README.md", mw.path)
	}

	// Re-activate files, move to main.go, open → vim path (missing editor notice).
	reg, ok = m.windows.activate(filesWindowID)
	if !ok {
		t.Fatal("re-activate files failed")
	}
	m.windows = reg
	m.focus = focusRight
	// Cursor still on README.md; move to main.go.
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(Model)
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	t.Setenv("PATH", t.TempDir())
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if cmd != nil {
		msg := runAppCmd(t, cmd)
		if msg != nil {
			updated, cmd = m.Update(msg)
			m = updated.(Model)
			if cmd != nil {
				runAppCmd(t, cmd)
			}
		}
	}
	if !m.noticeErr || !strings.Contains(m.notice, "no editor found") {
		t.Fatalf("want missing-editor notice for .go, got err=%v notice=%q", m.noticeErr, m.notice)
	}
}

func TestOpenFilesExplorerPathRoutes(t *testing.T) {
	m, _ := newAppTestModelWithOptions(Options{WorkDir: t.TempDir(), VimMode: VimModeTakeover})
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.services.Files = &fakeFiles{files: map[string][]byte{
		"doc.MD": []byte("# Hi"),
	}}

	updated, cmd := m.openFilesExplorerPath("doc.MD")
	mm := updated.(Model)
	if cmd != nil {
		runAppCmd(t, cmd)
	}
	if mm.windows.active().id() != markdownWindowID {
		t.Fatalf("active = %q, want markdown for .MD", mm.windows.active().id())
	}

	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	t.Setenv("PATH", t.TempDir())
	updated, _ = m.openFilesExplorerPath("lib/x.go")
	mm = updated.(Model)
	if !mm.noticeErr || !strings.Contains(mm.notice, "no editor found") {
		t.Fatalf("want editor notice for code file, got err=%v notice=%q", mm.noticeErr, mm.notice)
	}

	updated, cmd = m.openFilesExplorerPath("  ")
	if cmd != nil {
		t.Fatal("blank path should not emit cmd")
	}
	_ = updated
}

type errBoom string

func (e errBoom) Error() string { return string(e) }
