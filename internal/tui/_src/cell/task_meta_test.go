package tui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

func TestTaskSpawnStaysRunningUntilChildCompleted(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	meta, _ := json.Marshal(map[string]string{
		"sessionId": "child-sess-1",
		"status":    "started",
	})
	m.applyEvent(protocol.ToolCallBegin{CallID: "t1", Name: "task", Args: json.RawMessage(`{"prompt":"explore"}`)})
	m.applyEvent(protocol.ToolCallEnd{
		CallID:   "t1",
		Title:    "task child-se",
		Output:   "Started child session child-sess-1 (agent explore). …",
		Metadata: meta,
	})
	tc, ok := m.toolByID["t1"]
	if !ok || tc == nil {
		t.Fatal("missing task tool cell")
	}
	if tc.done {
		t.Fatal("task cell done=true immediately after spawn; want running")
	}
	if tc.isError {
		t.Fatal("spawn marked error")
	}
	rendered := ansi.Strip(tc.render(80, theme.Default()))
	// In-progress: ellipsis (or no success checkmark).
	ic := theme.Default().Resolve().Icons
	if strings.Contains(rendered, ic.OK) {
		t.Fatalf("spawn render shows success glyph %q: %q", ic.OK, rendered)
	}

	m.applyEvent(protocol.ChildCompleted{
		Correlation: protocol.Correlation{SessionID: "child-sess-1", ParentSessionID: "p", Depth: 1},
		Status:      protocol.ChildStatusCompleted,
		Summary:     "found three TODOs",
	})
	if !tc.done {
		t.Fatal("task cell still running after ChildCompleted")
	}
	if tc.isError {
		t.Fatal("completed task marked error")
	}
	if tc.output != "found three TODOs" {
		t.Errorf("output = %q, want summary", tc.output)
	}
	metaAfter, ok := parseTaskMetadata(tc.metadata)
	if !ok || metaAfter.Status != "completed" {
		t.Errorf("metadata after complete = %+v", metaAfter)
	}
	rendered = ansi.Strip(tc.render(80, theme.Default()))
	if !strings.Contains(rendered, ic.OK) {
		t.Fatalf("completed render missing success glyph: %q", rendered)
	}
}

func TestTaskChildFailedShowsErrorNotSuccess(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	meta, _ := json.Marshal(map[string]string{"sessionId": "c-fail", "status": "started"})
	m.applyEvent(protocol.ToolCallBegin{CallID: "t1", Name: "task"})
	m.applyEvent(protocol.ToolCallEnd{CallID: "t1", Title: "task", Output: "Started…", Metadata: meta})
	m.applyEvent(protocol.ChildCompleted{
		Correlation: protocol.Correlation{SessionID: "c-fail"},
		Status:      protocol.ChildStatusFailed,
		Summary:     "boom",
	})
	tc := m.toolByID["t1"]
	if !tc.done || !tc.isError {
		t.Fatalf("done=%v isError=%v, want done error", tc.done, tc.isError)
	}
	ic := theme.Default().Resolve().Icons
	rendered := ansi.Strip(tc.render(60, theme.Default()))
	if strings.Contains(rendered, ic.OK) {
		t.Fatalf("failed task shows OK: %q", rendered)
	}
	if !strings.Contains(rendered, ic.Err) {
		t.Fatalf("failed task missing Err glyph: %q", rendered)
	}
}

func TestTaskChildCanceledNotSuccess(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	meta, _ := json.Marshal(map[string]string{"sessionId": "c-can", "status": "started"})
	m.applyEvent(protocol.ToolCallBegin{CallID: "t1", Name: "task"})
	m.applyEvent(protocol.ToolCallEnd{CallID: "t1", Output: "Started…", Metadata: meta})
	m.applyEvent(protocol.ChildCompleted{
		Correlation: protocol.Correlation{SessionID: "c-can"},
		Status:      protocol.ChildStatusCanceled,
		Summary:     "task canceled",
	})
	tc := m.toolByID["t1"]
	if !tc.done || !tc.isError {
		t.Fatalf("canceled: done=%v isError=%v", tc.done, tc.isError)
	}
}

func TestCellsFromEventsTaskSpawnRunningThenComplete(t *testing.T) {
	meta, _ := json.Marshal(map[string]string{"sessionId": "c1", "status": "started"})
	cells, toolByID := cellsFromEvents([]protocol.Event{
		protocol.ToolCallBegin{CallID: "t1", Name: "task"},
		protocol.ToolCallEnd{CallID: "t1", Title: "task c1", Output: "Started child session c1", Metadata: meta},
	})
	tc := toolByID["t1"]
	if tc == nil || tc.done {
		t.Fatalf("after spawn done=%v tc=%v cells=%d", tc != nil && tc.done, tc != nil, len(cells))
	}
	cells, toolByID = cellsFromEvents([]protocol.Event{
		protocol.ToolCallBegin{CallID: "t1", Name: "task"},
		protocol.ToolCallEnd{CallID: "t1", Title: "task c1", Output: "Started", Metadata: meta},
		protocol.ChildCompleted{
			Correlation: protocol.Correlation{SessionID: "c1"},
			Status:      protocol.ChildStatusCompleted,
			Summary:     "all good",
		},
	})
	tc = toolByID["t1"]
	if tc == nil || !tc.done || tc.isError {
		t.Fatalf("after complete done=%v err=%v", tc != nil && tc.done, tc != nil && tc.isError)
	}
	if tc.output != "all good" {
		t.Errorf("output = %q", tc.output)
	}
	_ = cells
}

