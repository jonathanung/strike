package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/protocol"
)

// fakeWorkflows is a minimal host.Workflows for slash-command tests.
type fakeWorkflows struct {
	items []host.WorkflowSummary
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
