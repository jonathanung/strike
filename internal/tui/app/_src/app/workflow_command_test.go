package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

// fakeWorkflows is a minimal host.Workflows for slash-command tests.
type fakeWorkflows struct {
	items    []host.WorkflowSummary
	docs     map[string]host.WorkflowDocument
	saveErr  error
	saved    []host.WorkflowDocument
	savePath string
}

func (f fakeWorkflows) List() []host.WorkflowSummary {
	out := make([]host.WorkflowSummary, len(f.items))
	copy(out, f.items)
	return out
}

func (f fakeWorkflows) Get(name string) (host.WorkflowSummary, bool) {
	for _, w := range f.items {
		if w.Name == name {
			return w, true
		}
	}
	return host.WorkflowSummary{}, false
}

func (f fakeWorkflows) Document(name string) (host.WorkflowDocument, bool) {
	if f.docs != nil {
		if d, ok := f.docs[name]; ok {
			return d, true
		}
	}
	sum, ok := f.Get(name)
	if !ok {
		return host.WorkflowDocument{}, false
	}
	return workflowSummaryToDocument(sum), true
}

func (f fakeWorkflows) Scaffold(name string) (host.WorkflowDocument, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return host.WorkflowDocument{}, errors.New("empty workflow name")
	}
	return host.WorkflowDocument{
		SchemaVersion: 1,
		Name:          name,
		Description:   "TODO: describe this workflow",
		Phases: []host.WorkflowPhaseDocument{
			{Name: "step-one", Agent: "build", Gate: "agent"},
		},
	}, nil
}

func (f fakeWorkflows) Validate(doc host.WorkflowDocument) error {
	if strings.TrimSpace(doc.Name) == "" {
		return errors.New("name: empty")
	}
	if len(doc.Phases) == 0 {
		return errors.New("phases: workflow has no phases")
	}
	for i, p := range doc.Phases {
		if strings.TrimSpace(p.Name) == "" {
			return fmt.Errorf("phases[%d].name: empty", i)
		}
		if p.Gate == "check" && strings.TrimSpace(p.GateCommand) == "" {
			return fmt.Errorf("phases[%d].exit: check gate requires command", i)
		}
	}
	return nil
}

func (f fakeWorkflows) Format(doc host.WorkflowDocument) (string, error) {
	var b strings.Builder
	b.WriteString("{\n")
	fmt.Fprintf(&b, "  \"schemaVersion\": %d,\n", doc.SchemaVersion)
	fmt.Fprintf(&b, "  \"name\": %q,\n", doc.Name)
	fmt.Fprintf(&b, "  \"description\": %q,\n", doc.Description)
	fmt.Fprintf(&b, "  \"phases\": [\n")
	for i, p := range doc.Phases {
		fmt.Fprintf(&b, "    {\"name\": %q, \"agent\": %q, \"gate\": %q, \"command\": %q, \"context\": %q, \"perms\": %d}",
			p.Name, p.Agent, p.Gate, p.GateCommand, p.Context, len(p.Permissions))
		if i+1 < len(doc.Phases) {
			b.WriteString(",")
		}
		b.WriteByte('\n')
	}
	b.WriteString("  ]\n}\n")
	return b.String(), nil
}

func (f fakeWorkflows) PhaseGrants(doc host.WorkflowDocument, phaseIndex int) []host.WorkflowPermission {
	if phaseIndex < 0 || phaseIndex >= len(doc.Phases) {
		return nil
	}
	return append([]host.WorkflowPermission(nil), doc.Phases[phaseIndex].Permissions...)
}

func (f fakeWorkflows) Save(doc host.WorkflowDocument, scope string, force bool) (string, error) {
	if f.saveErr != nil {
		return "", f.saveErr
	}
	if err := f.Validate(doc); err != nil {
		return "", fmt.Errorf("%w: %v", host.ErrWorkflowInvalid, err)
	}
	path := f.savePath
	if path == "" {
		path = "/tmp/" + scope + "/" + doc.Name + ".json"
	}
	return path, nil
}

func testWorkflowCatalog() []host.WorkflowSummary {
	return []host.WorkflowSummary{
		{
			Name:        "plan-implement",
			Description: "Read-only planning phase, then implementation",
			Source:      host.WorkflowSourceBuiltin,
			Fingerprint: "fp-plan",
			Valid:       true,
			Phases: []host.WorkflowPhaseSummary{
				{
					Name: "plan", Agent: "plan", Gate: "user",
					Permissions: []host.WorkflowPermission{
						{Permission: "write", Pattern: "*", Action: "deny"},
						{Permission: "edit", Pattern: "*", Action: "deny"},
					},
				},
				{Name: "implement", Agent: "build", Gate: "agent"},
			},
		},
		{
			Name:            "broken",
			Source:          host.WorkflowSourceGlobal,
			Valid:           false,
			ValidationError: "no phases",
		},
		{
			Name:   "project-flow",
			Source: host.WorkflowSourceProject,
			Valid:  true,
			Phases: []host.WorkflowPhaseSummary{
				{Name: "only", Gate: "agent"},
			},
		},
		{
			Name:   "plugin-flow",
			Source: host.WorkflowSourcePlugin,
			Valid:  true,
			Phases: []host.WorkflowPhaseSummary{
				{Name: "step", Gate: "check", GateCommand: "make test"},
			},
		},
	}
}