func TestActivityAndToolCellAlignOnTaskLifecycle(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	corr := protocol.Correlation{SessionID: "act-1", ParentSessionID: "p", Depth: 1}
	meta, _ := json.Marshal(map[string]string{"sessionId": "act-1", "status": "started"})
	m.applyEvent(protocol.ToolCallBegin{CallID: "t1", Name: "task"})
	m.applyEvent(protocol.ToolCallEnd{CallID: "t1", Title: "task act-1", Output: "Started", Metadata: meta})
	m.applyEvent(protocol.ChildStarted{Correlation: corr, Agent: "explore", Prompt: "scan"})
	if m.toolByID["t1"].done {
		t.Fatal("tool done while child running")
	}
	if len(m.children) != 1 || m.children[0].status != "running" {
		t.Fatalf("children = %+v", m.children)
	}
	m.applyEvent(protocol.ChildCompleted{Correlation: corr, Status: protocol.ChildStatusCompleted, Summary: "ok"})
	if !m.toolByID["t1"].done || m.children[0].status != "completed" {
		t.Fatalf("tool done=%v child status=%q", m.toolByID["t1"].done, m.children[0].status)
	}
}

func TestSleepToolCallsCoalesceToOneRow(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	for i, id := range []string{"s1", "s2", "s3", "s4"} {
		m.applyEvent(protocol.ToolCallBegin{CallID: id, Name: "sleep", Args: json.RawMessage(`{"seconds":1}`)})
		m.applyEvent(protocol.ToolCallEnd{
			CallID: id,
			Title:  "slept 1s",
			Output: "Slept for 1 seconds",
		})
		_ = i
	}
	var sleepCells int
	for _, c := range m.cells {
		if tc, ok := c.(*toolCell); ok && tc.name == "sleep" {
			sleepCells++
		}
	}
	if sleepCells != 1 {
		t.Fatalf("sleep transcript rows = %d, want 1 (coalesced)", sleepCells)
	}
	tc := m.toolByID["s4"]
	if tc == nil || !tc.done {
		t.Fatalf("final sleep cell missing/done=%v", tc)
	}
	if tc.title != "slept 1s" {
		t.Errorf("title = %q", tc.title)
	}
}

func TestExplicitSleepAloneStillVisible(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.applyEvent(protocol.ToolCallBegin{CallID: "s1", Name: "sleep", Args: json.RawMessage(`{"seconds":2}`)})
	m.applyEvent(protocol.ToolCallEnd{CallID: "s1", Title: "slept 2s", Output: "Slept for 2 seconds"})
	var sleepCells int
	for _, c := range m.cells {
		if tc, ok := c.(*toolCell); ok && tc.name == "sleep" {
			sleepCells++
		}
	}
	if sleepCells != 1 {
		t.Fatalf("sleep rows = %d, want 1", sleepCells)
	}
}

func TestChildCompletedNoticeIsInfoNotUser(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.applyEvent(protocol.UserMessage{Text: "real user prompt"})
	m.applyEvent(protocol.UserMessage{Text: "[child.completed session=abcd1234 status=completed]\nok\nDo not sleep-poll for subagents; this is the terminal result."})
	var users, infos int
	for _, c := range m.cells {
		switch c.(type) {
		case *userCell:
			users++
		case *infoCell:
			infos++
		}
	}
	if users != 1 {
		t.Fatalf("user cells = %d, want 1", users)
	}
	if infos != 1 {
		t.Fatalf("info cells = %d, want 1 for child.completed", infos)
	}
}

func TestChildCompletedDesktopNotifyTransition(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.notifyMode = NotifyOn
	m.turnStartedAt = time.Now().Add(-time.Hour)
	cmd := m.applyEvent(protocol.ChildCompleted{
		Correlation: protocol.Correlation{SessionID: "c1"},
		Status:      protocol.ChildStatusCompleted,
		Summary:     "done",
	})
	if cmd == nil {
		t.Fatal("ChildCompleted should batch a desktop notify cmd when notify=on")
	}
}

func TestIsChildCompletedNotice(t *testing.T) {
	if !isChildCompletedNotice("[child.completed session=x status=completed]\nhi") {
		t.Fatal("expected true")
	}
	if isChildCompletedNotice("please complete the child task") {
		t.Fatal("expected false for ordinary text")
	}
}
