package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/host/local"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

func TestApplyDiffModalConfirmAppliesEdit(t *testing.T) {
	work := t.TempDir()
	path := filepath.Join(work, "f.go")
	if err := os.WriteFile(path, []byte("old line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	files := local.NewFiles(work)
	m := newApplyDiffModalEdit(files, "f.go", "old line", "new line", false)

	next, cmd := m.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if next != nil {
		t.Fatalf("modal should close after apply, got %T", next)
	}
	if cmd == nil {
		t.Fatal("expected apply cmd")
	}
	msg := cmd()
	res, ok := msg.(applyDiffResultMsg)
	if !ok {
		t.Fatalf("msg = %T", msg)
	}
	if res.err != "" || res.canceled || res.path != "f.go" || res.count != 1 {
		t.Fatalf("result = %+v", res)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new line\n" {
		t.Fatalf("file = %q", got)
	}
}

func TestApplyDiffModalCancel(t *testing.T) {
	ff := &fakeFiles{files: map[string][]byte{"a.go": []byte("x")}}
	m := newApplyDiffModalEdit(ff, "a.go", "x", "y", false)
	next, cmd := m.update(tea.KeyMsg{Type: tea.KeyEsc})
	if next != nil {
		t.Fatalf("want closed, got %T", next)
	}
	res := cmd().(applyDiffResultMsg)
	if !res.canceled {
		t.Fatalf("result = %+v", res)
	}
	if ff.lastApply.Path != "" {
		t.Fatalf("should not apply on cancel: %+v", ff.lastApply)
	}
}

func TestApplyDiffModalFailureSurfacesError(t *testing.T) {
	ff := &fakeFiles{
		files:    map[string][]byte{"a.go": []byte("hello")},
		applyErr: errBoom("disk full"),
	}
	m := newApplyDiffModalEdit(ff, "a.go", "hello", "hi", false)
	_, cmd := m.update(tea.KeyMsg{Type: tea.KeyEnter})
	res := cmd().(applyDiffResultMsg)
	if res.err == "" || !strings.Contains(res.err, "disk full") {
		t.Fatalf("result = %+v", res)
	}
}

func TestApplyDiffModalPatchPath(t *testing.T) {
	ff := &fakeFiles{}
	patch := "*** Begin Patch\n*** End Patch"
	m := newApplyDiffModalPatch(ff, patch)
	_, cmd := m.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1")})
	res := cmd().(applyDiffResultMsg)
	if res.err != "" || !res.multi {
		t.Fatalf("result = %+v", res)
	}
	if ff.lastPatch != patch {
		t.Fatalf("lastPatch = %q", ff.lastPatch)
	}
}

func TestApplyDiffModalViewShowsDiff(t *testing.T) {
	m := newApplyDiffModalEdit(&fakeFiles{}, "x.go", "foo", "bar", false)
	plain := ansi.Strip(m.view(60, theme.Default()))
	for _, want := range []string{"Apply patch", "x.go", "-foo", "+bar", "apply", "cancel"} {
		if !strings.Contains(plain, want) {
			t.Errorf("missing %q in:\n%s", want, plain)
		}
	}
}

func TestApplySelectedToolOpensModalAndApplies(t *testing.T) {
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "f.go"), []byte("aaa\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, _ := newAppTestModelWithOptions(Options{WorkDir: work})
	ff := local.NewFiles(work)
	m.services.Files = ff

	meta, _ := json.Marshal(map[string]any{
		"oldString": "aaa",
		"newString": "bbb",
		"count":     1,
	})
	m.cells = []cell{&toolCell{
		name:     "edit",
		title:    "f.go",
		metadata: meta,
		done:     true,
	}}
	m.selectedCell = 0
	m.composer.SetValue("")

	handled, cmd := m.applySelectedTool()
	if !handled || cmd != nil {
		t.Fatalf("handled=%v cmd=%v", handled, cmd != nil)
	}
	am, ok := m.modal.(*applyDiffModal)
	if !ok || am == nil {
		t.Fatalf("modal = %T", m.modal)
	}
	next, applyCmd := am.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if next != nil || applyCmd == nil {
		t.Fatalf("confirm next=%T cmd=%v", next, applyCmd != nil)
	}
	msg := applyCmd()
	res, ok := msg.(applyDiffResultMsg)
	if !ok || res.err != "" {
		t.Fatalf("msg = %#v", msg)
	}
	got, err := os.ReadFile(filepath.Join(work, "f.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "bbb\n" {
		t.Fatalf("file = %q", got)
	}

	// Result handler sets notice.
	_ = m.applyApplyDiffResult(res)
	if m.notice == "" || !strings.Contains(m.notice, "applied") {
		t.Fatalf("notice = %q", m.notice)
	}
}

func TestApplySelectedToolKeyRouting(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.services.Files = &fakeFiles{files: map[string][]byte{"f.go": []byte("old")}}
	meta := json.RawMessage(`{"oldString":"old","newString":"new","count":1}`)
	m.cells = []cell{&toolCell{
		name: "edit", title: "f.go", metadata: meta, done: true,
	}}
	m.selectedCell = 0
	m.composer.SetValue("")
	m.focus = focusLeft

	handled, _ := m.handleToolCellKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if !handled {
		t.Fatal("a should be handled")
	}
	if _, ok := m.modal.(*applyDiffModal); !ok {
		t.Fatalf("modal = %T", m.modal)
	}
}

func TestToolCellApplyable(t *testing.T) {
	edit := &toolCell{
		name:     "edit",
		title:    "a.go",
		metadata: json.RawMessage(`{"oldString":"x","newString":"y"}`),
		done:     true,
	}
	if !edit.applyable() {
		t.Fatal("edit with meta should be applyable")
	}
	if edit.done {
		edit.isError = true
		if !edit.applyable() {
			t.Fatal("error edit with meta should still be applyable for re-apply")
		}
	}
	patchArgs, _ := json.Marshal(map[string]string{
		"patch": "*** Begin Patch\n*** Add File: n.go\n+hi\n*** End Patch",
	})
	ap := &toolCell{name: "apply_patch", args: patchArgs, done: true}
	if !ap.applyable() {
		t.Fatal("apply_patch with args should be applyable")
	}
	bash := &toolCell{name: "bash", title: "ls", done: true}
	if bash.applyable() {
		t.Fatal("bash must not be applyable")
	}
}
