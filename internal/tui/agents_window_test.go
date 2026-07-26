package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

func TestAgentsWindowEmptyState(t *testing.T) {
	w := newAgentsWindow().resize(32, 6).(agentsWindow)
	plain := ansi.Strip(w.view(theme.Default()))
	if !strings.Contains(plain, "no subagents") {
		t.Fatalf("empty view = %q", plain)
	}
	for _, line := range strings.Split(plain, "\n") {
		if got := lipgloss.Width(line); got > 32 {
			t.Errorf("line width %d > 32: %q", got, line)
		}
	}
}

func TestAgentsWindowMultiRootTree(t *testing.T) {
	w := newAgentsWindow().resize(48, 10).(agentsWindow)
	next, _ := w.update(agentsStateMsg{
		activeID:  "root-a",
		viewingID: "root-a",
		roots: []agentsRootSnap{
			{
				ID:    "root-a",
				Title: "first task",
				State: theme.AgentStateWorking,
				Children: []childActivity{
					{sessionID: "child-a", parentID: "root-a", agent: "explore", prompt: "scan", status: "running"},
				},
			},
			{
				ID:    "root-b",
				Title: "second task",
				State: theme.AgentStateReady,
			},
		},
	})
	w = next.(agentsWindow)
	plain := ansi.Strip(w.view(theme.Default()))
	if !strings.Contains(plain, "first task") {
		t.Errorf("missing root-a: %q", plain)
	}
	if !strings.Contains(plain, "second task") {
		t.Errorf("missing root-b: %q", plain)
	}
	if !strings.Contains(plain, "explore") && !strings.Contains(plain, "scan") {
		t.Errorf("missing child: %q", plain)
	}
	if !strings.Contains(plain, "working") && !strings.Contains(plain, "ready") {
		t.Errorf("missing status detail: %q", plain)
	}
	for _, line := range strings.Split(plain, "\n") {
		if got := lipgloss.Width(line); got > 48 {
			t.Errorf("line width %d > 48: %q", got, line)
		}
	}
}

func TestAgentsWindowEnterOpensRoot(t *testing.T) {
	w := newAgentsWindow().resize(40, 6).(agentsWindow)
	next, _ := w.update(agentsStateMsg{
		activeID: "root-a",
		roots: []agentsRootSnap{
			{ID: "root-a", Title: "a", State: theme.AgentStateReady},
			{ID: "root-b", Title: "b", State: theme.AgentStateReady},
		},
	})
	w = next.(agentsWindow)
	next, _ = w.update(tea.KeyMsg{Type: tea.KeyDown})
	w = next.(agentsWindow)
	next, cmd := w.update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter produced no cmd")
	}
	msg := cmd()
	om, ok := msg.(agentsOpenMsg)
	if !ok || om.sessionID != "root-b" {
		t.Fatalf("open msg = %#v, want agentsOpenMsg{root-b}", msg)
	}
	_ = next
}

func TestAgentsWindowSpawnKey(t *testing.T) {
	w := newAgentsWindow().resize(40, 4).(agentsWindow)
	_, cmd := w.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if cmd == nil {
		t.Fatal("n produced no cmd")
	}
	if _, ok := cmd().(agentsSpawnMsg); !ok {
		t.Fatalf("want agentsSpawnMsg, got %#v", cmd())
	}
}

func TestAgentsWindowEnterOpensChild(t *testing.T) {
	w := newAgentsWindow().resize(40, 6).(agentsWindow)
	next, _ := w.update(agentsStateMsg{
		activeID: "root",
		roots: []agentsRootSnap{
			{
				ID:    "root",
				Title: "main",
				State: theme.AgentStateReady,
				Children: []childActivity{
					{sessionID: "child-1", parentID: "root", agent: "explore", status: "running"},
					{sessionID: "child-2", parentID: "root", agent: "general", status: string(protocol.ChildStatusCompleted)},
				},
			},
		},
	})
	w = next.(agentsWindow)
	// cursor 0 = root; down to first child; down to second
	next, _ = w.update(tea.KeyMsg{Type: tea.KeyDown})
	w = next.(agentsWindow)
	next, _ = w.update(tea.KeyMsg{Type: tea.KeyDown})
	w = next.(agentsWindow)
	next, cmd := w.update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter produced no cmd")
	}
	msg := cmd()
	om, ok := msg.(agentsOpenMsg)
	if !ok || om.sessionID != "child-2" {
		t.Fatalf("open msg = %#v, want agentsOpenMsg{child-2}", msg)
	}
	_ = next
}

