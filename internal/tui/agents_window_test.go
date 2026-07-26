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
	if !strings.Contains(plain, "no subagents this session") {
		t.Fatalf("empty view = %q", plain)
	}
	for _, line := range strings.Split(plain, "\n") {
		if got := lipgloss.Width(line); got > 32 {
			t.Errorf("line width %d > 32: %q", got, line)
		}
	}
}

func TestAgentsWindowListsParentChildrenOnly(t *testing.T) {
	now := time.Now()
	w := newAgentsWindow().resize(48, 8).(agentsWindow)
	next, _ := w.update(agentsStateMsg{
		parentID: "root-sess",
		children: []childActivity{
			{sessionID: "child-a", agent: "explore", prompt: "scan", status: "running", startedAt: now.Add(-5 * time.Second)},
			{sessionID: "child-b", agent: "general", prompt: "fix", status: string(protocol.ChildStatusCompleted), startedAt: now.Add(-30 * time.Second), endedAt: now.Add(-2 * time.Second)},
			{sessionID: "child", agent: "ghost", status: "running"}, // placeholder dropped
		},
	})
	w = next.(agentsWindow)
	plain := ansi.Strip(w.view(theme.Default()))
	if !strings.Contains(plain, "explore") {
		t.Errorf("missing explore: %q", plain)
	}
	if !strings.Contains(plain, "general") {
		t.Errorf("missing general: %q", plain)
	}
	if !strings.Contains(plain, "running") {
		t.Errorf("missing running: %q", plain)
	}
	if !strings.Contains(plain, "done") {
		t.Errorf("missing done: %q", plain)
	}
	if strings.Contains(plain, "ghost") {
		t.Errorf("placeholder child leaked: %q", plain)
	}
	// No root session row — agents pane is children-only.
	if strings.Contains(plain, "root-sess") {
		t.Errorf("listed parent root id: %q", plain)
	}
	for _, line := range strings.Split(plain, "\n") {
		if got := lipgloss.Width(line); got > 48 {
			t.Errorf("line width %d > 48: %q", got, line)
		}
	}
}

func TestAgentsWindowFailedStatus(t *testing.T) {
	w := newAgentsWindow().resize(40, 4).(agentsWindow)
	next, _ := w.update(agentsStateMsg{
		children: []childActivity{
			{sessionID: "c-fail", agent: "build", status: string(protocol.ChildStatusFailed)},
		},
	})
	plain := ansi.Strip(next.view(theme.Default()))
	if !strings.Contains(plain, "failed") || !strings.Contains(plain, "build") {
		t.Fatalf("failed row missing: %q", plain)
	}
}

func TestAgentsWindowEnterOpensChild(t *testing.T) {
	w := newAgentsWindow().resize(40, 6).(agentsWindow)
	next, _ := w.update(agentsStateMsg{
		children: []childActivity{
			{sessionID: "child-1", agent: "explore", status: "running"},
			{sessionID: "child-2", agent: "general", status: string(protocol.ChildStatusCompleted)},
		},
	})
	w = next.(agentsWindow)
	// Move to second row and enter.
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

	// Activate agents pane and focus right so window update receives keys.
	reg, ok := m.windows.activate(agentsWindowID)
	if !ok {
		t.Fatal("activate agents")
	}
	m.windows = reg
	m.focus = focusRight

	// Two running children.
	m.applyEvent(protocol.ChildStarted{
		Correlation: protocol.Correlation{SessionID: "c1", ParentSessionID: "parent-1", Depth: 1},
		Agent:       "explore",
		Prompt:      "one",
	})
	// applyEvent returns broadcast cmd — push snapshot into windows.
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
	if strings.Count(plain, "running") < 2 {
		t.Fatalf("want both running: %q", plain)
	}

	// Complete both without restart.
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
	if !strings.Contains(plain, "done") {
		t.Errorf("missing done: %q", plain)
	}
	if !strings.Contains(plain, "failed") {
		t.Errorf("missing failed: %q", plain)
	}
	if strings.Contains(plain, "running") {
		t.Errorf("still shows running after complete: %q", plain)
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
