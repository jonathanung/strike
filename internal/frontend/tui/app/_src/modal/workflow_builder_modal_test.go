package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/jonathanung/strike-cli/internal/frontend/host"
	"github.com/jonathanung/strike-cli/internal/frontend/tui/theme"
)

func TestWorkflowBuilderViewUsesThemeComponents(t *testing.T) {
	wf := fakeWorkflows{}
	doc, err := wf.Scaffold("view-flow")
	if err != nil {
		t.Fatal(err)
	}
	doc.Phases[0].Permissions = []host.WorkflowPermission{
		{Permission: "write", Pattern: "*", Action: "deny"},
	}
	modal := newWorkflowBuilderModal(wf, []string{"build"}, doc, host.WorkflowScopeProject, true, theme.Default())
	out := modal.view(80, theme.Default())
	if out == "" {
		t.Fatal("empty view")
	}
	for _, want := range []string{"workflow builder", "view-flow", "Phases", "Permissions", "Preview", "validation"} {
		if !strings.Contains(out, want) {
			t.Errorf("view missing %q in %q", want, out)
		}
	}
}

func TestWorkflowBuilderNarrowLayout(t *testing.T) {
	wf := fakeWorkflows{}
	doc, _ := wf.Scaffold("narrow")
	modal := newWorkflowBuilderModal(wf, nil, doc, host.WorkflowScopeGlobal, true, theme.Default())
	out := modal.view(40, theme.Default())
	if out == "" || !strings.Contains(out, "narrow") {
		t.Fatalf("narrow view = %q", out)
	}
}

func TestWorkflowBuilderAddReorderDeletePhase(t *testing.T) {
	wf := fakeWorkflows{}
	doc, _ := wf.Scaffold("ord")
	modal := newWorkflowBuilderModal(wf, nil, doc, host.WorkflowScopeProject, true, theme.Default())
	modal.focus = wfBuilderFocusPhases
	if _, _ = modal.addPhase(); len(modal.doc.Phases) != 2 {
		t.Fatalf("phases after add = %d", len(modal.doc.Phases))
	}
	modal.phaseCursor = 1
	modal.doc.Phases[1].Name = "second"
	modal.phaseCursor = 0
	if _, _ = modal.reorderPhase(1); modal.doc.Phases[0].Name != "second" {
		t.Fatalf("reorder failed: %#v", modal.doc.Phases)
	}
	modal.phaseCursor = 0
	if _, _ = modal.deleteCurrent(); len(modal.doc.Phases) != 1 {
		t.Fatalf("delete left %d", len(modal.doc.Phases))
	}
	// cannot delete last phase
	if _, _ = modal.deleteCurrent(); len(modal.doc.Phases) != 1 {
		t.Fatal("deleted last phase")
	}
	if !modal.statusErr {
		t.Fatal("expected status about needing one phase")
	}
}

func TestWorkflowBuilderEditFieldsAndPerms(t *testing.T) {
	wf := fakeWorkflows{}
	doc, _ := wf.Scaffold("editme")
	modal := newWorkflowBuilderModal(wf, []string{"build", "plan"}, doc, host.WorkflowScopeProject, true, theme.Default())
	modal.focus = wfBuilderFocusFields
	modal.fieldCursor = wfFieldGate
	if _, _ = modal.beginFieldEdit(); modal.doc.Phases[0].Gate != "user" {
		t.Fatalf("gate cycle = %q", modal.doc.Phases[0].Gate)
	}
	// cycle again to check
	if _, _ = modal.cycleGate(); modal.doc.Phases[0].Gate != "check" {
		t.Fatalf("gate = %q", modal.doc.Phases[0].Gate)
	}
	modal.doc.Phases[0].GateCommand = "make test"
	modal.refreshStatus()
	if modal.statusErr {
		t.Fatalf("status = %s", modal.status)
	}

	if _, _ = modal.addPerm(); len(modal.doc.Phases[0].Permissions) != 1 {
		t.Fatal("add perm failed")
	}
	modal.focus = wfBuilderFocusPerms
	modal.permCursor = 0
	// Edit permission name via text.
	modal.permPart = wfPermPartName
	updated, _ := modal.beginPermEdit()
	ed := updated.(*workflowBuilderModal)
	if !ed.editing || ed.editKind != "perm.permission" {
		t.Fatalf("expected perm name edit, editing=%v kind=%q", ed.editing, ed.editKind)
	}
	ed.input.SetValue("write")
	next, _ := ed.commitTextEdit()
	ed = next.(*workflowBuilderModal)
	if ed.doc.Phases[0].Permissions[0].Permission != "write" {
		t.Fatalf("permission = %q", ed.doc.Phases[0].Permissions[0].Permission)
	}
	// Pattern text edit.
	ed.permPart = wfPermPartPattern
	next, _ = ed.beginPermEdit()
	ed = next.(*workflowBuilderModal)
	ed.input.SetValue("src/**")
	next, _ = ed.commitTextEdit()
	ed = next.(*workflowBuilderModal)
	if ed.doc.Phases[0].Permissions[0].Pattern != "src/**" {
		t.Fatalf("pattern = %q", ed.doc.Phases[0].Permissions[0].Pattern)
	}
	// Action cycles.
	ed.permPart = wfPermPartAction
	next, _ = ed.beginPermEdit()
	ed = next.(*workflowBuilderModal)
	if ed.doc.Phases[0].Permissions[0].Action != "allow" {
		t.Fatalf("action = %q want allow (from deny)", ed.doc.Phases[0].Permissions[0].Action)
	}
}

