package tui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/protocol"
)

func TestCellsFromEventsRebuildsTranscript(t *testing.T) {
	events := []protocol.Event{
		protocol.UserMessage{Text: "do work"},
		protocol.TextDelta{Text: "hello "},
		protocol.TextDelta{Text: "world"},
		protocol.ToolCallBegin{CallID: "c1", Name: "bash", Args: json.RawMessage(`{"command":"ls"}`)},
		protocol.ToolCallOutput{CallID: "c1", Data: "out"},
		protocol.ToolCallEnd{CallID: "c1", Title: "ls", Output: "out", IsError: false},
		protocol.TurnCompleted{StopReason: "end_turn"},
	}
	cells, tools := cellsFromEvents(events)
	if len(cells) != 3 {
		t.Fatalf("cells = %d, want 3 (user, assistant, tool)", len(cells))
	}
	if _, ok := cells[0].(*userCell); !ok {
		t.Fatalf("cell0 type = %T, want *userCell", cells[0])
	}
	ac, ok := cells[1].(*assistantCell)
	if !ok {
		t.Fatalf("cell1 type = %T, want *assistantCell", cells[1])
	}
	if ac.text != "hello world" || !ac.complete {
		t.Errorf("assistant = %+v", ac)
	}
	tc, ok := cells[2].(*toolCell)
	if !ok {
		t.Fatalf("cell2 type = %T, want *toolCell", cells[2])
	}
	if !tc.done || tc.output != "out" || tools["c1"] != tc {
		t.Errorf("tool = %+v tools=%v", tc, tools)
	}
}

func TestSessionNavCtrlXDownOpensChildTranscript(t *testing.T) {
	fs := newFakeSessions()
	childLog := mustSessionJSONL(t,
		protocol.UserMessage{Text: "child prompt"},
		protocol.TextDelta{Text: "child says hi"},
		protocol.TurnCompleted{StopReason: "end_turn"},
	)
	fs.put(host.Session{ID: "child-1", ParentID: "root-sess", Title: "explore: child prompt"}, childLog)

	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "root-sess"
	m.services.Sessions = fs
	m.children = []childActivity{{
		sessionID: "child-1",
		agent:     "explore",
		prompt:    "child prompt",
		status:    string(protocol.ChildStatusCompleted),
	}}
	m.cells = []cell{&userCell{text: "root prompt"}}
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 40})

	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlX})
	if !m.leaderArmed {
		t.Fatal("leader not armed after ctrl+x")
	}
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyDown})
	if !m.viewingChild() || m.viewingID != "child-1" {
		t.Fatalf("viewingID = %q, want child-1", m.viewingID)
	}
	if len(m.viewCells) < 2 {
		t.Fatalf("viewCells = %d, want rebuilt transcript", len(m.viewCells))
	}
	// Force viewport content for the child cells.
	m.refreshViewport()
	plain := ansi.Strip(m.View())
	if !strings.Contains(plain, "child says hi") {
		t.Errorf("view missing child text:\n%s", plain)
	}
	if !strings.Contains(plain, "explore") && !strings.Contains(plain, "child prompt") {
		t.Errorf("view missing child title:\n%s", plain)
	}
	if len(m.cells) != 1 {
		t.Errorf("root cells mutated: %d", len(m.cells))
	}

	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.viewingChild() {
		t.Fatal("still viewing child after esc")
	}
}

func TestSessionNavSiblingCycle(t *testing.T) {
	fs := newFakeSessions()
	fs.put(host.Session{ID: "c1", ParentID: "root", Title: "one"}, mustSessionJSONL(t, protocol.UserMessage{Text: "a"}))
	fs.put(host.Session{ID: "c2", ParentID: "root", Title: "two"}, mustSessionJSONL(t, protocol.UserMessage{Text: "b"}))

	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "root"
	m.services.Sessions = fs
	m.children = []childActivity{
		{sessionID: "c1", agent: "a", status: "completed"},
		{sessionID: "c2", agent: "b", status: "completed"},
	}
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 30})

	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlX})
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyDown})
	if m.viewingID != "c1" {
		t.Fatalf("first child = %q", m.viewingID)
	}
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyRight})
	if m.viewingID != "c2" {
		t.Fatalf("next sibling = %q, want c2", m.viewingID)
	}
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyLeft})
	if m.viewingID != "c1" {
		t.Fatalf("prev sibling = %q, want c1", m.viewingID)
	}
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyUp})
	if m.viewingChild() {
		t.Fatal("up should return to parent")
	}
}

func TestSessionNavLeaderTimeout(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlX})
	if !m.leaderArmed {
		t.Fatal("expected leader armed")
	}
	gen := m.leaderGen
	m = updateApp(t, m, leaderExpiredMsg{gen: gen})
	if m.leaderArmed {
		t.Fatal("leader still armed after expiry")
	}
}

func TestSessionNavNoChildrenNotice(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "root"
	m.services.Sessions = newFakeSessions()
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlX})
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyDown})
	if m.viewingChild() {
		t.Fatal("opened child without any children")
	}
	if !strings.Contains(m.notice, "no subagent") {
		t.Errorf("notice = %q", m.notice)
	}
}

func TestActivityPaneRendersSessionTree(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "root"
	m.titleTopic = "main task"
	m.children = []childActivity{
		{sessionID: "c1", agent: "explore", prompt: "find bugs", status: "running"},
	}
	body := ansi.Strip(m.activityPaneBody(48, 8))
	if !strings.Contains(body, "main task") && !strings.Contains(body, "session") {
		t.Errorf("missing root node: %q", body)
	}
	if !strings.Contains(body, "explore") && !strings.Contains(body, "find bugs") {
		t.Errorf("missing child node: %q", body)
	}
}