func TestWorkflowSlashListInspectStartStop(t *testing.T) {
	ops := make(chan protocol.Op, 4)
	m := New(ops, nil, host.Services{
		Workflows: fakeWorkflows{items: testWorkflowCatalog()},
	})

	next, _ := m.handleCommand("/workflow list")
	m = next.(Model)
	if !strings.Contains(m.notice, "plan-implement") || !strings.Contains(m.notice, "builtin") {
		t.Fatalf("list notice = %q", m.notice)
	}
	if !strings.Contains(m.notice, "project-flow") || !strings.Contains(m.notice, "plugin-flow") {
		t.Fatalf("list missing sources: %q", m.notice)
	}
	if !strings.Contains(m.notice, "broken") || !strings.Contains(m.notice, "invalid") {
		t.Fatalf("list missing invalid: %q", m.notice)
	}

	next, _ = m.handleCommand("/workflow inspect plan-implement")
	m = next.(Model)
	if !strings.Contains(m.notice, "plan@plan") || !strings.Contains(m.notice, "deny write") {
		t.Fatalf("inspect notice = %q", m.notice)
	}
	if !strings.Contains(m.notice, "fp=fp-plan") {
		t.Fatalf("inspect missing fingerprint: %q", m.notice)
	}

	next, _ = m.handleCommand("/workflow start plan-implement")
	m = next.(Model)
	modal, ok := m.modal.(*workflowStartModal)
	if !ok {
		t.Fatalf("modal = %T, want *workflowStartModal", m.modal)
	}
	// Confirm start → StartWorkflow op.
	updated, cmd := modal.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if updated != nil {
		t.Fatal("confirm should close modal")
	}
	if cmd == nil {
		t.Fatal("expected start cmd")
	}
	msg := cmd()
	res, ok := msg.(workflowStartResultMsg)
	if !ok || !res.started || res.name != "plan-implement" {
		t.Fatalf("result = %#v", msg)
	}
	select {
	case op := <-ops:
		sw, ok := op.(protocol.StartWorkflow)
		if !ok || sw.Name != "plan-implement" {
			t.Fatalf("op = %#v", op)
		}
	default:
		t.Fatal("expected StartWorkflow op")
	}
	m.modal = nil // modal update closes overlay; Model.Update would clear it

	// Invalid cannot activate.
	next, _ = m.handleCommand("/workflow start broken")
	m = next.(Model)
	if _, ok := m.modal.(*workflowStartModal); ok {
		t.Fatal("invalid start opened confirm modal")
	}
	if !m.noticeErr || !strings.Contains(m.notice, "invalid") {
		t.Fatalf("invalid start notice = %q err=%v", m.notice, m.noticeErr)
	}

	// Stop with no active phase.
	next, _ = m.handleCommand("/workflow stop")
	m = next.(Model)
	if m.noticeErr || !strings.Contains(m.notice, "no active") {
		t.Fatalf("stop idle notice = %q", m.notice)
	}

	// Stop when active.
	m.phaseWorkflow = "plan-implement"
	m.phaseName = "plan"
	next, cmd = m.handleCommand("/workflow stop")
	m = next.(Model)
	if cmd == nil {
		t.Fatal("expected stop cmd")
	}
	_ = cmd()
	select {
	case op := <-ops:
		if _, ok := op.(protocol.StopWorkflow); !ok {
			t.Fatalf("op = %#v, want StopWorkflow", op)
		}
	default:
		t.Fatal("expected StopWorkflow op")
	}
}

func TestWorkflowCommandNilService(t *testing.T) {
	m := New(nil, nil, host.Services{})
	next, _ := m.handleCommand("/workflow list")
	m = next.(Model)
	if !m.noticeErr || !strings.Contains(m.notice, "unavailable") {
		t.Fatalf("nil workflows notice = %q", m.notice)
	}
}

func TestWorkflowStartBlockedMidTurn(t *testing.T) {
	m := New(nil, nil, host.Services{
		Workflows: fakeWorkflows{items: testWorkflowCatalog()},
	})
	m.turnRunning = true
	next, _ := m.handleCommand("/workflow start plan-implement")
	m = next.(Model)
	if m.modal != nil {
		t.Fatal("start mid-turn opened modal")
	}
	if !m.noticeErr || !strings.Contains(m.notice, "turn is running") {
		t.Fatalf("notice = %q", m.notice)
	}
}