func TestWorkflowBuilderScopeCycle(t *testing.T) {
	wf := fakeWorkflows{}
	doc, _ := wf.Scaffold("sc")
	modal := newWorkflowBuilderModal(wf, nil, doc, host.WorkflowScopeProject, true, theme.Default())
	modal.cycleScope()
	if modal.scope != host.WorkflowScopeGlobal {
		t.Fatalf("scope = %q", modal.scope)
	}
	modal.cycleScope()
	if modal.scope != host.WorkflowScopeProject {
		t.Fatalf("scope = %q", modal.scope)
	}
}

func TestWorkflowBuilderTextEditName(t *testing.T) {
	wf := fakeWorkflows{}
	doc, _ := wf.Scaffold("old")
	modal := newWorkflowBuilderModal(wf, nil, doc, host.WorkflowScopeProject, true, theme.Default())
	modal.focus = wfBuilderFocusMeta
	modal.metaCursor = wfMetaName
	updated, _ := modal.beginEdit()
	if !updated.(*workflowBuilderModal).editing {
		t.Fatal("expected editing")
	}
	modal.input.SetValue("renamed")
	updated, _ = modal.commitTextEdit()
	if updated.(*workflowBuilderModal).doc.Name != "renamed" {
		t.Fatalf("name = %q", modal.doc.Name)
	}
}

func TestWorkflowBuilderNameLockedWhenEditing(t *testing.T) {
	wf := fakeWorkflows{items: testWorkflowCatalog()}
	doc, _ := wf.Document("plan-implement")
	modal := newWorkflowBuilderModal(wf, nil, doc, host.WorkflowScopeProject, false, theme.Default())
	modal.focus = wfBuilderFocusMeta
	modal.metaCursor = wfMetaName
	updated, _ := modal.beginEdit()
	if updated.(*workflowBuilderModal).editing {
		t.Fatal("name should be locked")
	}
	if !modal.statusErr {
		t.Fatal("expected lock notice")
	}
}

func TestWorkflowBuilderPreviewToggle(t *testing.T) {
	wf := fakeWorkflows{}
	doc, _ := wf.Scaffold("p")
	modal := newWorkflowBuilderModal(wf, nil, doc, host.WorkflowScopeProject, true, theme.Default())
	if !modal.showPreview {
		t.Fatal("preview on by default")
	}
	modal.update(tea.KeyPressMsg{Text: "p", Code: 'p'})
	if modal.showPreview {
		t.Fatal("p should toggle preview off")
	}
}

func TestWorkflowBuilderExistsOverwritePrompt(t *testing.T) {
	wf := fakeWorkflows{saveErr: host.ErrWorkflowExists}
	doc, _ := wf.Scaffold("exists")
	modal := newWorkflowBuilderModal(wf, nil, doc, host.WorkflowScopeProject, true, theme.Default())
	_, cmd := modal.doSave(false, false)
	msg := cmd().(workflowBuilderSavedMsg)
	next, _ := modal.onSaved(msg)
	if next != modal || !modal.overwritePrompt {
		t.Fatal("expected overwrite prompt")
	}
	// n cancels
	next, _ = modal.update(tea.KeyPressMsg{Text: "n", Code: 'n'})
	if next != modal || modal.overwritePrompt {
		t.Fatal("n should clear overwrite prompt")
	}
}
