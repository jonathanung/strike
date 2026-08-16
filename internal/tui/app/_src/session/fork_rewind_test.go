package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/protocol"
)

func TestForkCommandQueuesResume(t *testing.T) {
	fs := newFakeSessions()
	fs.put(host.Session{ID: "cur", Title: "main"}, mustSessionJSONL(t,
		protocol.UserMessage{Text: "hi"},
		protocol.TextDelta{Text: "hello"},
		protocol.TurnCompleted{StopReason: "end_turn"},
	))
	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "cur"
	m.services.Sessions = fs

	next, cmd := m.handleCommand("/fork")
	if cmd == nil {
		t.Fatal("expected quit cmd after fork")
	}
	// tea.Quit returns a special msg; just ensure PendingResume is set.
	nm := next.(Model)
	if !strings.HasPrefix(nm.PendingResume(), "cur-fork") {
		t.Fatalf("PendingResume = %q", nm.PendingResume())
	}
	if !strings.Contains(nm.notice, "forked") {
		t.Fatalf("notice = %q", nm.notice)
	}
	// Run quit cmd so it doesn't hang if executed.
	_ = cmd
}

func TestForkCommandRejectsWhileTurnRunning(t *testing.T) {
	fs := newFakeSessions()
	fs.put(host.Session{ID: "cur", Title: "main"}, nil)
	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "cur"
	m.services.Sessions = fs
	m.turnRunning = true
	next, cmd := m.handleCommand("/fork")
	if cmd != nil {
		t.Fatal("should not quit while turn running")
	}
	if !strings.Contains(next.(Model).notice, "wait") {
		t.Fatalf("notice = %q", next.(Model).notice)
	}
}

func TestUndoCommandOpensModal(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	next, cmd := m.handleCommand("/undo")
	if cmd != nil {
		t.Fatal("bare /undo should open modal, not send op")
	}
	nm := next.(Model)
	if _, ok := nm.modal.(*undoModal); !ok {
		t.Fatalf("modal = %T, want *undoModal", nm.modal)
	}
}

func TestUndoChatSendsRewindOp(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	_, cmd := m.handleCommand("/undo chat")
	if cmd == nil {
		t.Fatal("expected op cmd")
	}
	runAppCmd(t, cmd)
	op := receiveAppOp(t, ops)
	rw, ok := op.(protocol.Rewind)
	if !ok || rw.RestoreFiles {
		t.Fatalf("op = %#v, want Rewind chat-only", op)
	}
}

func TestUndoFilesSendsRewindOp(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	_, cmd := m.handleCommand("/undo files")
	if cmd == nil {
		t.Fatal("expected op cmd")
	}
	runAppCmd(t, cmd)
	op := receiveAppOp(t, ops)
	rw, ok := op.(protocol.Rewind)
	if !ok || !rw.RestoreFiles {
		t.Fatalf("op = %#v, want Rewind restore files", op)
	}
}

func TestRewindOpensTurnPicker(t *testing.T) {
	fs := newFakeSessions()
	fs.put(host.Session{ID: "cur", Title: "main"}, mustSessionJSONL(t,
		protocol.UserMessage{Text: "first"},
		protocol.TurnCompleted{StopReason: "end_turn"},
		protocol.UserMessage{Text: "second"},
		protocol.TurnCompleted{StopReason: "end_turn"},
	))
	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "cur"
	m.services.Sessions = fs
	next, cmd := m.handleCommand("/rewind")
	if cmd != nil {
		t.Fatal("bare /rewind should open modal")
	}
	rm, ok := next.(Model).modal.(*rewindModal)
	if !ok {
		t.Fatalf("modal = %T", next.(Model).modal)
	}
	if len(rm.choices) != 2 {
		t.Fatalf("choices = %d, want 2", len(rm.choices))
	}
	// Default cursor on previous turn (one step back).
	if rm.cursor != 0 {
		t.Fatalf("cursor = %d, want 0 (turn 1)", rm.cursor)
	}
}

func TestRewindTurnArgForksPrefix(t *testing.T) {
	fs := newFakeSessions()
	fs.put(host.Session{ID: "cur", Title: "main"}, mustSessionJSONL(t,
		protocol.UserMessage{Text: "first"},
		protocol.TextDelta{Text: "one"},
		protocol.TurnCompleted{StopReason: "end_turn"},
		protocol.UserMessage{Text: "second"},
		protocol.TextDelta{Text: "two"},
		protocol.TurnCompleted{StopReason: "end_turn"},
	))
	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "cur"
	m.services.Sessions = fs

	next, cmd := m.handleCommand("/rewind 1")
	if cmd == nil {
		t.Fatal("expected quit cmd after rewind fork")
	}
	nm := next.(Model)
	if !strings.HasPrefix(nm.PendingResume(), "cur-fork") {
		t.Fatalf("PendingResume = %q", nm.PendingResume())
	}
	// Original intact.
	src, err := fs.ReplayJSONL("cur")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "second") {
		t.Fatal("original session lost later turn")
	}
	// Fork has only first turn.
	childID := nm.PendingResume()
	got, err := fs.ReplayJSONL(childID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "second") {
		t.Fatalf("fork should not include turn 2: %s", got)
	}
	if !strings.Contains(string(got), "first") {
		t.Fatalf("fork missing turn 1: %s", got)
	}
	// Both listable as roots.
	roots, err := fs.List(true)
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) < 2 {
		t.Fatalf("roots = %d, want >= 2", len(roots))
	}
}