func TestWorkflowStartModalCancelNoOp(t *testing.T) {
	ops := make(chan protocol.Op, 1)
	w := testWorkflowCatalog()[0]
	modal := newWorkflowStartModal(w, ops, protocol.AutonomySupervised)
	updated, cmd := modal.update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if updated != nil {
		t.Fatal("esc should close")
	}
	msg := cmd()
	res := msg.(workflowStartResultMsg)
	if !res.canceled {
		t.Fatalf("result = %#v", res)
	}
	select {
	case op := <-ops:
		t.Fatalf("unexpected op on cancel: %#v", op)
	default:
	}
}

func TestFormatWorkflowInspectSources(t *testing.T) {
	for _, src := range []string{
		host.WorkflowSourceBuiltin,
		host.WorkflowSourceGlobal,
		host.WorkflowSourceProject,
		host.WorkflowSourcePlugin,
	} {
		s := formatWorkflowInspect(host.WorkflowSummary{
			Name: "x", Source: src, Valid: true,
			Phases: []host.WorkflowPhaseSummary{{Name: "a", Gate: "agent"}},
		})
		if !strings.Contains(s, src) {
			t.Errorf("inspect missing source %q: %s", src, s)
		}
	}
}

func TestPhaseChangedTracksIdentityAndClear(t *testing.T) {
	m := New(nil, nil, host.Services{})
	m.applyEvent(protocol.PhaseChanged{
		Workflow:    "plan-implement",
		Phase:       "plan",
		Index:       0,
		Gate:        "user",
		Source:      "builtin",
		Fingerprint: "abc",
	})
	if m.phaseWorkflow != "plan-implement" || m.phaseGate != "user" || m.phaseSource != "builtin" || m.phaseFingerprint != "abc" {
		t.Fatalf("phase state = %q/%q/%q/%q", m.phaseWorkflow, m.phaseGate, m.phaseSource, m.phaseFingerprint)
	}
	m.applyEvent(protocol.PhaseChanged{})
	if m.phaseName != "" || m.phaseWorkflow != "" || m.phaseGate != "" || m.phaseSource != "" || m.phaseFingerprint != "" || m.phaseStatus != "" {
		t.Fatalf("clear left residue: name=%q wf=%q gate=%q src=%q fp=%q st=%q",
			m.phaseName, m.phaseWorkflow, m.phaseGate, m.phaseSource, m.phaseFingerprint, m.phaseStatus)
	}
}

func TestActivePhaseGrantsLabel(t *testing.T) {
	m := New(nil, nil, host.Services{
		Workflows: fakeWorkflows{items: testWorkflowCatalog()},
	})
	if got := m.activePhaseGrantsLabel(); got != "" {
		t.Fatalf("idle grants = %q", got)
	}
	m.phaseWorkflow = "plan-implement"
	m.phaseName = "plan"
	got := m.activePhaseGrantsLabel()
	if !strings.Contains(got, "deny write") {
		t.Fatalf("grants = %q", got)
	}
	m.phaseStatus = protocol.PhaseStatusMissing
	if got := m.activePhaseGrantsLabel(); got != "none (recovery)" {
		t.Fatalf("recovery grants = %q", got)
	}
}

func TestWorkflowNewOpensBuilder(t *testing.T) {
	m := New(nil, nil, host.Services{
		Workflows: fakeWorkflows{items: testWorkflowCatalog()},
		Agents:    []string{"build", "plan"},
	})
	// Preserve composer-adjacent state: notice cleared, modal set.
	m.setNotice("prior", false)
	next, _ := m.handleCommand("/workflow new demo-flow")
	m = next.(Model)
	ed, ok := m.modal.(*workflowBuilderModal)
	if !ok {
		t.Fatalf("modal type = %T", m.modal)
	}
	if ed.doc.Name != "demo-flow" || !ed.creating {
		t.Fatalf("doc=%+v creating=%v", ed.doc, ed.creating)
	}
	if ed.scope != host.WorkflowScopeProject {
		t.Fatalf("scope = %q", ed.scope)
	}
}

func TestWorkflowEditOpensBuilder(t *testing.T) {
	m := New(nil, nil, host.Services{
		Workflows: fakeWorkflows{items: testWorkflowCatalog()},
	})
	next, _ := m.handleCommand("/workflow edit plan-implement")
	m = next.(Model)
	ed, ok := m.modal.(*workflowBuilderModal)
	if !ok {
		t.Fatalf("modal type = %T", m.modal)
	}
	if ed.doc.Name != "plan-implement" || ed.creating {
		t.Fatalf("doc=%+v creating=%v", ed.doc, ed.creating)
	}
	if len(ed.doc.Phases) != 2 {
		t.Fatalf("phases = %d", len(ed.doc.Phases))
	}
	// Builtin defaults to project override scope.
	if ed.scope != host.WorkflowScopeProject {
		t.Fatalf("scope = %q", ed.scope)
	}
}