func TestAgentsWindowLiveStatusViaModel(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "parent-1"
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})

	reg, ok := m.windows.activate(agentsWindowID)
	if !ok {
		t.Fatal("activate agents")
	}
	m.windows = reg
	m.focus = focusRight

	m.applyEvent(protocol.ChildStarted{
		Correlation: protocol.Correlation{SessionID: "c1", ParentSessionID: "parent-1", Depth: 1},
		Agent:       "explore",
		Prompt:      "one",
	})
	m.windows, _ = m.windows.broadcast(m.agentsStateSnapshot())
	m.applyEvent(protocol.ChildStarted{
		Correlation: protocol.Correlation{SessionID: "c2", ParentSessionID: "parent-1", Depth: 1},
		Agent:       "general",
		Prompt:      "two",
	})
	m.windows, _ = m.windows.broadcast(m.agentsStateSnapshot())

	aw := agentsWindowFrom(t, m)
	plain := ansi.Strip(aw.view(theme.Default()))
	if !strings.Contains(plain, "explore") || !strings.Contains(plain, "general") {
		t.Fatalf("running rows missing: %q", plain)
	}

	m.applyEvent(protocol.ChildCompleted{
		Correlation: protocol.Correlation{SessionID: "c1", ParentSessionID: "parent-1", Depth: 1},
		Status:      protocol.ChildStatusCompleted,
	})
	m.windows, _ = m.windows.broadcast(m.agentsStateSnapshot())
	m.applyEvent(protocol.ChildCompleted{
		Correlation: protocol.Correlation{SessionID: "c2", ParentSessionID: "parent-1", Depth: 1},
		Status:      protocol.ChildStatusFailed,
	})
	m.windows, _ = m.windows.broadcast(m.agentsStateSnapshot())

	aw = agentsWindowFrom(t, m)
	plain = ansi.Strip(aw.view(theme.Default()))
	if !strings.Contains(plain, "completed") && !strings.Contains(plain, "done") && !strings.Contains(plain, "failed") {
		// Tree detail uses raw status "completed"/"failed"
		if !strings.Contains(plain, "failed") {
			t.Errorf("missing failed status: %q", plain)
		}
	}
}

func TestAgentsOpenMsgNavigatesTranscript(t *testing.T) {
	fs := newFakeSessions()
	childLog := mustSessionJSONL(t,
		protocol.UserMessage{Text: "child work"},
		protocol.TextDelta{Text: "child reply here"},
	)
	fs.put(host.Session{ID: "child-nav", ParentID: "root", Title: "explore: child work"}, childLog)

	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "root"
	m.services.Sessions = fs
	m.children = []childActivity{{
		sessionID: "child-nav",
		agent:     "explore",
		status:    string(protocol.ChildStatusCompleted),
	}}
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})

	m = updateApp(t, m, agentsOpenMsg{sessionID: "child-nav"})
	if !m.viewingChild() || m.viewingID != "child-nav" {
		t.Fatalf("viewingID = %q, want child-nav", m.viewingID)
	}
	plain := ansi.Strip(m.View())
	if !strings.Contains(plain, "child reply here") {
		t.Errorf("view missing child transcript:\n%s", plain)
	}
}

func TestAgentsAgeLabel(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	ch := childActivity{
		startedAt: now.Add(-65 * time.Second),
		endedAt:   now.Add(-5 * time.Second),
		status:    string(protocol.ChildStatusCompleted),
	}
	if got := agentsAgeLabel(ch, now); got != "1m 0s" {
		t.Errorf("completed age = %q, want 1m 0s", got)
	}
	running := childActivity{startedAt: now.Add(-12 * time.Second), status: "running"}
	if got := agentsAgeLabel(running, now); got != "12s" {
		t.Errorf("running age = %q, want 12s", got)
	}
	if got := agentsAgeLabel(childActivity{}, now); got != "" {
		t.Errorf("zero start = %q, want empty", got)
	}
}

