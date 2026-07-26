package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

func TestSessionModalFiltersByProject(t *testing.T) {
	fs := newFakeSessionsForProject("/repos/a")
	fs.put(host.Session{ID: "a1", Title: "repo A work", ProjectKey: "/repos/a"}, nil)
	fs.put(host.Session{ID: "b1", Title: "repo B work", ProjectKey: "/repos/b"}, nil)
	fs.put(host.Session{ID: "legacy", Title: "no project"}, nil)

	m := newSessionModal(fs, "")
	if m.loadErr != "" {
		t.Fatalf("loadErr = %q", m.loadErr)
	}
	if len(m.all) != 1 || m.all[0].ID != "a1" {
		t.Fatalf("default list = %+v, want only a1", m.all)
	}
	view := m.view(72, theme.Default().Resolve())
	if !strings.Contains(view, "repo A work") {
		t.Errorf("missing A title:\n%s", view)
	}
	if strings.Contains(view, "repo B work") {
		t.Errorf("should not list other project:\n%s", view)
	}
	if !strings.Contains(view, "ctrl+a all projects") {
		t.Errorf("missing all-projects hint:\n%s", view)
	}

	next, _ := m.update(tea.KeyMsg{Type: tea.KeyCtrlA})
	sm := next.(*sessionModal)
	if !sm.allProjects || len(sm.all) != 3 {
		t.Fatalf("all projects: allProjects=%v list=%+v", sm.allProjects, sm.all)
	}
	view = sm.view(80, theme.Default().Resolve())
	if !strings.Contains(view, "all projects") {
		t.Errorf("title should note all projects:\n%s", view)
	}
	if !strings.Contains(view, "repo B work") {
		t.Errorf("all mode missing B:\n%s", view)
	}
	next, _ = sm.update(tea.KeyMsg{Type: tea.KeyCtrlA})
	sm = next.(*sessionModal)
	if sm.allProjects || len(sm.all) != 1 {
		t.Fatalf("toggle back: allProjects=%v list=%+v", sm.allProjects, sm.all)
	}
}

func TestSessionProjectLabel(t *testing.T) {
	cases := map[string]string{
		"":              "unknown project",
		"/repos/strike": "strike",
		`C:\work\app`:   "app",
		"simple":        "simple",
	}
	for in, want := range cases {
		if got := sessionProjectLabel(in); got != want {
			t.Errorf("sessionProjectLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

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

func TestSessionModalShowsPRBadge(t *testing.T) {
	fs := newFakeSessions()
	fs.put(host.Session{
		ID:       "with-pr",
		Title:    "ship feature",
		PRURL:    "https://github.com/acme/repo/pull/42",
		PRNumber: 42,
		PRState:  "open",
	}, nil)
	fs.put(host.Session{
		ID:       "merged-pr",
		Title:    "done work",
		PRURL:    "https://github.com/acme/repo/pull/9",
		PRNumber: 9,
		PRState:  "merged",
	}, nil)
	fs.put(host.Session{ID: "no-pr", Title: "local only"}, nil)

	m := newSessionModal(fs, "with-pr")
	view := m.view(80, theme.Default().Resolve())
	if !strings.Contains(view, "#42") {
		t.Errorf("missing open PR badge:\n%s", view)
	}
	if !strings.Contains(view, "#9") {
		t.Errorf("missing merged PR badge:\n%s", view)
	}
	// OSC 8 hyperlink to pr_url
	if !strings.Contains(view, "https://github.com/acme/repo/pull/42") {
		t.Errorf("missing OSC8 pr url:\n%s", view)
	}
	// Sessions without PR still listed by title only.
	if !strings.Contains(view, "local only") {
		t.Errorf("missing no-pr session:\n%s", view)
	}
}

func TestSessionPRBadgeEmptyWithoutPR(t *testing.T) {
	th := theme.Default().Resolve()
	if got := sessionPRBadge(th, host.Session{Title: "x"}); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestSessionModalRefreshUsesFakePRState(t *testing.T) {
	fs := newFakeSessions()
	fs.put(host.Session{
		ID: "r1", Title: "t", PRURL: "https://github.com/a/b/pull/1", PRNumber: 1, PRState: "open",
	}, nil)
	fs.refresh = func(in []host.Session) []host.Session {
		out := append([]host.Session(nil), in...)
		out[0].PRState = "closed"
		return out
	}
	m := newSessionModal(fs, "")
	if len(m.all) != 1 || m.all[0].PRState != "closed" {
		t.Fatalf("refresh not applied: %+v", m.all)
	}
	view := m.view(72, theme.Default().Resolve())
	if !strings.Contains(view, "closed") {
		t.Errorf("view missing closed state:\n%s", view)
	}
}