func TestRewindModalEnterForks(t *testing.T) {
	fs := newFakeSessions()
	fs.put(host.Session{ID: "cur", Title: "main"}, mustSessionJSONL(t,
		protocol.UserMessage{Text: "a"},
		protocol.TurnCompleted{},
		protocol.UserMessage{Text: "b"},
		protocol.TurnCompleted{},
	))
	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "cur"
	m.services.Sessions = fs
	next, _ := m.handleCommand("/rewind")
	nm := next.(Model)
	modal := nm.modal.(*rewindModal)
	updated, cmd := modal.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if updated != nil {
		t.Fatal("enter should close modal")
	}
	if cmd == nil {
		t.Fatal("expected fork msg cmd")
	}
	msg := runAppCmd(t, cmd)
	rf, ok := msg.(rewindForkMsg)
	if !ok {
		t.Fatalf("msg = %T", msg)
	}
	next2, cmd2 := nm.Update(rf)
	if cmd2 == nil {
		t.Fatal("expected quit after apply")
	}
	if !strings.HasPrefix(next2.(Model).PendingResume(), "cur-fork") {
		t.Fatalf("PendingResume = %q", next2.(Model).PendingResume())
	}
}

func TestRewindRejectsWhileTurnRunning(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "cur"
	m.turnRunning = true
	next, cmd := m.handleCommand("/rewind")
	if cmd != nil {
		t.Fatal("should not quit while turn running")
	}
	if !strings.Contains(next.(Model).notice, "wait") {
		t.Fatalf("notice = %q", next.(Model).notice)
	}
}

func TestUndoModalEnterSendsRestore(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	// Seed a harness-file turn so the picker defaults to "chat and files".
	m.applyEvent(protocol.TurnCompleted{
		StopReason: "end_turn",
		Files:      []protocol.TurnFileChange{{Path: "a.go", Kind: "update"}},
	})
	next, _ := m.handleCommand("/undo")
	nm := next.(Model)
	modal := nm.modal.(*undoModal)
	if modal.cursor != 1 {
		t.Fatalf("cursor = %d, want 1 (files) when paths present", modal.cursor)
	}
	view := modal.view(80, nm.th)
	if !strings.Contains(view, "a.go") {
		t.Fatalf("preview missing path:\n%s", view)
	}
	updated, cmd := modal.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if updated != nil {
		t.Fatal("enter should close modal")
	}
	if cmd == nil {
		t.Fatal("expected op cmd")
	}
	runAppCmd(t, cmd)
	op := receiveAppOp(t, ops)
	rw, ok := op.(protocol.Rewind)
	if !ok || !rw.RestoreFiles {
		t.Fatalf("op = %#v", op)
	}
}

func TestUndoModalWarnsUncoveredBash(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.applyEvent(protocol.TurnCompleted{
		StopReason:        "end_turn",
		Files:             []protocol.TurnFileChange{{Path: "note.txt", Kind: "update"}},
		Uncovered:         []string{"bash"},
		CheckpointSkipped: 1,
	})
	next, _ := m.handleCommand("/undo")
	modal := next.(Model).modal.(*undoModal)
	view := modal.view(100, next.(Model).th)
	if !strings.Contains(view, "note.txt") {
		t.Fatalf("missing path preview:\n%s", view)
	}
	if !strings.Contains(strings.ToLower(view), "uncovered") || !strings.Contains(view, "bash") {
		t.Fatalf("missing uncovered warning:\n%s", view)
	}
	if !strings.Contains(view, "skipped") {
		t.Fatalf("missing skipped count:\n%s", view)
	}
}

func TestUndoModalEmptyBiasesChatOnly(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	next, _ := m.handleCommand("/undo")
	modal := next.(Model).modal.(*undoModal)
	if modal.cursor != 0 {
		t.Fatalf("cursor = %d, want 0 when no file preview", modal.cursor)
	}
}

