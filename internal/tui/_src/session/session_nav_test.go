package tui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
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

	m = updateApp(t, m, tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	if !m.leaderArmed {
		t.Fatal("leader not armed after ctrl+x")
	}
	m = updateApp(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	if !m.viewingChild() || m.viewingID != "child-1" {
		t.Fatalf("viewingID = %q, want child-1", m.viewingID)
	}
	if len(m.viewCells) < 2 {
		t.Fatalf("viewCells = %d, want rebuilt transcript", len(m.viewCells))
	}
	// Force viewport content for the child cells.
	m.refreshViewport()
	plain := ansi.Strip(viewString(m))
	if !strings.Contains(plain, "child says hi") {
		t.Errorf("view missing child text:\n%s", plain)
	}
	if !strings.Contains(plain, "explore") && !strings.Contains(plain, "child prompt") {
		t.Errorf("view missing child title:\n%s", plain)
	}
	if len(m.cells) != 1 {
		t.Errorf("root cells mutated: %d", len(m.cells))
	}

	m = updateApp(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
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

	m = updateApp(t, m, tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	m = updateApp(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	if m.viewingID != "c1" {
		t.Fatalf("first child = %q", m.viewingID)
	}
	m = updateApp(t, m, tea.KeyPressMsg{Code: tea.KeyRight})
	if m.viewingID != "c2" {
		t.Fatalf("next sibling = %q, want c2", m.viewingID)
	}
	m = updateApp(t, m, tea.KeyPressMsg{Code: tea.KeyLeft})
	if m.viewingID != "c1" {
		t.Fatalf("prev sibling = %q, want c1", m.viewingID)
	}
	m = updateApp(t, m, tea.KeyPressMsg{Code: tea.KeyUp})
	if m.viewingChild() {
		t.Fatal("up should return to parent")
	}
}

func TestSessionNavLeaderTimeout(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
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
	m = updateApp(t, m, tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	m = updateApp(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
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

func TestSessionTreeShowsNestedGrandchildren(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "root"
	m.titleTopic = "root work"
	m.children = []childActivity{
		{sessionID: "c1", parentID: "root", agent: "general", prompt: "mid task", status: "running"},
		{sessionID: "c1a", parentID: "c1", agent: "explore", prompt: "leaf task", status: "running"},
	}
	nodes := m.sessionTreeNodes()
	if len(nodes) != 1 {
		t.Fatalf("roots = %d, want 1", len(nodes))
	}
	if len(nodes[0].Children) != 1 {
		t.Fatalf("root children = %d, want 1 (grandchild not flattened)", len(nodes[0].Children))
	}
	mid := nodes[0].Children[0]
	if mid.ID != "c1" {
		t.Errorf("mid id = %q, want c1", mid.ID)
	}
	if mid.Leaf || len(mid.Children) != 1 {
		t.Fatalf("mid children = %d leaf=%v, want 1 grandchild", len(mid.Children), mid.Leaf)
	}
	if mid.Children[0].ID != "c1a" {
		t.Errorf("grandchild id = %q, want c1a", mid.Children[0].ID)
	}
	body := ansi.Strip(m.activityPaneBody(48, 10))
	if !strings.Contains(body, "mid task") && !strings.Contains(body, "general") {
		t.Errorf("missing mid node in body: %q", body)
	}
	if !strings.Contains(body, "leaf task") && !strings.Contains(body, "explore") {
		t.Errorf("missing grandchild in body: %q", body)
	}
}

func TestSubmitBlockedWhileViewingChild(t *testing.T) {
	fs := newFakeSessions()
	fs.put(host.Session{ID: "child-1", ParentID: "root", Title: "explore"}, mustSessionJSONL(t,
		protocol.UserMessage{Text: "child work"},
	))

	m, ops := newAppTestModel(nil, nil)
	m.sessionID = "root"
	m.providerName = "echo"
	m.services.Sessions = fs
	m.children = []childActivity{{
		sessionID: "child-1",
		agent:     "explore",
		status:    string(protocol.ChildStatusCompleted),
	}}
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 40})
	m = updateApp(t, m, tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	m = updateApp(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	if !m.viewingChild() {
		t.Fatal("expected child view")
	}

	const draft = "should not go to parent"
	m.composer.SetValue(draft)
	m = updateApp(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	assertNoAppOp(t, ops)
	if m.composer.Value() != draft {
		t.Fatalf("composer = %q, want draft kept", m.composer.Value())
	}
	if !m.noticeErr || !strings.Contains(m.notice, "subagent") {
		t.Fatalf("notice = %q (err=%v), want subagent send block", m.notice, m.noticeErr)
	}
	if !m.viewingChild() {
		t.Fatal("send should not leave child view")
	}
}

func TestSkillSubmitBlockedWhileViewingChild(t *testing.T) {
	fs := newFakeSessions()
	fs.put(host.Session{ID: "child-1", ParentID: "root", Title: "explore"}, mustSessionJSONL(t,
		protocol.UserMessage{Text: "child work"},
	))
	skill := fakeSkill("review", "review code", "Review: $ARGUMENTS")

	m, ops := newAppTestModel(nil, []host.Skill{skill})
	m.sessionID = "root"
	m.providerName = "echo"
	m.services.Sessions = fs
	m.children = []childActivity{{
		sessionID: "child-1",
		agent:     "explore",
		status:    string(protocol.ChildStatusCompleted),
	}}
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 40})
	m = updateApp(t, m, tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	m = updateApp(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	if !m.viewingChild() {
		t.Fatal("expected child view")
	}

	m.composer.SetValue("/review this diff")
	m = updateApp(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	assertNoAppOp(t, ops)
	if m.composer.Value() != "/review this diff" {
		t.Fatalf("composer = %q, want skill draft kept", m.composer.Value())
	}
	if !m.noticeErr || !strings.Contains(m.notice, "subagent") {
		t.Fatalf("notice = %q (err=%v), want subagent send block", m.notice, m.noticeErr)
	}
}

func TestDecodeSessionJSONLSkipsTrailingPartialLine(t *testing.T) {
	good := mustSessionJSONL(t,
		protocol.UserMessage{Text: "go"},
		protocol.TextDelta{Text: "partial ok"},
	)
	// Append a truncated JSON line as a live writer might mid-append.
	raw := append(append([]byte{}, good...), []byte(`{"type":"text.delta","payload":{"text":"cut`)...)
	events, err := decodeSessionJSONL(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2 complete lines (skip trailing partial)", len(events))
	}
	// A corrupt middle line must still fail.
	badMid := mustSessionJSONL(t, protocol.UserMessage{Text: "a"})
	badMid = append(badMid, []byte("not-json\n")...)
	badMid = append(badMid, mustSessionJSONL(t, protocol.TextDelta{Text: "b"})...)
	if _, err := decodeSessionJSONL(badMid); err == nil {
		t.Fatal("expected error for corrupt non-trailing line")
	}
}

func TestDecodeSessionJSONLSkipsSchemaHeader(t *testing.T) {
	body := mustSessionJSONL(t,
		protocol.UserMessage{Text: "hi"},
		protocol.TextDelta{Text: "there"},
	)
	hdr := []byte(`{"type":"session.header","schemaVersion":1,"time":"2020-01-01T00:00:00Z"}` + "\n")
	events, err := decodeSessionJSONL(append(hdr, body...))
	if err != nil {
		t.Fatalf("decode with header: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}
	future := []byte(`{"type":"session.header","schemaVersion":99,"time":"2020-01-01T00:00:00Z"}` + "\n")
	if _, err := decodeSessionJSONL(append(future, body...)); err == nil {
		t.Fatal("expected error for newer schema header")
	}
}

func TestCellsFromEventsLiveKeepsTrailingStreamIncomplete(t *testing.T) {
	// Trailing TextDelta with no TurnCompleted: finished path force-completes;
	// live path leaves the assistant incomplete (no glamour on partial md).
	streamEvents := []protocol.Event{
		protocol.UserMessage{Text: "work"},
		protocol.TextDelta{Text: "still streaming **md"},
	}
	doneCells, _ := cellsFromEvents(streamEvents)
	acDone, ok := doneCells[1].(*assistantCell)
	if !ok || !acDone.complete {
		t.Fatalf("finished path assistant complete=%v type=%T", ok && acDone.complete, doneCells[1])
	}
	liveStream, _ := cellsFromEventsLive(streamEvents)
	ac, ok := liveStream[1].(*assistantCell)
	if !ok {
		t.Fatalf("cell1 = %T, want *assistantCell", liveStream[1])
	}
	if ac.complete {
		t.Fatal("live path must not complete trailing assistant")
	}

	// Open tool at end of log: assistant is completed by ToolCallBegin (correct),
	// but the tool stays !done so live tails keep rendering.
	toolEvents := []protocol.Event{
		protocol.UserMessage{Text: "work"},
		protocol.TextDelta{Text: "calling tool"},
		protocol.ToolCallBegin{CallID: "c1", Name: "bash", Args: json.RawMessage(`{"command":"ls"}`)},
		protocol.ToolCallOutput{CallID: "c1", Data: "line\rprogress\x1b[31mred\x1b[0m"},
	}
	liveCells, tools := cellsFromEventsLive(toolEvents)
	if len(liveCells) < 3 {
		t.Fatalf("live cells = %d, want user+assistant+tool", len(liveCells))
	}
	acTool, ok := liveCells[1].(*assistantCell)
	if !ok || !acTool.complete {
		t.Fatalf("assistant before tool should be complete, ok=%v complete=%v", ok, ok && acTool.complete)
	}
	tc, ok := tools["c1"]
	if !ok || tc.done {
		t.Fatalf("live tool done=%v ok=%v", ok && tc.done, ok)
	}
	if !strings.Contains(tc.output, "progress") {
		t.Fatalf("tool output = %q", tc.output)
	}
}

func TestOpenRunningChildUsesLiveTranscriptAndRefresh(t *testing.T) {
	fs := newFakeSessions()
	fs.put(host.Session{ID: "c-live", ParentID: "root", Title: "general"}, mustSessionJSONL(t,
		protocol.UserMessage{Text: "do work"},
		protocol.TextDelta{Text: "thinking about it"},
		protocol.ToolCallBegin{CallID: "t1", Name: "bash", Args: json.RawMessage(`{"command":"echo hi"}`)},
		protocol.ToolCallOutput{CallID: "t1", Data: "hi\rbye\x1b[2J"},
	))
	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "root"
	m.services.Sessions = fs
	m.children = []childActivity{{
		sessionID: "c-live",
		agent:     "general",
		status:    "running",
	}}
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 40})
	cmd := m.openSessionView("c-live")
	if !m.viewingChild() || m.viewingID != "c-live" {
		t.Fatalf("viewingID = %q", m.viewingID)
	}
	// Live rebuild: open tool stays !done (assistant before tool is complete).
	var sawAssistant, sawTool bool
	for _, c := range m.viewCells {
		switch cell := c.(type) {
		case *assistantCell:
			sawAssistant = true
		case *toolCell:
			sawTool = true
			if cell.done {
				t.Error("running child tool should stay open")
			}
		}
	}
	if !sawAssistant || !sawTool {
		t.Fatalf("viewCells missing stream cells: %#v", m.viewCells)
	}
	// Refresh tick must be scheduled while running.
	if cmd == nil {
		t.Fatal("expected live refresh tick cmd")
	}
	// Render must not retain CR / CSI that would corrupt the alt screen.
	m.refreshViewport()
	frame := viewString(m)
	if strings.Contains(frame, "\r") {
		t.Fatal("rendered frame retained carriage return")
	}
	if strings.Contains(frame, "\x1b[2J") {
		t.Fatal("rendered frame retained clear-screen CSI from tool output")
	}
	plain := ansi.Strip(frame)
	if !strings.Contains(plain, "thinking about it") && !strings.Contains(plain, "bash") {
		t.Errorf("missing live child content:\n%s", plain)
	}
	if !strings.Contains(plain, "subagent running") {
		// Only when empty; with content we should show the transcript panel title.
		if !strings.Contains(plain, "general") && !strings.Contains(plain, "do work") {
			t.Errorf("missing child title chrome:\n%s", plain)
		}
	}
}

func TestRunningChildEmptyShowsLivePlaceholder(t *testing.T) {
	fs := newFakeSessions()
	// ChildStarted-only log yields no transcript cells.
	fs.put(host.Session{ID: "c-empty", ParentID: "root", Title: "general"}, mustSessionJSONL(t,
		protocol.ChildStarted{
			Correlation: protocol.Correlation{SessionID: "c-empty", ParentSessionID: "root", Depth: 1},
			Agent:       "general",
			Prompt:      "soon",
		},
	))
	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "root"
	m.services.Sessions = fs
	m.children = []childActivity{{sessionID: "c-empty", agent: "general", status: "running"}}
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 40})
	_ = m.openSessionView("c-empty")
	if len(m.viewCells) != 0 {
		t.Fatalf("viewCells = %d, want 0", len(m.viewCells))
	}
	plain := ansi.Strip(viewString(m))
	if !strings.Contains(plain, "subagent running") {
		t.Errorf("want live placeholder, got:\n%s", plain)
	}
	if strings.Contains(plain, "subagent transcript empty") {
		t.Errorf("finished empty copy shown for running child:\n%s", plain)
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
	m = updateApp(t, m, tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	m = updateApp(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
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

func TestSeedFromReplaySkipsStreamDeltasInLiveTimeline(t *testing.T) {
	events := []protocol.Event{protocol.UserMessage{Text: "hi"}}
	for i := 0; i < 250; i++ {
		events = append(events, protocol.TextDelta{Text: "x"})
	}
	events = append(events, protocol.TurnCompleted{StopReason: "end_turn"})
	var m Model
	seedFromReplay(&m, events)
	if len(m.cells) < 2 {
		t.Fatalf("cells = %d, want user+assistant snapshot", len(m.cells))
	}
	if m.runTimeline == nil {
		t.Fatal("expected timeline builder")
	}
	tr := m.runTimeline.Trace()
	if len(tr.Entries) >= 250 {
		t.Fatalf("timeline entries = %d; seed applied every TextDelta into the live model", len(tr.Entries))
	}
}

func TestWelcomeSessionResumeLoadsTranscript(t *testing.T) {
	fs := newFakeSessions()
	log := mustSessionJSONL(t,
		protocol.UserMessage{Text: "prior prompt from old session"},
		protocol.TextDelta{Text: "prior reply"},
		protocol.TurnCompleted{StopReason: "end_turn"},
	)
	fs.put(host.Session{ID: "past", Title: "old work"}, log)

	fr := &fakeRoots{active: "fresh", live: []string{"fresh"}}
	m, _ := newAppTestModelHome(nil, nil)
	m.sessionID = "fresh"
	m.services.Roots = fr
	m.services.Sessions = fs
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 40})
	if !m.showHomeLayout() {
		t.Fatal("want home layout on blank welcome session")
	}

	next, cmd := m.Update(sessionResumeMsg{id: "past"})
	m = next.(Model)
	if m.sessionID != "past" {
		t.Fatalf("sessionID = %q, want past", m.sessionID)
	}
	if m.PendingResume() != "" {
		t.Fatalf("in-process open quit unexpectedly: PendingResume=%q", m.PendingResume())
	}
	if m.showHomeLayout() {
		t.Fatal("blank home still showing after resume (should be loading or transcript)")
	}
	if len(m.cells) != 0 {
		t.Fatalf("Update applied %d cells on the UI thread; want async snapshot", len(m.cells))
	}
	if cmd == nil {
		t.Fatal("expected async JSONL seed cmd")
	}
	m = applyAppCmds(t, m, cmd)
	if m.replayPending {
		t.Fatal("replay still pending after seed cmd")
	}
	if len(m.cells) < 2 {
		t.Fatalf("cells = %d, want resumed transcript (not a blank session)", len(m.cells))
	}
	if m.showHomeLayout() {
		t.Fatal("home layout after seed — continued with a blank session")
	}
	plain := ansi.Strip(viewString(m))
	if !strings.Contains(plain, "prior prompt from old session") {
		t.Fatalf("view missing resumed transcript:\n%s", plain)
	}
}

func TestReplaySeedPreservesInFlightTurn(t *testing.T) {
	m, _ := newAppTestModelHome(nil, nil)
	m.sessionID = "past"
	m.replayPending = true
	m.replayID = "past"
	m.replayGenByID = map[string]int{"past": 1}
	m.turnRunning = true
	m.cells = []cell{&userCell{text: "typed during load"}}
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 40})
	hist := &Model{
		cells: []cell{
			&userCell{text: "old prompt"},
			&assistantCell{text: "old reply", complete: true},
		},
		toolByID: map[string]*toolCell{},
	}
	cmd := m.applyReplaySeed(replaySeedMsg{id: "past", gen: 1, tmp: hist})
	_ = cmd
	if !m.turnRunning {
		t.Fatal("seed wiped in-flight turnRunning")
	}
	if len(m.cells) < 3 {
		t.Fatalf("cells = %d, want history prefix + live suffix", len(m.cells))
	}
	if u, ok := m.cells[len(m.cells)-1].(*userCell); !ok || u.text != "typed during load" {
		t.Fatalf("live suffix lost: %#v", m.cells[len(m.cells)-1])
	}
	if u, ok := m.cells[0].(*userCell); !ok || u.text != "old prompt" {
		t.Fatalf("history prefix missing: %#v", m.cells[0])
	}
	p := m.roots["past"]
	if p == nil {
		t.Fatal("active pane not stashed after seed")
	}
	if len(p.cells) != len(m.cells) {
		t.Fatalf("pane cells = %d, live cells = %d (seed concatenated onto stash)", len(p.cells), len(m.cells))
	}
}

func TestReplaySeedDropsStaleGeneration(t *testing.T) {
	m, _ := newAppTestModelHome(nil, nil)
	m.sessionID = "past"
	m.replayPending = true
	m.replayID = "past"
	m.replayGenByID = map[string]int{"past": 2}
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 40})
	newer := &Model{
		cells: []cell{
			&userCell{text: "prompt"},
			&assistantCell{text: "reply", complete: true},
		},
		toolByID: map[string]*toolCell{},
	}
	older := &Model{
		cells: []cell{
			&userCell{text: "prompt"},
			&assistantCell{text: "reply", complete: true},
		},
		toolByID: map[string]*toolCell{},
	}
	_ = m.applyReplaySeed(replaySeedMsg{id: "past", gen: 2, tmp: newer})
	if n := len(m.cells); n != 2 {
		t.Fatalf("after gen=2: cells = %d, want 2", n)
	}
	_ = m.applyReplaySeed(replaySeedMsg{id: "past", gen: 1, tmp: older})
	if n := len(m.cells); n != 2 {
		t.Fatalf("stale gen=1 grew cells to %d", n)
	}
}

func TestReplayPendingDoesNotStickAfterSpawn(t *testing.T) {
	fs := newFakeSessions()
	log := mustSessionJSONL(t, protocol.UserMessage{Text: "old"})
	fs.put(host.Session{ID: "past", Title: "old"}, log)
	fr := &fakeRoots{active: "fresh", live: []string{"fresh"}}
	m, _ := newAppTestModelHome(nil, nil)
	m.sessionID = "fresh"
	m.services.Roots = fr
	m.services.Sessions = fs
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 40})

	next, cmd := m.Update(sessionResumeMsg{id: "past"})
	m = next.(Model)
	if !m.replayLoading() {
		t.Fatal("want loading chrome on past session")
	}
	// Spawn a new root before the seed returns.
	spawnCmd := m.spawnRoot()
	_ = spawnCmd
	if m.sessionID == "past" {
		t.Fatal("spawn did not switch away from past")
	}
	if m.replayLoading() {
		t.Fatal("new session stuck on loading session…")
	}
	if m.showHomeLayout() && m.replayPending && m.replayID == m.sessionID {
		t.Fatal("home/loading chrome leaked onto spawned root")
	}
	// Historical seed still applies to the stashed pane.
	m = applyAppCmds(t, m, cmd)
	if p := m.roots["past"]; p == nil || len(p.cells) == 0 {
		t.Fatal("stashed past pane missing transcript after late seed")
	}
}

func TestSessionResumeOpenFailureFallsBackToQuit(t *testing.T) {
	fr := &fakeRoots{active: "fresh", live: []string{"fresh"}, err: errFake("open failed")}
	m, _ := newAppTestModelHome(nil, nil)
	m.sessionID = "fresh"
	m.services.Roots = fr
	next, cmd := m.Update(sessionResumeMsg{id: "past"})
	nm := next.(Model)
	if nm.PendingResume() != "past" {
		t.Fatalf("PendingResume = %q, want process-restart fallback", nm.PendingResume())
	}
	if cmd == nil {
		t.Fatal("expected tea.Quit")
	}
}

func applyAppCmds(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	for _, msg := range runAllAppCmds(t, cmd) {
		updated, next := m.Update(msg)
		m = updated.(Model)
		if next != nil {
			m = applyAppCmds(t, m, next)
		}
	}
	return m
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
