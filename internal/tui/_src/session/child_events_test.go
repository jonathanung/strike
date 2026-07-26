package tui

import (
	"testing"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

func TestChildCorrelatedTurnEventsDoNotAffectTranscriptOrRunning(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.applyEvent(protocol.TurnStarted{
		Correlation: protocol.Correlation{SessionID: "parent", TurnID: "t1"},
	})
	if !m.turnRunning {
		t.Fatal("parent TurnStarted should set turnRunning")
	}
	cellsBefore := len(m.cells)

	// Child TextDelta / TurnCompleted must not clear running or append cells.
	childCorr := protocol.Correlation{
		SessionID:       "child-1",
		ParentSessionID: "parent",
		Depth:           1,
		TurnID:          "child-turn",
	}
	m.applyEvent(protocol.TextDelta{Correlation: childCorr, Text: "secret child text"})
	m.applyEvent(protocol.TurnCompleted{Correlation: childCorr, StopReason: "end_turn"})
	m.applyEvent(protocol.UserMessage{Correlation: childCorr, Text: "child user"})
	m.applyEvent(protocol.TurnStarted{Correlation: childCorr})
	m.applyEvent(protocol.ToolCallBegin{Correlation: childCorr, CallID: "c1", Name: "bash"})
	m.applyEvent(protocol.ToolCallEnd{Correlation: childCorr, CallID: "c1", Output: "nope"})
	m.applyEvent(protocol.EngineError{Correlation: childCorr, Message: "child boom"})

	if !m.turnRunning {
		t.Error("child TurnCompleted cleared turnRunning")
	}
	if len(m.cells) != cellsBefore {
		t.Errorf("cells grew from %d to %d; child events must not append transcript", cellsBefore, len(m.cells))
	}
	for _, c := range m.cells {
		if ac, ok := c.(*assistantCell); ok && ac.text != "" {
			t.Errorf("unexpected assistant text from child delta: %q", ac.text)
		}
	}

	// Parent TurnCompleted still works.
	m.applyEvent(protocol.TurnCompleted{
		Correlation: protocol.Correlation{SessionID: "parent", TurnID: "t1"},
	})
	if m.turnRunning {
		t.Error("parent TurnCompleted should clear turnRunning")
	}
}

func TestChildLifecycleEventsDoNotPanic(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	corr := protocol.Correlation{
		SessionID:       "child-1",
		ParentSessionID: "parent",
		Depth:           1,
	}
	// Root-emitted lifecycle (no parent filter if ParentSessionID set — filtered
	// by defense path). Also exercise zero-depth no-op cases.
	m.applyEvent(protocol.ChildStarted{Correlation: corr, Agent: "build", Prompt: "hi"})
	m.applyEvent(protocol.ChildCompleted{Correlation: corr, Status: protocol.ChildStatusCompleted, Summary: "done"})
	m.applyEvent(protocol.ChildStarted{})
	m.applyEvent(protocol.ChildCompleted{Status: protocol.ChildStatusCanceled})
	if m.modal != nil {
		t.Error("child lifecycle should not open a modal")
	}
	if m.turnRunning {
		t.Error("child lifecycle should not set turnRunning")
	}
}

func TestChildPermissionAskedStillOpensModal(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	m.applyEvent(protocol.TurnStarted{
		Correlation: protocol.Correlation{SessionID: "parent", TurnID: "t1"},
	})
	childCorr := protocol.Correlation{
		SessionID:         "child-1",
		ParentSessionID:   "parent",
		Depth:             1,
		TurnID:            "ct",
		ProviderRequestID: "pr",
	}
	m.applyEvent(protocol.PermissionAsked{
		Correlation: childCorr,
		RequestID:   "perm_child_1",
		Permission:  "bash",
		Patterns:    []string{"echo hi"},
	})
	modal, ok := m.modal.(*permissionModal)
	if !ok || modal == nil {
		t.Fatalf("modal = %T, want permissionModal", m.modal)
	}
	if modal.req.RequestID != "perm_child_1" {
		t.Errorf("requestId = %q", modal.req.RequestID)
	}
	if modal.req.Permission != "bash" {
		t.Errorf("permission = %q", modal.req.Permission)
	}
	// Resolve closes modal.
	m.applyEvent(protocol.PermissionResolved{
		Correlation: childCorr,
		RequestID:   "perm_child_1",
		Decision:    protocol.DecisionOnce,
	})
	if m.modal != nil {
		t.Error("PermissionResolved should clear matching modal")
	}
	_ = ops
}
