package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

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

func TestQuestionModalWrapsLongOptionLabels(t *testing.T) {
	const width = 40
	longOpt := "To verify that the entire system works correctly from end to end"
	req := protocol.QuestionAsked{
		RequestID: "q-wrap",
		Questions: []protocol.QuestionPrompt{{
			Question: "Which of the following is the best description of the primary purpose of a unit test?",
			Options: []protocol.QuestionOption{
				{Label: longOpt},
				{Label: "To check the performance of the application under load"},
			},
		}},
	}
	m, _ := newTestQuestionModalFrom(req)
	th := theme.Default().Resolve()
	view := m.view(width, th)
	plain := strings.ReplaceAll(ansi.Strip(view), th.Icons.FocusBar, "")
	compact := strings.Join(strings.Fields(plain), " ")
	if !strings.Contains(compact, longOpt) {
		t.Fatalf("long option truncated instead of wrapped:\n%s", plain)
	}
	if !strings.Contains(compact, "primary purpose of a unit test") {
		t.Fatalf("question text missing or clipped:\n%s", plain)
	}
	if strings.Contains(plain, "end to e…") || strings.Contains(plain, "end to e...") {
		t.Fatalf("option still ellipsis-clipped:\n%s", plain)
	}
	for i, line := range strings.Split(view, "\n") {
		if got := lipgloss.Width(line); got > width {
			t.Errorf("line %d width = %d, want <= %d: %q", i, got, width, ansi.Strip(line))
		}
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

func TestQuestionModalMultiMixedFreeformAndOptions(t *testing.T) {
	req := protocol.QuestionAsked{
		RequestID: "q-mixed",
		Questions: []protocol.QuestionPrompt{
			{
				Question: "Pick flavor?",
				Options:  []protocol.QuestionOption{{Label: "Vanilla"}, {Label: "Chocolate"}},
			},
			{Question: "Any toppings?"},
			{
				Question: "Size?",
				Options:  []protocol.QuestionOption{{Label: "S"}, {Label: "L"}},
			},
		},
	}
	m, ops := newTestQuestionModalFrom(req)

	view := strings.ToLower(m.view(60, theme.Default()))
	if !strings.Contains(view, "question 1/3") {
		t.Errorf("view missing 1/3 progress:\n%s", view)
	}

	next, cmd := m.update(questionKey("2")) // Chocolate
	if next == nil {
		t.Fatal("first answer closed modal early")
	}
	runQuestionCmd(t, cmd)
	assertNoAppOp(t, ops)
	qm := next.(*questionModal)
	if !qm.isFreeform() || qm.index != 1 {
		t.Fatalf("expected freeform step 2, index=%d freeform=%v", qm.index, qm.isFreeform())
	}
	view = strings.ToLower(qm.view(60, theme.Default()))
	if !strings.Contains(view, "question 2/3") || !strings.Contains(view, "any toppings") {
		t.Errorf("view missing step 2:\n%s", view)
	}

	next, cmd = qm.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("sprinkles")})
	if next == nil {
		t.Fatal("typing closed modal")
	}
	runQuestionCmd(t, cmd)
	next, cmd = next.(*questionModal).update(questionKey("enter"))
	if next == nil {
		t.Fatal("freeform submit closed before last question")
	}
	runQuestionCmd(t, cmd)
	assertNoAppOp(t, ops)
	qm = next.(*questionModal)
	if qm.isFreeform() || qm.index != 2 {
		t.Fatalf("expected options step 3, index=%d freeform=%v", qm.index, qm.isFreeform())
	}
	view = strings.ToLower(qm.view(60, theme.Default()))
	if !strings.Contains(view, "question 3/3") {
		t.Errorf("view missing 3/3 progress:\n%s", view)
	}

	next, cmd = qm.update(questionKey("1")) // S
	if next != nil {
		t.Fatal("expected modal closed after last answer")
	}
	reply := receiveQuestionReply(t, ops, cmd)
	if len(reply.Answers) != 3 ||
		reply.Answers[0] != "Chocolate" ||
		reply.Answers[1] != "sprinkles" ||
		reply.Answers[2] != "S" {
		t.Errorf("answers = %#v, want [Chocolate sprinkles S]", reply.Answers)
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