func TestFormatSessionRewoundWarnsUncovered(t *testing.T) {
	msg := formatSessionRewound(protocol.SessionRewound{
		Removed:       2,
		RestoreFiles:  true,
		FilesRestored: 1,
		Files:         []string{"a.go"},
		Uncovered:     []string{"bash"},
	})
	if !strings.Contains(msg, "restored") || !strings.Contains(msg, "a.go") {
		t.Fatalf("msg = %q", msg)
	}
	if !strings.Contains(msg, "uncovered") || !strings.Contains(msg, "bash") {
		t.Fatalf("missing uncovered warn: %q", msg)
	}
	// Chat-only must not scare about uncovered disk restore.
	chat := formatSessionRewound(protocol.SessionRewound{
		Removed: 2, RestoreFiles: false, Uncovered: []string{"bash"},
	})
	if strings.Contains(chat, "uncovered") {
		t.Fatalf("chat-only should omit uncovered warn: %q", chat)
	}
}

func TestUndoModalWarnsCheckpointsGone(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.undoStack = []undoPreview{{
		files:           []protocol.TurnFileChange{{Path: "a.go", Kind: "update"}},
		checkpointsGone: true,
	}}
	next, _ := m.handleCommand("/undo")
	view := next.(Model).modal.(*undoModal).view(100, next.(Model).th)
	if !strings.Contains(strings.ToLower(view), "unavailable") {
		t.Fatalf("missing checkpoint unavailable warning:\n%s", view)
	}
}

func TestUndoStackFromEvents(t *testing.T) {
	events := []protocol.Event{
		protocol.TurnCompleted{Files: []protocol.TurnFileChange{{Path: "a", Kind: "create"}}},
		protocol.TurnCompleted{Files: []protocol.TurnFileChange{{Path: "b", Kind: "update"}}, Uncovered: []string{"bash"}},
		protocol.SessionRewound{Removed: 1},
	}
	stack := undoStackFromEvents(events)
	if len(stack) != 1 {
		t.Fatalf("stack len = %d, want 1", len(stack))
	}
	if len(stack[0].files) != 1 || stack[0].files[0].Path != "a" {
		t.Fatalf("stack[0] = %+v", stack[0])
	}
}

func TestSessionRewoundDropsTranscriptCells(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.applyEvent(protocol.UserMessage{Text: "first"})
	m.applyEvent(protocol.TextDelta{Text: "one"})
	m.applyEvent(protocol.TurnCompleted{StopReason: "end_turn"})
	m.applyEvent(protocol.UserMessage{Text: "second"})
	m.applyEvent(protocol.TextDelta{Text: "two"})
	m.applyEvent(protocol.TurnCompleted{StopReason: "end_turn", Files: []protocol.TurnFileChange{{Path: "x", Kind: "update"}}})
	if len(m.undoStack) != 2 {
		t.Fatalf("undoStack before = %d", len(m.undoStack))
	}
	if len(m.cells) < 4 {
		t.Fatalf("cells before = %d", len(m.cells))
	}
	m.applyEvent(protocol.SessionRewound{Removed: 2, RestoreFiles: true, FilesRestored: 1, Files: []string{"x"}})
	// Should keep first user + assistant only.
	if len(m.cells) != 2 {
		t.Fatalf("cells after rewind = %d, want 2", len(m.cells))
	}
	if len(m.undoStack) != 1 {
		t.Fatalf("undoStack after = %d, want 1", len(m.undoStack))
	}
	uc, ok := m.cells[0].(*userCell)
	if !ok || uc.text != "first" {
		t.Fatalf("cell0 = %#v", m.cells[0])
	}
	if !strings.Contains(m.notice, "rewound") || !strings.Contains(m.notice, "restored") {
		t.Fatalf("notice = %q", m.notice)
	}
}

func TestCellsFromEventsAppliesSessionRewound(t *testing.T) {
	events := []protocol.Event{
		protocol.UserMessage{Text: "a"},
		protocol.TextDelta{Text: "A"},
		protocol.TurnCompleted{},
		protocol.UserMessage{Text: "b"},
		protocol.TextDelta{Text: "B"},
		protocol.TurnCompleted{},
		protocol.SessionRewound{Removed: 2},
	}
	cells, _ := cellsFromEvents(events)
	if len(cells) != 2 {
		t.Fatalf("cells = %d, want 2", len(cells))
	}
	if uc, ok := cells[0].(*userCell); !ok || uc.text != "a" {
		t.Fatalf("cell0 = %#v", cells[0])
	}
}

func TestHelpListsForkUndoRewind(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	names := map[string]bool{}
	for _, c := range m.commands {
		names[c.Name] = true
	}
	for _, want := range []string{"/fork", "/undo", "/rewind"} {
		if !names[want] {
			t.Errorf("missing command %s", want)
		}
	}
}
