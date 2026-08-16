package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

func TestProjectQueuePaneEntriesOrder(t *testing.T) {
	entries := projectQueuePaneEntries(
		theme.Default(),
		"model",
		[]string{"model"},
		[]childActivity{{
			sessionID:  "c1",
			agent:      "explore",
			queueLabel: "bash",
			queuePools: []string{"process"},
		}},
		[]queuedInput{
			{modelText: "first", displayPrompt: "first"},
			{modelText: "second", displayPrompt: "second"},
		},
		[]scheduledLoop{{
			id:       "l1",
			interval: 15 * time.Minute,
			job:      "check pipeline",
			runs:     2,
		}},
	)
	if len(entries) != 5 {
		t.Fatalf("len=%d want 5: %+v", len(entries), entries)
	}
	wantKinds := []queuePaneKind{
		queuePaneScheduler, queuePaneScheduler,
		queuePanePrompt, queuePanePrompt,
		queuePaneLoop,
	}
	for i, k := range wantKinds {
		if entries[i].Kind != k {
			t.Errorf("entry[%d] kind=%v want %v", i, entries[i].Kind, k)
		}
	}
	if entries[2].Detail != "next" || entries[3].Detail != "queued" {
		t.Errorf("prompt details = %q / %q", entries[2].Detail, entries[3].Detail)
	}
	if entries[4].LoopID != "l1" || !strings.Contains(entries[4].Detail, "15m") {
		t.Errorf("loop entry = %+v", entries[4])
	}
}

func TestQueuePaneBodyShowsPromptsLoopsAndEmpty(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.width, m.height = 80, 24

	empty := ansi.Strip(m.queuePaneBody(40, 8))
	if !strings.Contains(empty, "nothing queued") {
		t.Fatalf("empty body = %q", empty)
	}

	m.inputQueue = []queuedInput{
		{modelText: "alpha prompt", displayPrompt: "alpha prompt"},
		{modelText: "beta prompt", displayPrompt: "beta prompt"},
	}
	m.loops = []scheduledLoop{{
		id: "l2", interval: 2 * time.Hour, job: "nightly review", runs: 0,
	}}
	m.queueLabel = "model"
	m.queuePools = []string{"model"}

	body := ansi.Strip(m.queuePaneBody(48, 12))
	for _, want := range []string{"alpha prompt", "beta prompt", "l2", "nightly", "model", "wait"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q:\n%s", want, body)
		}
	}
	if m.queuePaneBody(0, 10) != "" || m.queuePaneBody(10, 0) != "" {
		t.Fatal("zero geometry should yield empty body")
	}
}

func TestQueueSlashFocusesQueuePane(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	if reg, ok := m.windows.activate(memoryWindowID); ok {
		m.windows = reg
	}
	m.composer.SetValue("/queue")
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	if cmd != nil {
		runAppCmd(t, cmd)
	}
	if m.windows.active().id() != queueWindowID {
		t.Fatalf("active = %q, want queue", m.windows.active().id())
	}
	if m.focus != focusRight {
		t.Fatalf("focus = %v, want right", m.focus)
	}
	if m.modal != nil {
		t.Fatalf("modal = %T, want nil", m.modal)
	}
}

