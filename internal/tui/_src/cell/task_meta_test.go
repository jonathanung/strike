package tui

import (
	"encoding/json"
	"strings"
	"testing"

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
