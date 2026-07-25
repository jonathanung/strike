package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

func TestQuestionModalOpensOnQuestionAsked(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.applyEvent(protocol.TurnStarted{})
	m.applyEvent(protocol.QuestionAsked{
		RequestID: "q-open",
		Questions: []protocol.QuestionPrompt{
			{ID: "1", Question: "Open me?", Options: []protocol.QuestionOption{
				{Label: "A"}, {Label: "B"},
			}},
		},
	})
	modal, ok := m.modal.(*questionModal)
	if !ok || modal == nil {
		t.Fatalf("modal = %T, want questionModal", m.modal)
	}
	if modal.req.RequestID != "q-open" {
		t.Errorf("requestId = %q", modal.req.RequestID)
	}
	view := strings.ToLower(modal.view(60, theme.Default()))
	if !strings.Contains(view, "open me") {
		t.Errorf("view missing question:\n%s", view)
	}
}

func TestQuestionModalOptionSelectSendsReply(t *testing.T) {
	req := protocol.QuestionAsked{
		RequestID: "q-opt",
		Questions: []protocol.QuestionPrompt{{
			Question: "Pick?",
			Options: []protocol.QuestionOption{
				{Label: "Alpha", Description: "a"},
				{Label: "Beta", Description: "b"},
			},
		}},
	}
	m, ops := newTestQuestionModalFrom(req)
	next, cmd := m.update(questionKey("down"))
	if next == nil {
		t.Fatal("down closed modal")
	}
	next, cmd = next.(*questionModal).update(questionKey("enter"))
	if next != nil {
		t.Fatal("enter should close modal after final answer")
	}
	reply := receiveQuestionReply(t, ops, cmd)
	if reply.RequestID != "q-opt" {
		t.Errorf("requestId = %q", reply.RequestID)
	}
	if len(reply.Answers) != 1 || reply.Answers[0] != "Beta" {
		t.Errorf("answers = %#v, want [Beta]", reply.Answers)
	}
}

func TestQuestionModalMultiStep(t *testing.T) {
	req := protocol.QuestionAsked{
		RequestID: "q-multi",
		Questions: []protocol.QuestionPrompt{
			{
				Question: "First?",
				Options:  []protocol.QuestionOption{{Label: "One"}, {Label: "Two"}},
			},
			{
				Question: "Second?",
				Options:  []protocol.QuestionOption{{Label: "X"}, {Label: "Y"}},
			},
		},
	}
	m, ops := newTestQuestionModalFrom(req)
	// Select first option on q1 via "1"
	next, cmd := m.update(questionKey("1"))
	if next == nil {
		t.Fatal("first answer closed modal early")
	}
	runQuestionCmd(t, cmd)
	assertNoAppOp(t, ops)
	qm := next.(*questionModal)
	if qm.index != 1 {
		t.Fatalf("index = %d, want 1", qm.index)
	}
	// Select second option on q2
	next, cmd = qm.update(questionKey("2"))
	if next != nil {
		t.Fatal("expected modal closed after last answer")
	}
	reply := receiveQuestionReply(t, ops, cmd)
	if len(reply.Answers) != 2 || reply.Answers[0] != "One" || reply.Answers[1] != "Y" {
		t.Errorf("answers = %#v, want [One Y]", reply.Answers)
	}
}

func TestQuestionModalFreeform(t *testing.T) {
	req := protocol.QuestionAsked{
		RequestID: "q-free",
		Questions: []protocol.QuestionPrompt{{
			Question: "Type something?",
		}},
	}
	m, ops := newTestQuestionModalFrom(req)
	if !m.isFreeform() {
		t.Fatal("expected freeform")
	}
	// Type via textinput update path.
	next, cmd := m.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")})
	if next == nil {
		t.Fatal("typing closed modal")
	}
	runQuestionCmd(t, cmd)
	next, cmd = next.(*questionModal).update(questionKey("enter"))
	if next != nil {
		t.Fatal("enter should submit freeform")
	}
	reply := receiveQuestionReply(t, ops, cmd)
	if len(reply.Answers) != 1 || reply.Answers[0] != "hello" {
		t.Errorf("answers = %#v, want [hello]", reply.Answers)
	}
}

func TestQuestionModalEscEmptyAnswers(t *testing.T) {
	req := protocol.QuestionAsked{
		RequestID: "q-esc",
		Questions: []protocol.QuestionPrompt{{
			Question: "Dismiss?",
			Options:  []protocol.QuestionOption{{Label: "A"}, {Label: "B"}},
		}},
	}
	m, ops := newTestQuestionModalFrom(req)
	next, cmd := m.update(questionKey("esc"))
	if next != nil {
		t.Fatal("esc should close modal")
	}
	reply := receiveQuestionReply(t, ops, cmd)
	if reply.RequestID != "q-esc" {
		t.Errorf("requestId = %q", reply.RequestID)
	}
	if reply.Answers != nil && len(reply.Answers) != 0 {
		t.Errorf("answers = %#v, want empty", reply.Answers)
	}
}

func TestQuestionResolvedClosesModal(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.applyEvent(protocol.QuestionAsked{
		RequestID: "q-close",
		Questions: []protocol.QuestionPrompt{{Question: "hi?"}},
	})
	if _, ok := m.modal.(*questionModal); !ok {
		t.Fatal("modal not open")
	}
	m.applyEvent(protocol.QuestionResolved{RequestID: "q-close"})
	if m.modal != nil {
		t.Error("QuestionResolved should clear matching modal")
	}
}

func TestChildQuestionAskedOpensModal(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.applyEvent(protocol.TurnStarted{
		Correlation: protocol.Correlation{SessionID: "parent", TurnID: "t1"},
	})
	childCorr := protocol.Correlation{
		SessionID:       "child-1",
		ParentSessionID: "parent",
		Depth:           1,
	}
	m.applyEvent(protocol.QuestionAsked{
		Correlation: childCorr,
		RequestID:   "q_child_1",
		Questions:   []protocol.QuestionPrompt{{Question: "From child?"}},
	})
	modal, ok := m.modal.(*questionModal)
	if !ok || modal == nil {
		t.Fatalf("modal = %T, want questionModal", m.modal)
	}
	if modal.req.RequestID != "q_child_1" {
		t.Errorf("requestId = %q", modal.req.RequestID)
	}
	m.applyEvent(protocol.QuestionResolved{
		Correlation: childCorr,
		RequestID:   "q_child_1",
	})
	if m.modal != nil {
		t.Error("child QuestionResolved should clear modal")
	}
}

func newTestQuestionModalFrom(req protocol.QuestionAsked) (*questionModal, chan protocol.Op) {
	ops := make(chan protocol.Op, 4)
	return newQuestionModal(req, ops), ops
}

func questionKey(key string) tea.KeyMsg {
	switch key {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	}
}

func runQuestionCmd(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	if cmd == nil {
		return nil
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		return msg
	case <-time.After(appCmdTimeout):
		t.Fatalf("tea command did not complete within %s", appCmdTimeout)
		return nil
	}
}

func receiveQuestionReply(t *testing.T, ops <-chan protocol.Op, cmd tea.Cmd) protocol.QuestionReply {
	t.Helper()
	runQuestionCmd(t, cmd)
	select {
	case op := <-ops:
		reply, ok := op.(protocol.QuestionReply)
		if !ok {
			t.Fatalf("op = %T, want QuestionReply", op)
		}
		assertNoAppOp(t, ops)
		return reply
	default:
		t.Fatal("question command emitted no reply")
		return protocol.QuestionReply{}
	}
}
