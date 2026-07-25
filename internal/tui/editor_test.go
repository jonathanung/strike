package tui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

func TestResolveEditorPrefersVisualThenEditorThenFallback(t *testing.T) {
	look := func(name string) (string, error) {
		switch name {
		case "custom-visual":
			return "/bin/custom-visual", nil
		case "custom-editor":
			return "/bin/custom-editor", nil
		case "nvim":
			return "/usr/bin/nvim", nil
		default:
			return "", errors.New("not found")
		}
	}
	bin, args, err := resolveEditor(func(key string) string {
		switch key {
		case "VISUAL":
			return "custom-visual -p"
		case "EDITOR":
			return "custom-editor"
		default:
			return ""
		}
	}, look)
	if err != nil {
		t.Fatal(err)
	}
	if bin != "/bin/custom-visual" || len(args) != 1 || args[0] != "-p" {
		t.Fatalf("visual = %q %v", bin, args)
	}

	bin, args, err = resolveEditor(func(key string) string {
		if key == "EDITOR" {
			return "custom-editor"
		}
		return ""
	}, look)
	if err != nil {
		t.Fatal(err)
	}
	if bin != "/bin/custom-editor" || len(args) != 0 {
		t.Fatalf("editor = %q %v", bin, args)
	}

	bin, args, err = resolveEditor(func(string) string { return "" }, look)
	if err != nil {
		t.Fatal(err)
	}
	if bin != "/usr/bin/nvim" || args != nil {
		t.Fatalf("fallback = %q %v", bin, args)
	}

	_, _, err = resolveEditor(func(string) string { return "" }, func(string) (string, error) {
		return "", errors.New("missing")
	})
	if err == nil || !strings.Contains(err.Error(), "no editor found") {
		t.Fatalf("want missing-editor error, got %v", err)
	}
}

func TestParseVimArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		path    string
		line    int
		wantErr bool
	}{
		{name: "bare", args: nil},
		{name: "path", args: []string{"internal/tui/app.go"}, path: "internal/tui/app.go"},
		{name: "path colon line", args: []string{"app.go:42"}, path: "app.go", line: 42},
		{name: "path plus line", args: []string{"app.go", "+7"}, path: "app.go", line: 7},
		{name: "bad plus", args: []string{"app.go", "7"}, wantErr: true},
		{name: "too many", args: []string{"a", "b", "c"}, wantErr: true},
		{name: "bare plus", args: []string{"+3"}, wantErr: true},
		{name: "zero line", args: []string{"app.go:0"}, path: "app.go:0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path, line, err := parseVimArgs(tt.args)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if path != tt.path || line != tt.line {
				t.Fatalf("got (%q, %d), want (%q, %d)", path, line, tt.path, tt.line)
			}
		})
	}
}

func TestBuildEditorCmdAddsWaitAndLine(t *testing.T) {
	cmd := buildEditorCmd("code", nil, "/tmp/x.go", 12)
	got := cmd.Args
	if len(got) < 3 || got[0] != "code" || got[1] != "-w" || got[len(got)-1] != "/tmp/x.go" {
		t.Fatalf("code args = %v", got)
	}

	cmd = buildEditorCmd("/usr/bin/nvim", nil, "/tmp/x.go", 9)
	got = cmd.Args
	want := []string{"/usr/bin/nvim", "+9", "/tmp/x.go"}
	if len(got) != len(want) {
		t.Fatalf("nvim args = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("nvim args = %v, want %v", got, want)
		}
	}

	cmd = buildEditorCmd("code", []string{"-w"}, "/tmp/x.go", 0)
	if strings.Count(strings.Join(cmd.Args, " "), "-w") != 1 {
		t.Fatalf("duplicate wait flag: %v", cmd.Args)
	}
}