func TestWorkflowEditUnknown(t *testing.T) {
	m := New(nil, nil, host.Services{
		Workflows: fakeWorkflows{items: testWorkflowCatalog()},
	})
	next, _ := m.handleCommand("/workflow edit missing-flow")
	m = next.(Model)
	if m.modal != nil {
		t.Fatal("unexpected modal")
	}
	if !m.noticeErr || !strings.Contains(m.notice, "unknown") {
		t.Fatalf("notice = %q", m.notice)
	}
}

func TestWorkflowBuilderCancelPreservesNoStart(t *testing.T) {
	ops := make(chan protocol.Op, 1)
	wf := fakeWorkflows{items: testWorkflowCatalog()}
	doc, _ := wf.Document("plan-implement")
	modal := newWorkflowBuilderModal(wf, nil, doc, host.WorkflowScopeProject, false, testTheme())
	// Clean cancel (no dirty) closes without StartWorkflow.
	updated, cmd := modal.update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if updated != nil {
		t.Fatal("esc should close clean builder")
	}
	if cmd == nil {
		t.Fatal("expected result cmd")
	}
	msg := cmd()
	res, ok := msg.(workflowBuilderResultMsg)
	if !ok || !res.canceled {
		t.Fatalf("result = %#v", msg)
	}
	select {
	case op := <-ops:
		t.Fatalf("unexpected op: %#v", op)
	default:
	}
}

func TestWorkflowBuilderRejectsInvalidSave(t *testing.T) {
	wf := fakeWorkflows{}
	doc := host.WorkflowDocument{
		SchemaVersion: 1,
		Name:          "bad",
		// no phases → invalid
	}
	modal := newWorkflowBuilderModal(wf, nil, doc, host.WorkflowScopeProject, true, testTheme())
	// Force empty phases after construct (constructor adds default phase).
	modal.doc.Phases = nil
	modal.refreshStatus()
	if !modal.statusErr {
		t.Fatal("expected invalid status")
	}
	updated, cmd := modal.trySave(false)
	if updated != modal {
		t.Fatal("should stay open")
	}
	if cmd != nil {
		t.Fatal("invalid save must not dispatch Save cmd")
	}
}

func TestWorkflowBuilderSaveDoesNotStart(t *testing.T) {
	ops := make(chan protocol.Op, 1)
	wf := fakeWorkflows{savePath: "/tmp/project/ok-flow.json"}
	doc, err := wf.Scaffold("ok-flow")
	if err != nil {
		t.Fatal(err)
	}
	modal := newWorkflowBuilderModal(wf, []string{"build"}, doc, host.WorkflowScopeProject, true, testTheme())
	updated, cmd := modal.trySave(false)
	if updated != modal || cmd == nil {
		t.Fatalf("save cmd missing: updated=%T cmd=%v", updated, cmd != nil)
	}
	msg := cmd()
	saved, ok := msg.(workflowBuilderSavedMsg)
	if !ok || saved.err != nil || saved.path == "" {
		t.Fatalf("saved msg = %#v", msg)
	}
	// Apply success — stay open (closeAfter false); no StartWorkflow.
	next, _ := modal.onSaved(saved)
	if next == nil {
		t.Fatal("should remain open after save without closeAfter")
	}
	select {
	case op := <-ops:
		t.Fatalf("save must not start workflow: %#v", op)
	default:
	}
}

func TestWorkflowBuilderUnsavedDiscard(t *testing.T) {
	wf := fakeWorkflows{}
	doc, _ := wf.Scaffold("x")
	modal := newWorkflowBuilderModal(wf, nil, doc, host.WorkflowScopeProject, true, testTheme())
	modal.doc.Description = "changed"
	if !modal.dirty() {
		t.Fatal("expected dirty")
	}
	updated, _ := modal.update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if updated != modal || !modal.unsavedPrompt {
		t.Fatal("expected unsaved prompt")
	}
	// n stays
	updated, _ = modal.update(tea.KeyPressMsg{Text: "n", Code: 'n'})
	if updated != modal || modal.unsavedPrompt {
		t.Fatal("n should dismiss prompt and stay")
	}
	// esc again then y discard
	updated, _ = modal.update(tea.KeyPressMsg{Code: tea.KeyEsc})
	updated, cmd := modal.update(tea.KeyPressMsg{Text: "y", Code: 'y'})
	if updated != nil {
		t.Fatal("y should discard and close")
	}
	res := cmd().(workflowBuilderResultMsg)
	if !res.canceled {
		t.Fatalf("result = %#v", res)
	}
}

func testTheme() theme.Theme {
	return theme.Default()
}