func TestQueuePaneKeysReorderPromoteDelete(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m.inputQueue = []queuedInput{
		{modelText: "a", displayPrompt: "a"},
		{modelText: "b", displayPrompt: "b"},
		{modelText: "c", displayPrompt: "c"},
	}
	m.windows, _ = m.windows.activate(queueWindowID)
	_ = m.setPaneFocus(focusRight)
	// Cursor on second prompt (after any scheduler rows — none here).
	entries := m.queuePaneEntries()
	m.setQueuePaneCursor(entries, 1) // "b"

	handled, _ := m.handleQueuePaneKeys(tea.KeyPressMsg{Text: "K"}) // shift+K reorder up
	if !handled {
		t.Fatal("K not handled")
	}
	if got := queueLabels(m.inputQueue); got != "b|a|c" {
		t.Fatalf("after K reorder = %s", got)
	}

	// Promote "c" to head.
	entries = m.queuePaneEntries()
	for i, e := range entries {
		if e.Kind == queuePanePrompt && e.PromptIdx == 2 {
			m.setQueuePaneCursor(entries, i)
			break
		}
	}
	handled, _ = m.handleQueuePaneKeys(tea.KeyPressMsg{Code: 'p'})
	if !handled {
		t.Fatal("p not handled")
	}
	if got := queueLabels(m.inputQueue); got != "c|b|a" {
		t.Fatalf("after promote = %s", got)
	}

	// Delete head.
	entries = m.queuePaneEntries()
	m.setQueuePaneCursor(entries, 0)
	handled, _ = m.handleQueuePaneKeys(tea.KeyPressMsg{Code: 'd'})
	if !handled {
		t.Fatal("d not handled")
	}
	if got := queueLabels(m.inputQueue); got != "b|a" {
		t.Fatalf("after delete = %s", got)
	}
}

func TestQueuePaneStopLoopAndModal(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m.loops = []scheduledLoop{{id: "l9", interval: time.Minute, job: "tick", gen: 1}}
	m.windows, _ = m.windows.activate(queueWindowID)
	_ = m.setPaneFocus(focusRight)
	entries := m.queuePaneEntries()
	m.setQueuePaneCursor(entries, 0)

	handled, _ := m.handleQueuePaneKeys(tea.KeyPressMsg{Code: 'd'})
	if !handled {
		t.Fatal("d on loop not handled")
	}
	if len(m.loops) != 0 {
		t.Fatalf("loop not stopped: %+v", m.loops)
	}

	m.inputQueue = []queuedInput{{modelText: "hi", displayPrompt: "hi"}}
	handled, _ = m.handleQueuePaneKeys(tea.KeyPressMsg{Code: 'm'})
	if !handled {
		t.Fatal("m not handled")
	}
	if _, ok := m.modal.(*queueModal); !ok {
		t.Fatalf("modal = %T, want *queueModal", m.modal)
	}
}

func TestQueuePaneEnterOpensEdit(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m.inputQueue = []queuedInput{{modelText: "edit me", displayPrompt: "edit me"}}
	m.windows, _ = m.windows.activate(queueWindowID)
	_ = m.setPaneFocus(focusRight)
	entries := m.queuePaneEntries()
	m.setQueuePaneCursor(entries, 0)

	handled, _ := m.handleQueuePaneKeys(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !handled {
		t.Fatal("enter not handled")
	}
	qm, ok := m.modal.(*queueModal)
	if !ok {
		t.Fatalf("modal = %T", m.modal)
	}
	if !qm.edit {
		t.Fatal("want edit mode after enter")
	}
}

func TestQueuePaneEditComposer(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m.inputQueue = []queuedInput{
		{modelText: "keep", displayPrompt: "keep"},
		{modelText: "load me", displayPrompt: "load me"},
	}
	m.windows, _ = m.windows.activate(queueWindowID)
	_ = m.setPaneFocus(focusRight)
	entries := m.queuePaneEntries()
	m.setQueuePaneCursor(entries, 1)

	handled, cmd := m.handleQueuePaneKeys(tea.KeyPressMsg{Code: 'e'})
	if !handled {
		t.Fatal("e not handled")
	}
	if cmd != nil {
		// setPaneFocus may return a cmd
		_ = cmd
	}
	if got := queueLabels(m.inputQueue); got != "keep" {
		t.Fatalf("queue after e = %s", got)
	}
	if m.composer.Value() != "load me" {
		t.Fatalf("composer = %q", m.composer.Value())
	}
}

func TestSchedulerQueuedShowsInQueuePane(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "root"
	_ = m.applyEvent(protocol.SchedulerQueued{
		Correlation: protocol.Correlation{SessionID: "root"},
		RequestID:   "r1",
		Pools:       []string{"model"},
		Label:       "model",
	})
	body := ansi.Strip(m.queuePaneBody(40, 6))
	if !strings.Contains(body, "model") && !strings.Contains(body, "queued") {
		t.Fatalf("queue pane missing scheduler wait:\n%s", body)
	}
}
