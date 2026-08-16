package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

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

	next, _ := m.update(tea.KeyPressMsg{Code: 'a', Mod: tea.ModCtrl})
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
	next, _ = sm.update(tea.KeyPressMsg{Code: 'a', Mod: tea.ModCtrl})
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
	next, cmd := modal.update(tea.KeyPressMsg{Code: tea.KeyEnter})
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
	next, cmd := modal.update(tea.KeyPressMsg{Code: tea.KeyEnter})
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

func TestSessionCommandIDCrossWorkspace(t *testing.T) {
	// Default picker is workspace-scoped, but /session <id> must still open a
	// root from another project via Get (unfiltered).
	fs := newFakeSessionsForProject("/repos/a")
	fs.put(host.Session{ID: "a-cur", Title: "here", ProjectKey: "/repos/a"}, nil)
	fs.put(host.Session{ID: "b-other", Title: "elsewhere", ProjectKey: "/repos/b"}, nil)

	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "a-cur"
	m.services.Sessions = fs

	// Picker only lists this workspace.
	next, _ := m.handleCommand("/session")
	sm, ok := next.(Model).modal.(*sessionModal)
	if !ok || sm == nil {
		t.Fatalf("modal = %T", next.(Model).modal)
	}
	if len(sm.all) != 1 || sm.all[0].ID != "a-cur" {
		t.Fatalf("picker = %+v, want only a-cur", sm.all)
	}

	// Explicit id from another workspace still resumes.
	_, cmd := m.handleCommand("/session b-other")
	if cmd == nil {
		t.Fatal("expected resume cmd for cross-workspace id")
	}
	msg := cmd()
	rm, ok := msg.(sessionResumeMsg)
	if !ok || rm.id != "b-other" {
		t.Fatalf("msg = %#v, want sessionResumeMsg{b-other}", msg)
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

func TestSessionModalFilterLive(t *testing.T) {
	fs := newFakeSessions()
	fs.put(host.Session{ID: "a", Title: "alpha work"}, nil)
	fs.put(host.Session{ID: "b", Title: "beta task"}, nil)
	m := newSessionModal(fs, "")
	next, _ := m.update(tea.KeyPressMsg{Text: "bet"})
	sm := next.(*sessionModal)
	list := sm.filtered()
	if len(list) != 1 || list[0].ID != "b" {
		t.Fatalf("filter bet = %+v", list)
	}
	view := sm.view(72, theme.Default().Resolve())
	if !strings.Contains(view, "beta task") {
		t.Errorf("missing beta:\n%s", view)
	}
	if strings.Contains(view, "alpha work") {
		t.Errorf("alpha should be filtered out:\n%s", view)
	}
}

func TestSessionModalRenamePersistsViaHost(t *testing.T) {
	fs := newFakeSessions()
	fs.put(host.Session{ID: "s1", Title: "old name"}, nil)
	m := newSessionModal(fs, "")
	next, _ := m.update(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	sm := next.(*sessionModal)
	if sm.phase != sessionPhaseRename {
		t.Fatalf("phase = %v, want rename", sm.phase)
	}
	// Clear prefilled title then type new.
	for range sm.renameBuf {
		next, _ = sm.update(tea.KeyPressMsg{Code: tea.KeyBackspace})
		sm = next.(*sessionModal)
	}
	next, _ = sm.update(tea.KeyPressMsg{Text: "fresh title"})
	sm = next.(*sessionModal)
	next, _ = sm.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	sm = next.(*sessionModal)
	if sm.phase != sessionPhaseBrowse {
		t.Fatalf("phase after save = %v", sm.phase)
	}
	got, ok, err := fs.Get("s1")
	if err != nil || !ok || got.Title != "fresh title" {
		t.Fatalf("host title = %+v ok=%v err=%v", got, ok, err)
	}
	view := sm.view(72, theme.Default().Resolve())
	if !strings.Contains(view, "fresh title") {
		t.Errorf("view missing renamed title:\n%s", view)
	}
}

func TestSessionModalDeleteRemovesFromList(t *testing.T) {
	fs := newFakeSessions()
	fs.put(host.Session{ID: "keep", Title: "keep me"}, nil)
	fs.put(host.Session{ID: "gone", Title: "delete me"}, nil)
	m := newSessionModal(fs, "keep")
	// Select "gone".
	list := m.filtered()
	for i, s := range list {
		if s.ID == "gone" {
			m.cursor = i
			break
		}
	}
	next, _ := m.update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	sm := next.(*sessionModal)
	if sm.phase != sessionPhaseConfirmDelete || sm.deleteID != "gone" {
		t.Fatalf("confirm: phase=%v id=%q", sm.phase, sm.deleteID)
	}
	next, _ = sm.update(tea.KeyPressMsg{Text: "y"})
	sm = next.(*sessionModal)
	if sm.phase != sessionPhaseBrowse {
		t.Fatalf("phase after delete = %v", sm.phase)
	}
	if _, ok, _ := fs.Get("gone"); ok {
		t.Fatal("gone still in host")
	}
	if len(sm.all) != 1 || sm.all[0].ID != "keep" {
		t.Fatalf("list after delete = %+v", sm.all)
	}
}

func TestSessionModalDeleteOpenRequiresForce(t *testing.T) {
	fs := newFakeSessions()
	fs.put(host.Session{ID: "open-one", Title: "busy", Open: true}, nil)
	m := newSessionModal(fs, "other")
	next, _ := m.update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	sm := next.(*sessionModal)
	// y without force should stay in confirm.
	next, _ = sm.update(tea.KeyPressMsg{Text: "y"})
	sm = next.(*sessionModal)
	if sm.phase != sessionPhaseConfirmDelete {
		t.Fatalf("phase = %v, want still confirm", sm.phase)
	}
	if _, ok, _ := fs.Get("open-one"); !ok {
		t.Fatal("open session deleted without force")
	}
	next, _ = sm.update(tea.KeyPressMsg{Text: "f"})
	sm = next.(*sessionModal)
	if !sm.deleteForce {
		t.Fatal("force not armed")
	}
	next, _ = sm.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	sm = next.(*sessionModal)
	if sm.phase != sessionPhaseBrowse {
		t.Fatalf("phase = %v after force delete", sm.phase)
	}
	if _, ok, _ := fs.Get("open-one"); ok {
		t.Fatal("open session still present after force")
	}
}

func TestSessionModalDeleteCurrentRequiresForce(t *testing.T) {
	fs := newFakeSessions()
	fs.put(host.Session{ID: "cur", Title: "current"}, nil)
	m := newSessionModal(fs, "cur")
	next, _ := m.update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	sm := next.(*sessionModal)
	next, _ = sm.update(tea.KeyPressMsg{Text: "y"})
	sm = next.(*sessionModal)
	if sm.phase != sessionPhaseConfirmDelete {
		t.Fatalf("current delete without force left confirm: phase=%v", sm.phase)
	}
	if _, ok, _ := fs.Get("cur"); !ok {
		t.Fatal("current deleted without force")
	}
	sm.deleteForce = true
	next, _ = sm.update(tea.KeyPressMsg{Text: "y"})
	sm = next.(*sessionModal)
	if _, ok, _ := fs.Get("cur"); ok {
		t.Fatal("current still present after force")
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