func TestChildCompletedRefreshesViewingTranscript(t *testing.T) {
	fs := newFakeSessions()
	fs.put(host.Session{ID: "c1", ParentID: "root", Title: "work"}, mustSessionJSONL(t,
		protocol.UserMessage{Text: "go"},
	))
	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "root"
	m.services.Sessions = fs
	m.children = []childActivity{{sessionID: "c1", agent: "build", status: "running"}}
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 30})
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlX})
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyDown})
	if !m.viewingChild() {
		t.Fatal("not viewing")
	}
	fs.logs["c1"] = mustSessionJSONL(t,
		protocol.UserMessage{Text: "go"},
		protocol.TextDelta{Text: "done now"},
		protocol.TurnCompleted{StopReason: "end_turn"},
	)
	m.applyEvent(protocol.ChildCompleted{
		Correlation: protocol.Correlation{SessionID: "c1", ParentSessionID: "root", Depth: 1},
		Status:      protocol.ChildStatusCompleted,
		Summary:     "done",
	})
	m.refreshViewingTranscript()
	found := false
	for _, c := range m.viewCells {
		if a, ok := c.(*assistantCell); ok && strings.Contains(a.text, "done now") {
			found = true
		}
	}
	if !found {
		t.Fatalf("view cells not refreshed: %#v", m.viewCells)
	}
}

func TestSeedFromReplayNoLiveSideEffects(t *testing.T) {
	events := []protocol.Event{
		protocol.UserMessage{Text: "hello session title candidate"},
		protocol.TurnStarted{},
		protocol.TextDelta{Text: "partial"},
		protocol.PermissionAsked{RequestID: "p1", Permission: "bash", Patterns: []string{"rm -rf /"}},
		protocol.QuestionAsked{RequestID: "q1"},
		protocol.PhaseChanged{Workflow: "plan-implement", Phase: "plan", Index: 0},
		protocol.AutonomySelected{Mode: protocol.AutonomyAgent},
		protocol.FastSelected{Enabled: true},
		protocol.AgentSelected{Name: "plan"},
		protocol.ModelSelected{Provider: "echo", Model: "echo"},
		protocol.ChildStarted{
			Correlation: protocol.Correlation{SessionID: "child-1", ParentSessionID: "root", Depth: 1},
			Agent:       "build",
			Prompt:      "do stuff",
		},
		// no ChildCompleted, no TurnCompleted, no PermissionResolved
	}
	m, _ := newAppTestModelWithOptions(Options{Replay: events, SessionID: "root"})
	if m.turnRunning {
		t.Fatal("turnRunning true after replay seed")
	}
	if m.awaitingPermission {
		t.Fatal("awaitingPermission true after replay seed")
	}
	if m.modal != nil {
		t.Fatalf("modal = %T, want nil", m.modal)
	}
	if m.phaseName != "plan" || m.phaseWorkflow != "plan-implement" {
		t.Fatalf("phase = %q/%q", m.phaseWorkflow, m.phaseName)
	}
	if m.autonomy != protocol.AutonomyAgent {
		t.Fatalf("autonomy = %q", m.autonomy)
	}
	if !m.fastEnabled {
		t.Fatal("fastEnabled false")
	}
	if m.agentName != "plan" || m.providerName != "echo" {
		t.Fatalf("agent/provider = %q/%q", m.agentName, m.providerName)
	}
	if m.titleTopic == "" {
		t.Fatal("titleTopic empty")
	}
	if len(m.cells) < 2 {
		t.Fatalf("cells = %d, want transcript", len(m.cells))
	}
	if len(m.children) != 1 {
		t.Fatalf("children = %d, want 1", len(m.children))
	}
	if m.children[0].status != string(protocol.ChildStatusCanceled) {
		t.Fatalf("incomplete child status = %q, want canceled", m.children[0].status)
	}
	if m.childIsRunning(m.children[0].sessionID) {
		t.Fatal("childIsRunning true for incomplete ChildStarted on resume")
	}
}

func TestChildrenFromEventsMarksIncompleteCanceled(t *testing.T) {
	events := []protocol.Event{
		protocol.ChildStarted{Correlation: protocol.Correlation{SessionID: "c1"}, Agent: "a", Prompt: "p"},
		protocol.ChildStarted{Correlation: protocol.Correlation{SessionID: "c2"}, Agent: "b", Prompt: "q"},
		protocol.ChildCompleted{Correlation: protocol.Correlation{SessionID: "c2"}, Status: protocol.ChildStatusCompleted},
	}
	got := childrenFromEvents(events)
	if len(got) != 2 {
		t.Fatalf("len = %d: %#v", len(got), got)
	}
	byID := map[string]string{}
	for _, ch := range got {
		byID[ch.sessionID] = ch.status
	}
	if byID["c1"] != string(protocol.ChildStatusCanceled) {
		t.Errorf("c1 = %q, want canceled", byID["c1"])
	}
	if byID["c2"] != string(protocol.ChildStatusCompleted) {
		t.Errorf("c2 = %q, want completed", byID["c2"])
	}
}

func mustSessionJSONL(t *testing.T, events ...protocol.Event) []byte {
	t.Helper()
	var b strings.Builder
	for _, ev := range events {
		env, err := protocol.Wrap(ev)
		if err != nil {
			t.Fatal(err)
		}
		env.Time = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		line, err := json.Marshal(env)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	return []byte(b.String())
}