func TestFileChangedSinceDetectsCreateModifyDelete(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")

	before := snapshotFile(path)
	if before.exists || fileChangedSince(path, before) {
		t.Fatal("missing file should not report a change against empty snapshot")
	}

	if err := os.WriteFile(path, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !fileChangedSince(path, before) {
		t.Fatal("create should count as change")
	}

	before = snapshotFile(path)
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(path, []byte("ab"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !fileChangedSince(path, before) {
		t.Fatal("content change should count")
	}

	before = snapshotFile(path)
	if fileChangedSince(path, before) {
		t.Fatal("unchanged file reported as changed")
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if !fileChangedSince(path, before) {
		t.Fatal("delete should count as change")
	}
}

func TestVimCommandUsageError(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	m.composer.SetValue("/vim a b c")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if msg := runAppCmd(t, cmd); msg != nil {
		t.Errorf("unexpected msg %#v", msg)
	}
	assertNoAppOp(t, ops)
	if !m.noticeErr || !strings.Contains(m.notice, "usage: /vim") {
		t.Errorf("notice = %q err=%v", m.notice, m.noticeErr)
	}
}

func TestVimCommandMissingEditor(t *testing.T) {
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	// Force empty PATH so LookPath cannot find nvim/vim/vi.
	t.Setenv("PATH", filepath.Join(t.TempDir(), "empty-bin"))

	m, ops := newAppTestModel(nil, nil)
	m.workDir = t.TempDir()
	m.composer.SetValue("/vim")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if m.composer.Value() != "" {
		t.Errorf("composer = %q, want reset", m.composer.Value())
	}
	msg := runAppCmd(t, cmd)
	finished, ok := msg.(editorFinishedMsg)
	if !ok {
		t.Fatalf("msg = %#v, want editorFinishedMsg", msg)
	}
	updated, cmd = m.Update(finished)
	m = updated.(Model)
	if msg := runAppCmd(t, cmd); msg != nil {
		t.Errorf("unexpected follow-up %#v", msg)
	}
	assertNoAppOp(t, ops)
	if !m.noticeErr || !strings.Contains(m.notice, "no editor found") {
		t.Errorf("notice = %q err=%v", m.notice, m.noticeErr)
	}
}

func TestApplyEditorFinishedSignalsFilesChanged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "edited.go")
	if err := os.WriteFile(path, []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := snapshotFile(path)
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(path, []byte("after"), 0o644); err != nil {
		t.Fatal(err)
	}

	m, ops := newAppTestModel(nil, nil)
	m.workDir = dir
	updated, cmd := m.applyEditorFinished(editorFinishedMsg{
		path:    path,
		display: "edited.go",
		before:  before,
		hadPath: true,
	})
	m = updated.(Model)
	runAppCmd(t, cmd)
	op := receiveAppOp(t, ops)
	got, ok := op.(protocol.FilesChanged)
	if !ok {
		t.Fatalf("op = %#v, want FilesChanged", op)
	}
	if len(got.Paths) != 1 || got.Paths[0] != "edited.go" || got.Reason != editorReasonExternal {
		t.Fatalf("FilesChanged = %#v", got)
	}
}

func TestApplyEditorFinishedUnchangedSkipsOp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "same.go")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := snapshotFile(path)

	m, ops := newAppTestModel(nil, nil)
	updated, cmd := m.applyEditorFinished(editorFinishedMsg{
		path:    path,
		display: "same.go",
		before:  before,
		hadPath: true,
	})
	m = updated.(Model)
	if msg := runAppCmd(t, cmd); msg != nil {
		t.Errorf("unexpected msg %#v", msg)
	}
	assertNoAppOp(t, ops)
	if m.noticeErr || !strings.Contains(m.notice, "unchanged") {
		t.Errorf("notice = %q err=%v", m.notice, m.noticeErr)
	}
}

func TestFilesInvalidatedNotice(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.applyEvent(protocol.FilesInvalidated{
		Paths:  []string{"a.go", "b.go"},
		Reason: editorReasonExternal,
	})
	if m.noticeErr || !strings.Contains(m.notice, "a.go") || !strings.Contains(m.notice, "b.go") {
		t.Errorf("notice = %q err=%v", m.notice, m.noticeErr)
	}
}

func TestHelpListsVim(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.composer.SetValue("/help")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	runAppCmd(t, cmd)
	if !strings.Contains(m.notice, "/vim") {
		t.Errorf("help notice missing /vim: %q", m.notice)
	}
}
