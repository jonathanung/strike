package tui

import (
	"testing"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

func assistantCellsOf(cells []cell) []*assistantCell {
	var out []*assistantCell
	for _, c := range cells {
		if a, ok := c.(*assistantCell); ok {
			out = append(out, a)
		}
	}
	return out
}

func TestTurnCompletedMarksAssistantComplete(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.applyEvent(protocol.TurnStarted{})
	m.applyEvent(protocol.TextDelta{Text: "# Hello\n\n**bold**"})

	assts := assistantCellsOf(m.cells)
	if len(assts) != 1 {
		t.Fatalf("after TextDelta: %d assistant cells, want 1", len(assts))
	}
	if assts[0].complete {
		t.Fatal("assistant complete=true while streaming, want false")
	}

	m.applyEvent(protocol.TurnCompleted{StopReason: "end_turn"})
	assts = assistantCellsOf(m.cells)
	if len(assts) != 1 {
		t.Fatalf("after TurnCompleted: %d assistant cells, want 1", len(assts))
	}
	if !assts[0].complete {
		t.Fatal("assistant complete=false after TurnCompleted, want true")
	}
	if assts[0].mdCacheOK {
		t.Fatal("mdCacheOK still true after completeAssistantCells, want false")
	}
}

func TestToolCallBeginMarksTrailingAssistantNewTextIncomplete(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.applyEvent(protocol.TurnStarted{})
	m.applyEvent(protocol.TextDelta{Text: "before tool"})

	assts := assistantCellsOf(m.cells)
	if len(assts) != 1 {
		t.Fatalf("after first TextDelta: %d assistants, want 1", len(assts))
	}
	a := assts[0]
	if a.complete {
		t.Fatal("assistant A complete while streaming, want false")
	}

	m.applyEvent(protocol.ToolCallBegin{CallID: "c1", Name: "bash"})
	if !a.complete {
		t.Fatal("assistant A complete=false after ToolCallBegin, want true")
	}
	if a.mdCacheOK {
		t.Fatal("assistant A mdCacheOK still true after ToolCallBegin, want false")
	}

	m.applyEvent(protocol.TextDelta{Text: "after tool"})
	assts = assistantCellsOf(m.cells)
	if len(assts) != 2 {
		t.Fatalf("after second TextDelta: %d assistants, want 2", len(assts))
	}
	b := assts[1]
	if b.complete {
		t.Fatal("assistant B complete while streaming, want false")
	}
	if !a.complete {
		t.Fatal("assistant A should stay complete after later TextDelta")
	}

	m.applyEvent(protocol.TurnCompleted{StopReason: "end_turn"})
	assts = assistantCellsOf(m.cells)
	if len(assts) != 2 {
		t.Fatalf("after TurnCompleted: %d assistants, want 2", len(assts))
	}
	for i, ac := range assts {
		if !ac.complete {
			t.Errorf("assistant %d complete=false after TurnCompleted, want true", i)
		}
	}
}

func TestEngineErrorDoesNotMarkAssistantComplete(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.applyEvent(protocol.TurnStarted{})
	if !m.turnRunning {
		t.Fatal("turnRunning false after TurnStarted")
	}
	m.applyEvent(protocol.TextDelta{Text: "streaming reply"})

	assts := assistantCellsOf(m.cells)
	if len(assts) != 1 {
		t.Fatalf("after TextDelta: %d assistants, want 1", len(assts))
	}
	if assts[0].complete {
		t.Fatal("assistant complete while streaming, want false")
	}

	m.applyEvent(protocol.EngineError{Message: "provider boom"})
	assts = assistantCellsOf(m.cells)
	if len(assts) != 1 {
		t.Fatalf("after EngineError: %d assistants, want 1", len(assts))
	}
	if assts[0].complete {
		t.Fatal("EngineError marked assistant complete, want still incomplete")
	}
	// Mid-turn error is appended as an error cell; assistant stays open.
	var sawError bool
	for _, c := range m.cells {
		if _, ok := c.(*errorCell); ok {
			sawError = true
			break
		}
	}
	if !sawError {
		t.Fatal("expected error cell after mid-turn EngineError")
	}

	m.applyEvent(protocol.TurnCompleted{StopReason: "error"})
	assts = assistantCellsOf(m.cells)
	if len(assts) != 1 {
		t.Fatalf("after TurnCompleted: %d assistants, want 1", len(assts))
	}
	if !assts[0].complete {
		t.Fatal("assistant complete=false after TurnCompleted, want true")
	}
}
