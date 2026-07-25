package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

func TestSessionModalListsRootsWithTitles(t *testing.T) {
	fs := newFakeSessions()
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	fs.put(host.Session{
		ID: "root-new", Title: "ship the feature", UpdatedAt: now.Add(-5 * time.Minute),
	}, nil)
	fs.put(host.Session{
		ID: "root-old", Title: "fix auth", UpdatedAt: now.Add(-2 * time.Hour),
	}, nil)
	fs.put(host.Session{
		ID: "child-1", ParentID: "root-new", Title: "sub", UpdatedAt: now,
	}, nil)

	m := newSessionModal(fs, "root-new")
	m.now = now
	if m.loadErr != "" {
		t.Fatalf("loadErr = %q", m.loadErr)
	}
	if len(m.all) != 2 {
		t.Fatalf("roots = %d, want 2 (children excluded)", len(m.all))
	}
	if m.all[0].ID != "root-new" {
		t.Errorf("newest first: got %q", m.all[0].ID)
	}
	if m.cursor != 0 {
		t.Errorf("cursor on current = %d, want 0", m.cursor)
	}

	view := m.view(72, theme.Default().Resolve())
	if !strings.Contains(view, "ship the feature") {
		t.Errorf("view missing title:\n%s", view)
	}
	if !strings.Contains(view, "fix auth") {
		t.Errorf("view missing second title:\n%s", view)
	}
	if strings.Contains(view, "sub") {
		t.Errorf("view should not list child sessions:\n%s", view)
	}
}

func TestSessionModalEnterResumesOther(t *testing.T) {
	fs := newFakeSessions()
	fs.put(host.Session{ID: "cur", Title: "current"}, nil)
	fs.put(host.Session{ID: "other", Title: "past work"}, nil)

	modal := newSessionModal(fs, "cur")
	// Move to "other" (list is ID-desc when UpdatedAt equal: other > cur?
	// "other" > "cur" lexicographically so other first when UpdatedAt zero.
	list := modal.filtered()
	idx := -1
	for i, s := range list {
		if s.ID == "other" {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatal("other not in list")
	}
	modal.cursor = idx
	next, cmd := modal.update(tea.KeyMsg{Type: tea.KeyEnter})
	if next != nil {
		t.Fatalf("modal after enter = %T, want nil", next)
	}
	if cmd == nil {
		t.Fatal("expected resume cmd")
	}
	msg := cmd()
	rm, ok := msg.(sessionResumeMsg)
	if !ok || rm.id != "other" {
		t.Fatalf("msg = %#v, want sessionResumeMsg{other}", msg)
	}
}

func TestSessionModalEnterCurrentCloses(t *testing.T) {
	fs := newFakeSessions()
	fs.put(host.Session{ID: "cur", Title: "current"}, nil)
	modal := newSessionModal(fs, "cur")
	next, cmd := modal.update(tea.KeyMsg{Type: tea.KeyEnter})
	if next != nil || cmd != nil {
		t.Fatalf("selecting current: next=%T cmd=%v", next, cmd != nil)
	}
}

func TestSessionCommandOpensPicker(t *testing.T) {
	fs := newFakeSessions()
	fs.put(host.Session{ID: "a", Title: "alpha"}, nil)
	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "cur"
	m.services.Sessions = fs

	next, _ := m.handleCommand("/session")
	nm := next.(Model)
	sm, ok := nm.modal.(*sessionModal)
	if !ok || sm == nil {
		t.Fatalf("modal = %T, want sessionModal", nm.modal)
	}
	if len(sm.all) != 1 || sm.all[0].Title != "alpha" {
		t.Fatalf("picker sessions = %+v", sm.all)
	}
}

func TestSessionCommandIDQueuesResume(t *testing.T) {
	fs := newFakeSessions()
	fs.put(host.Session{ID: "past-1", Title: "old"}, nil)
	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "cur"
	m.services.Sessions = fs

	next, cmd := m.handleCommand("/session past-1")
	if cmd == nil {
		t.Fatal("expected resume cmd")
	}
	msg := cmd()
	rm, ok := msg.(sessionResumeMsg)
	if !ok || rm.id != "past-1" {
		t.Fatalf("msg = %#v", msg)
	}

	// Apply resume msg → PendingResume + quit.
	got, qcmd := next.(Model).Update(msg)
	gm := got.(Model)
	if gm.PendingResume() != "past-1" {
		t.Errorf("PendingResume = %q", gm.PendingResume())
	}
	if qcmd == nil {
		t.Fatal("expected tea.Quit cmd")
	}
}

func TestSessionCommandRejectsChild(t *testing.T) {
	fs := newFakeSessions()
	fs.put(host.Session{ID: "child", ParentID: "root", Title: "sub"}, nil)
	m, _ := newAppTestModel(nil, nil)
	m.services.Sessions = fs
	next, cmd := m.handleCommand("/session child")
	if cmd != nil {
		t.Fatal("should not resume child")
	}
	nm := next.(Model)
	if !strings.Contains(nm.notice, "subagent") {
		t.Errorf("notice = %q", nm.notice)
	}
}

func TestSessionResumeBlockedWhileTurnRunning(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "cur"
	m.turnRunning = true
	next, cmd := m.Update(sessionResumeMsg{id: "other"})
	nm := next.(Model)
	if cmd != nil {
		t.Fatal("should not quit while turn running")
	}
	if nm.PendingResume() != "" {
		t.Errorf("PendingResume = %q", nm.PendingResume())
	}
	if !strings.Contains(nm.notice, "wait") {
		t.Errorf("notice = %q", nm.notice)
	}
}

func TestFormatRelativeTime(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "just now"},
		{2 * time.Minute, "2m ago"},
		{3 * time.Hour, "3h ago"},
		{48 * time.Hour, "2d ago"},
	}
	for _, tc := range cases {
		got := formatRelativeTime(now, now.Add(-tc.d))
		if got != tc.want {
			t.Errorf("formatRelativeTime(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}
