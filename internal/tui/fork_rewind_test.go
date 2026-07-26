package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
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

func TestUndoCommandSendsRewindOp(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	next, cmd := m.handleCommand("/undo")
	_ = next
	if cmd == nil {
		t.Fatal("expected op cmd")
	}
	runAppCmd(t, cmd)
	op := receiveAppOp(t, ops)
	if _, ok := op.(protocol.Rewind); !ok {
		t.Fatalf("op = %#v, want Rewind", op)
	}
}

func TestRewindAliasSendsRewindOp(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	_, cmd := m.handleCommand("/rewind")
	if cmd == nil {
		t.Fatal("expected op cmd")
	}
	runAppCmd(t, cmd)
	op := receiveAppOp(t, ops)
	if _, ok := op.(protocol.Rewind); !ok {
		t.Fatalf("op = %#v, want Rewind", op)
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
	m.applyEvent(protocol.TurnCompleted{StopReason: "end_turn"})
	if len(m.cells) < 4 {
		t.Fatalf("cells before = %d", len(m.cells))
	}
	m.applyEvent(protocol.SessionRewound{Removed: 2})
	// Should keep first user + assistant only.
	if len(m.cells) != 2 {
		t.Fatalf("cells after rewind = %d, want 2", len(m.cells))
	}
	uc, ok := m.cells[0].(*userCell)
	if !ok || uc.text != "first" {
		t.Fatalf("cell0 = %#v", m.cells[0])
	}
	if !strings.Contains(m.notice, "rewound") {
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

func TestHelpListsForkAndUndo(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	names := map[string]bool{}
	for _, c := range m.commands {
		names[c.Name] = true
	}
	for _, want := range []string{"/fork", "/undo"} {
		if !names[want] {
			t.Errorf("missing command %s", want)
		}
	}
}
