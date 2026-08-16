package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/frontend/host/local"
	"github.com/jonathanung/strike-cli/internal/frontend/tui/theme"
)

func TestApplyDiffModalConfirmAppliesEdit(t *testing.T) {
	work := t.TempDir()
	path := filepath.Join(work, "f.go")
	if err := os.WriteFile(path, []byte("old line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	files := local.NewFiles(work)
	m := newApplyDiffModalEdit(files, "f.go", "old line", "new line", false)

	next, cmd := m.update(tea.KeyPressMsg{Text: "y"})
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
	next, cmd := m.update(tea.KeyPressMsg{Code: tea.KeyEsc})
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
	_, cmd := m.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	res := cmd().(applyDiffResultMsg)
	if res.err == "" || !strings.Contains(res.err, "disk full") {
		t.Fatalf("result = %+v", res)
	}
}

func TestApplyDiffModalPatchPath(t *testing.T) {
	ff := &fakeFiles{}
	patch := "*** Begin Patch\n*** End Patch"
	m := newApplyDiffModalPatch(ff, patch)
	_, cmd := m.update(tea.KeyPressMsg{Text: "1"})
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
	for _, want := range []string{"Apply patch", "x.go", "foo", "bar", "apply", "cancel"} {
		if !strings.Contains(plain, want) {
			t.Errorf("missing %q in:\n%s", want, plain)
		}
	}
	// Markers may sit after a line-number gutter; still require +/- intent.
	if !strings.Contains(plain, "-") || !strings.Contains(plain, "+") {
		t.Errorf("missing diff markers in:\n%s", plain)
	}
}

func TestApplyDiffModalScrollsTallDiff(t *testing.T) {
	var oldB, newB strings.Builder
	for i := 0; i < 20; i++ {
		fmt.Fprintf(&oldB, "old-line-%d\n", i)
		fmt.Fprintf(&newB, "new-line-%d\n", i)
	}
	oldS := strings.TrimSuffix(oldB.String(), "\n")
	newS := strings.TrimSuffix(newB.String(), "\n")
	m := newApplyDiffModalEdit(&fakeFiles{}, "big.go", oldS, newS, false)

	plain0 := ansi.Strip(m.view(72, theme.Default()))
	if !strings.Contains(plain0, "scroll") {
		t.Fatalf("tall diff should advertise scroll in hint/more: %q", plain0)
	}
	if !strings.Contains(plain0, "old-line-0") {
		t.Fatalf("initial window should show start: %q", plain0)
	}

	// First down snaps from change-preferring auto window, then +1.
	start := m.diffOffset // 0 = auto
	next, cmd := m.update(tea.KeyPressMsg{Code: tea.KeyDown})
	if next != m || cmd != nil {
		t.Fatalf("scroll down next=%T cmd=%v", next, cmd != nil)
	}
	if !m.diffScrolled {
		t.Fatal("first scroll should mark diffScrolled")
	}
	if m.diffOffset < start {
		t.Fatalf("diffOffset after down = %d, want >= auto start", m.diffOffset)
	}
	plain1 := ansi.Strip(m.view(72, theme.Default()))
	if strings.Contains(plain1, "old-line-0") && m.diffOffset >= diffPreviewMaxLinesModal {
		// After enough downs the first line leaves the window.
	}
	// Page down jumps by a window.
	before := m.diffOffset
	_, _ = m.update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if m.diffOffset <= before {
		t.Fatalf("pgdown offset %d, want > %d", m.diffOffset, before)
	}
	// Up from mid restores toward start.
	_, _ = m.update(tea.KeyPressMsg{Code: tea.KeyUp})
	if m.diffOffset >= before+diffPreviewMaxLinesModal {
		t.Fatalf("up should decrease offset, got %d", m.diffOffset)
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
	next, applyCmd := am.update(tea.KeyPressMsg{Text: "y"})
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

	// Default bind is alt+a (#693); bare "a" must not open apply.
	handled, _ := m.handleToolCellKeys(tea.KeyPressMsg{Text: "a"})
	if handled {
		t.Fatal("bare a must not handle tool-apply")
	}
	if m.modal != nil {
		t.Fatalf("bare a opened modal %T", m.modal)
	}
	handled, _ = m.handleToolCellKeys(tea.KeyPressMsg{Code: 'a', Mod: tea.ModAlt})
	if !handled {
		t.Fatal("alt+a should be handled")
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