func TestMultiRootSwitchTranscript(t *testing.T) {
	fr := &fakeRoots{
		active: "root-a",
		live:   []string{"root-a", "root-b"},
		dirs:   map[string]string{"root-a": "/a", "root-b": "/b"},
	}
	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "root-a"
	m.workDir = "/a"
	m.titleTopic = "alpha"
	m.cells = []cell{&userCell{text: "from a"}}
	m.services.Roots = fr
	m.roots = map[string]*rootPane{
		"root-b": {
			sessionID:  "root-b",
			workDir:    "/b",
			titleTopic: "beta",
			cells:      []cell{&userCell{text: "from b"}},
			toolByID:   map[string]*toolCell{},
		},
	}
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 40})
	m = updateApp(t, m, agentsOpenMsg{sessionID: "root-b"})
	if m.sessionID != "root-b" {
		t.Fatalf("sessionID = %q, want root-b", m.sessionID)
	}
	if fr.active != "root-b" {
		t.Fatalf("roots active = %q", fr.active)
	}
	if len(m.cells) != 1 {
		t.Fatalf("cells = %d", len(m.cells))
	}
	if uc, ok := m.cells[0].(*userCell); !ok || uc.text != "from b" {
		t.Fatalf("cell = %#v, want from b", m.cells[0])
	}
	// Stashed root-a still has its transcript.
	if p := m.roots["root-a"]; p == nil || len(p.cells) != 1 {
		t.Fatalf("stashed root-a = %#v", m.roots["root-a"])
	}
}

func TestBackgroundRootEventUpdatesPane(t *testing.T) {
	fr := &fakeRoots{
		active: "root-a",
		live:   []string{"root-a", "root-b"},
	}
	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "root-a"
	m.services.Roots = fr
	m.roots = map[string]*rootPane{
		"root-b": {sessionID: "root-b", toolByID: map[string]*toolCell{}},
	}
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 30})
	m = updateApp(t, m, engineEventMsg{ev: protocol.TurnStarted{
		Correlation: protocol.Correlation{SessionID: "root-b"},
	}})
	p := m.roots["root-b"]
	if p == nil || !p.turnRunning {
		t.Fatalf("background turn not tracked: %#v", p)
	}
	if m.turnRunning {
		t.Fatal("active root should not be running")
	}
	if st := m.rootAgentState("root-b"); st != theme.AgentStateWorking {
		t.Fatalf("root-b state = %v, want working", st)
	}
}

func agentsWindowFrom(t *testing.T, m Model) agentsWindow {
	t.Helper()
	for _, w := range m.windows.windows {
		if aw, ok := w.(agentsWindow); ok {
			return aw
		}
	}
	t.Fatal("agents window missing from registry")
	return agentsWindow{}
}

// fakeRoots is a scriptable host.Roots for TUI tests.
type fakeRoots struct {
	active string
	live   []string
	dirs   map[string]string
	err    error
}

func (f *fakeRoots) ActiveID() string { return f.active }
func (f *fakeRoots) LiveIDs() []string {
	out := append([]string(nil), f.live...)
	return out
}
func (f *fakeRoots) Activate(id string) error {
	if f.err != nil {
		return f.err
	}
	for _, live := range f.live {
		if live == id {
			f.active = id
			return nil
		}
	}
	return errFake("not live")
}
func (f *fakeRoots) Spawn() (string, error) {
	if f.err != nil {
		return "", f.err
	}
	id := "root-new"
	f.live = append(f.live, id)
	f.active = id
	return id, nil
}
func (f *fakeRoots) Open(id string) error { return f.Activate(id) }
func (f *fakeRoots) Interrupt(string) error {
	if f.err != nil {
		return f.err
	}
	return nil
}
func (f *fakeRoots) WorkDir(id string) string {
	if f.dirs != nil {
		return f.dirs[id]
	}
	return ""
}

type fakeError string

func (e fakeError) Error() string { return string(e) }
func errFake(s string) error      { return fakeError(s) }
