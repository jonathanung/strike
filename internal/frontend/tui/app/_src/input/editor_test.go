package tui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

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
		case "nano":
			return "/usr/bin/nano", nil
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

	// nano is a PATH fallback after nvim/vim/vi.
	lookNanoOnly := func(name string) (string, error) {
		if name == "nano" {
			return "/bin/nano", nil
		}
		return "", errors.New("not found")
	}
	bin, args, err = resolveEditor(func(string) string { return "" }, lookNanoOnly)
	if err != nil {
		t.Fatal(err)
	}
	if bin != "/bin/nano" || args != nil {
		t.Fatalf("nano fallback = %q %v", bin, args)
	}

	// Explicit EDITOR=nano wins even when other candidates exist.
	bin, args, err = resolveEditor(func(key string) string {
		if key == "EDITOR" {
			return "nano"
		}
		return ""
	}, look)
	if err != nil {
		t.Fatal(err)
	}
	if bin != "/usr/bin/nano" || args != nil {
		t.Fatalf("EDITOR=nano = %q %v", bin, args)
	}

	_, _, err = resolveEditor(func(string) string { return "" }, func(string) (string, error) {
		return "", errors.New("missing")
	})
	if err == nil || !strings.Contains(err.Error(), "no editor found") {
		t.Fatalf("want missing-editor error, got %v", err)
	}
	if !strings.Contains(err.Error(), "nano") {
		t.Fatalf("missing-editor error should mention nano, got %v", err)
	}
}

func TestParseVimArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		path    string
		line    int
		wantErr bool
		errSub  string
	}{
		{name: "bare", args: nil},
		{name: "path", args: []string{"internal/frontend/tui/app.go"}, path: "internal/frontend/tui/app.go"},
		{name: "at path", args: []string{"@internal/foo.go"}, path: "internal/foo.go"},
		{name: "at path colon line", args: []string{"@app.go:42"}, path: "app.go", line: 42},
		{name: "at path plus line", args: []string{"@app.go", "+7"}, path: "app.go", line: 7},
		{name: "path colon line", args: []string{"app.go:42"}, path: "app.go", line: 42},
		{name: "path plus line", args: []string{"app.go", "+7"}, path: "app.go", line: 7},
		{name: "bad plus", args: []string{"app.go", "7"}, wantErr: true},
		{name: "too many", args: []string{"a", "b", "c"}, wantErr: true},
		{name: "bare plus", args: []string{"+3"}, wantErr: true},
		{name: "bare at", args: []string{"@"}, wantErr: true, errSub: "unresolved"},
		{name: "at escape", args: []string{"@../secret"}, wantErr: true, errSub: "unresolved"},
		{name: "zero line", args: []string{"app.go:0"}, path: "app.go:0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path, line, err := parseVimArgs(tt.args)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				if tt.errSub != "" && !strings.Contains(err.Error(), tt.errSub) {
					t.Fatalf("err = %v, want substring %q", err, tt.errSub)
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
	wantCode := []string{"code", "-w", "-g", "/tmp/x.go:12"}
	if len(got) != len(wantCode) {
		t.Fatalf("code args = %v, want %v", got, wantCode)
	}
	for i := range wantCode {
		if got[i] != wantCode[i] {
			t.Fatalf("code args = %v, want %v", got, wantCode)
		}
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

	cmd = buildEditorCmd("/usr/bin/nano", nil, "/tmp/x.go", 9)
	got = cmd.Args
	wantNano := []string{"/usr/bin/nano", "+9", "/tmp/x.go"}
	if len(got) != len(wantNano) {
		t.Fatalf("nano args = %v, want %v", got, wantNano)
	}
	for i := range wantNano {
		if got[i] != wantNano[i] {
			t.Fatalf("nano args = %v, want %v", got, wantNano)
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
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	if msg := runAppCmd(t, cmd); msg != nil {
		t.Errorf("unexpected msg %#v", msg)
	}
	assertNoAppOp(t, ops)
	if !m.noticeErr || !strings.Contains(m.notice, "usage: /vim") {
		t.Errorf("notice = %q err=%v", m.notice, m.noticeErr)
	}
	if got := m.composer.Value(); got != "/vim a b c" {
		t.Errorf("composer = %q, want invalid command retained", got)
	}
}

func TestNanoCommandUsageError(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	m.composer.SetValue("/nano a b c")
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	if msg := runAppCmd(t, cmd); msg != nil {
		t.Errorf("unexpected msg %#v", msg)
	}
	assertNoAppOp(t, ops)
	if !m.noticeErr || !strings.Contains(m.notice, "usage: /nano") {
		t.Errorf("notice = %q err=%v", m.notice, m.noticeErr)
	}
}

func TestVimCommandUnresolvedAtMention(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	m.composer.SetValue("/vim @")
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	if msg := runAppCmd(t, cmd); msg != nil {
		t.Errorf("unexpected msg %#v", msg)
	}
	assertNoAppOp(t, ops)
	if !m.noticeErr || !strings.Contains(m.notice, "unresolved") {
		t.Errorf("notice = %q err=%v, want unresolved mention", m.notice, m.noticeErr)
	}
}

func TestResolveNano(t *testing.T) {
	bin, err := resolveNano(func(name string) (string, error) {
		if name == "nano" {
			return "/usr/bin/nano", nil
		}
		return "", errors.New("not found")
	})
	if err != nil {
		t.Fatal(err)
	}
	if bin != "/usr/bin/nano" {
		t.Fatalf("bin = %q", bin)
	}
	_, err = resolveNano(func(string) (string, error) {
		return "", errors.New("missing")
	})
	if err == nil || !strings.Contains(err.Error(), "nano not found") {
		t.Fatalf("want nano-not-found error, got %v", err)
	}
	if !strings.Contains(err.Error(), "/vim") {
		t.Fatalf("error should mention /vim fallback, got %v", err)
	}
}

func TestVimCommandMissingEditor(t *testing.T) {
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	// Force empty PATH so LookPath cannot find nvim/vim/vi/nano.
	t.Setenv("PATH", filepath.Join(t.TempDir(), "empty-bin"))

	m, ops := newAppTestModel(nil, nil)
	m.workDir = t.TempDir()
	m.composer.SetValue("/vim")
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	if m.composer.Value() != "" {
		t.Errorf("composer = %q, want reset", m.composer.Value())
	}
	if msg := runAppCmd(t, cmd); msg != nil {
		t.Errorf("unexpected msg %#v", msg)
	}
	assertNoAppOp(t, ops)
	if !m.noticeErr || !strings.Contains(m.notice, "no editor found") {
		t.Errorf("notice = %q err=%v", m.notice, m.noticeErr)
	}
}

func TestNanoCommandMissingNano(t *testing.T) {
	// Even if EDITOR is set, /nano requires nano on PATH.
	t.Setenv("VISUAL", "nvim")
	t.Setenv("EDITOR", "nvim")
	t.Setenv("PATH", filepath.Join(t.TempDir(), "empty-bin"))

	m, ops := newAppTestModel(nil, nil)
	m.workDir = t.TempDir()
	m.composer.SetValue("/nano note.txt")
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	if m.composer.Value() != "" {
		t.Errorf("composer = %q, want reset", m.composer.Value())
	}
	if msg := runAppCmd(t, cmd); msg != nil {
		t.Errorf("unexpected msg %#v", msg)
	}
	assertNoAppOp(t, ops)
	if !m.noticeErr || !strings.Contains(m.notice, "nano not found") {
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

func TestHelpListsVimAndNano(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.composer.SetValue("/help")
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	runAppCmd(t, cmd)
	help, ok := m.modal.(*helpModal)
	if !ok {
		t.Fatalf("/help modal = %T, want helpModal", m.modal)
	}
	foundVim, foundNano := false, false
	for _, entry := range help.entries {
		if strings.HasPrefix(entry.Label, "/vim") {
			foundVim = true
		}
		if strings.HasPrefix(entry.Label, "/nano") {
			foundNano = true
		}
	}
	if !foundVim {
		t.Error("help catalog missing /vim")
	}
	if !foundNano {
		t.Error("help catalog missing /nano")
	}
}

func TestComposerExternalEditorMissingEditorKeepsDraft(t *testing.T) {
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	t.Setenv("PATH", filepath.Join(t.TempDir(), "empty-bin"))

	m, ops := newAppTestModel(nil, nil)
	m.composer.SetValue("keep this draft")
	updated, cmd := m.Update(tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl})
	m = updated.(Model)
	msg := runAppCmd(t, cmd)
	finished, ok := msg.(composerEditorFinishedMsg)
	if !ok {
		t.Fatalf("msg = %#v, want composerEditorFinishedMsg", msg)
	}
	if finished.launchErr == "" || !strings.Contains(finished.launchErr, "no editor found") {
		t.Fatalf("launchErr = %q", finished.launchErr)
	}
	updated, _ = m.Update(finished)
	m = updated.(Model)
	if m.composer.Value() != "keep this draft" {
		t.Errorf("composer = %q, want draft preserved", m.composer.Value())
	}
	if !m.noticeErr || !strings.Contains(m.notice, "no editor found") {
		t.Errorf("notice = %q err=%v", m.notice, m.noticeErr)
	}
	assertNoAppOp(t, ops)
}

func TestApplyComposerEditorFinishedReplacesDraft(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	m.composer.SetValue("before")
	updated, cmd := m.applyComposerEditorFinished(composerEditorFinishedMsg{
		text: "after\nmultiline",
	})
	m = updated.(Model)
	if msg := runAppCmd(t, cmd); msg != nil {
		t.Errorf("unexpected msg %#v", msg)
	}
	if m.composer.Value() != "after\nmultiline" {
		t.Errorf("composer = %q", m.composer.Value())
	}
	assertNoAppOp(t, ops)
}

func TestApplyComposerEditorFinishedReadErrorKeepsDraft(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.composer.SetValue("original")
	updated, _ := m.applyComposerEditorFinished(composerEditorFinishedMsg{
		readErr: "read temp: gone",
	})
	m = updated.(Model)
	if m.composer.Value() != "original" {
		t.Errorf("composer = %q, want original", m.composer.Value())
	}
	if !m.noticeErr || !strings.Contains(m.notice, "read temp") {
		t.Errorf("notice = %q err=%v", m.notice, m.noticeErr)
	}
}

func TestApplyComposerEditorFinishedAppliesTextDespiteProcessError(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.composer.SetValue("before")
	updated, _ := m.applyComposerEditorFinished(composerEditorFinishedMsg{
		text: "saved anyway",
		err:  errors.New("exit status 1"),
	})
	m = updated.(Model)
	if m.composer.Value() != "saved anyway" {
		t.Errorf("composer = %q", m.composer.Value())
	}
	if !m.noticeErr || !strings.Contains(m.notice, "exit status 1") {
		t.Errorf("notice = %q err=%v", m.notice, m.noticeErr)
	}
}

func TestComposerExternalEditorIgnoredWhenRightFocused(t *testing.T) {
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	t.Setenv("PATH", filepath.Join(t.TempDir(), "empty-bin"))

	m, _ := newAppTestModel(nil, nil)
	m.composer.SetValue("draft")
	m = updateApp(t, m, tea.KeyPressMsg{Code: 'l', Mod: tea.ModCtrl})
	if m.focus != focusRight {
		t.Fatalf("focus = %v, want right", m.focus)
	}
	updated, cmd := m.Update(tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl})
	m = updated.(Model)
	if msg := runAppCmd(t, cmd); msg != nil {
		if _, ok := msg.(composerEditorFinishedMsg); ok {
			t.Fatalf("right focus launched composer editor: %#v", msg)
		}
	}
	if m.composer.Value() != "draft" {
		t.Errorf("composer = %q", m.composer.Value())
	}
}
