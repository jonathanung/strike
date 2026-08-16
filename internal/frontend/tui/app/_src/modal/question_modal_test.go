package tui

import (
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/frontend/tui/theme"
	"github.com/jonathanung/strike-cli/internal/protocol"
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
	// Soft chrome verticals break Fields-join across wrapped lines; strip outline.
	for _, g := range []string{
		th.BorderStyle.TopLeft, th.BorderStyle.TopRight,
		th.BorderStyle.BottomLeft, th.BorderStyle.BottomRight,
		th.BorderStyle.Horizontal, th.BorderStyle.Vertical,
		"╭", "╮", "╰", "╯", "┌", "┐", "└", "┘", "│", "─",
	} {
		if g != "" {
			plain = strings.ReplaceAll(plain, g, " ")
		}
	}
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
	// Single answer → confirmation screen first.
	qm := expectQuestionModal(t, next, "after option select")
	if qm.phase != questionPhaseConfirm {
		t.Fatalf("phase = %v, want confirm", qm.phase)
	}
	runQuestionCmd(t, cmd)
	assertNoAppOp(t, ops)

	next, cmd = qm.update(questionKey("enter"))
	if next != nil {
		t.Fatal("confirm enter should close modal")
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
	// Select second option on q2 → confirm
	next, cmd = qm.update(questionKey("2"))
	qm = expectQuestionModal(t, next, "after last answer")
	if qm.phase != questionPhaseConfirm {
		t.Fatalf("phase = %v, want confirm", qm.phase)
	}
	runQuestionCmd(t, cmd)
	assertNoAppOp(t, ops)

	view := strings.ToLower(ansi.Strip(qm.view(60, theme.Default())))
	if !strings.Contains(view, "confirm") {
		t.Errorf("confirm view missing title:\n%s", view)
	}
	// Match answer tokens as whole words so "your" does not satisfy "y".
	if !strings.Contains(view, "one") || !regexp.MustCompile(`\by\b`).MatchString(view) {
		t.Errorf("confirm view missing answers:\n%s", view)
	}

	next, cmd = qm.update(questionKey("enter"))
	if next != nil {
		t.Fatal("expected modal closed after confirm")
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

	next, cmd = qm.update(tea.KeyPressMsg{Text: "sprinkles"})
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
	qm = expectQuestionModal(t, next, "after last mixed answer")
	if qm.phase != questionPhaseConfirm {
		t.Fatalf("phase = %v, want confirm", qm.phase)
	}
	runQuestionCmd(t, cmd)
	assertNoAppOp(t, ops)

	next, cmd = qm.update(questionKey("enter"))
	if next != nil {
		t.Fatal("expected modal closed after confirm")
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
	next, cmd := m.update(tea.KeyPressMsg{Text: "hello"})
	if next == nil {
		t.Fatal("typing closed modal")
	}
	runQuestionCmd(t, cmd)
	next, cmd = next.(*questionModal).update(questionKey("enter"))
	qm := expectQuestionModal(t, next, "after freeform enter")
	if qm.phase != questionPhaseConfirm {
		t.Fatalf("phase = %v, want confirm", qm.phase)
	}
	runQuestionCmd(t, cmd)
	assertNoAppOp(t, ops)

	next, cmd = qm.update(questionKey("enter"))
	if next != nil {
		t.Fatal("confirm should submit freeform")
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

func TestQuestionModalHopBetweenQuestions(t *testing.T) {
	req := protocol.QuestionAsked{
		RequestID: "q-hop",
		Questions: []protocol.QuestionPrompt{
			{Question: "First?", Options: []protocol.QuestionOption{{Label: "A"}, {Label: "B"}}},
			{Question: "Second?", Options: []protocol.QuestionOption{{Label: "X"}, {Label: "Y"}}},
			{Question: "Third?", Options: []protocol.QuestionOption{{Label: "1"}, {Label: "2"}}},
		},
	}
	m, ops := newTestQuestionModalFrom(req)

	next, cmd := m.update(questionKey("1")) // A
	qm := expectQuestionModal(t, next, "after q1")
	runQuestionCmd(t, cmd)
	if qm.index != 1 {
		t.Fatalf("index = %d, want 1", qm.index)
	}

	// Hop back to first question.
	next, cmd = qm.update(questionKey("shift+tab"))
	qm = expectQuestionModal(t, next, "back to q1")
	runQuestionCmd(t, cmd)
	if qm.index != 0 {
		t.Fatalf("index = %d, want 0 after back", qm.index)
	}
	view := strings.ToLower(ansi.Strip(qm.view(60, theme.Default())))
	if !strings.Contains(view, "first?") {
		t.Errorf("expected first question after back:\n%s", view)
	}

	// Change answer on q1, then advance with right after fill.
	next, cmd = qm.update(questionKey("2")) // B — advances to next unfilled (q2)
	qm = expectQuestionModal(t, next, "after re-answer q1")
	runQuestionCmd(t, cmd)
	if qm.index != 1 {
		t.Fatalf("index = %d, want 1", qm.index)
	}

	next, cmd = qm.update(questionKey("1")) // X
	qm = expectQuestionModal(t, next, "after q2")
	runQuestionCmd(t, cmd)
	if qm.index != 2 {
		t.Fatalf("index = %d, want 2", qm.index)
	}

	// left hops back
	next, cmd = qm.update(questionKey("left"))
	qm = expectQuestionModal(t, next, "left to q2")
	runQuestionCmd(t, cmd)
	if qm.index != 1 {
		t.Fatalf("index = %d, want 1 after left", qm.index)
	}

	// right hops forward when filled
	next, cmd = qm.update(questionKey("right"))
	qm = expectQuestionModal(t, next, "right to q3")
	runQuestionCmd(t, cmd)
	if qm.index != 2 {
		t.Fatalf("index = %d, want 2 after right", qm.index)
	}

	next, cmd = qm.update(questionKey("2")) // 2
	qm = expectQuestionModal(t, next, "confirm after all")
	runQuestionCmd(t, cmd)
	assertNoAppOp(t, ops)
	if qm.phase != questionPhaseConfirm {
		t.Fatalf("phase = %v, want confirm", qm.phase)
	}
	if got := qm.answers; len(got) != 3 || got[0] != "B" || got[1] != "X" || got[2] != "2" {
		t.Errorf("answers = %#v, want [B X 2]", got)
	}

	// From confirm, left returns to last question for edit.
	next, cmd = qm.update(questionKey("left"))
	qm = expectQuestionModal(t, next, "edit from confirm")
	runQuestionCmd(t, cmd)
	if qm.phase != questionPhaseAnswer || qm.index != 2 {
		t.Fatalf("phase=%v index=%d, want answer@2", qm.phase, qm.index)
	}

	// Hop back to q1, then right must visit q2 (not skip to confirm).
	next, cmd = qm.update(questionKey("shift+tab"))
	qm = expectQuestionModal(t, next, "back to q2")
	runQuestionCmd(t, cmd)
	next, cmd = qm.update(questionKey("shift+tab"))
	qm = expectQuestionModal(t, next, "back to q1")
	runQuestionCmd(t, cmd)
	if qm.index != 0 {
		t.Fatalf("index = %d, want 0", qm.index)
	}
	next, cmd = qm.update(questionKey("right"))
	qm = expectQuestionModal(t, next, "right stays on q2 when all filled")
	runQuestionCmd(t, cmd)
	if qm.phase != questionPhaseAnswer || qm.index != 1 {
		t.Fatalf("phase=%v index=%d, want answer@1 (not confirm)", qm.phase, qm.index)
	}

	// Advance to end and submit with a changed last answer.
	next, cmd = qm.update(questionKey("right"))
	qm = expectQuestionModal(t, next, "right to q3 again")
	runQuestionCmd(t, cmd)
	next, cmd = qm.update(questionKey("1"))
	qm = expectQuestionModal(t, next, "re-confirm")
	runQuestionCmd(t, cmd)
	next, cmd = qm.update(questionKey("enter"))
	if next != nil {
		t.Fatal("submit should close")
	}
	reply := receiveQuestionReply(t, ops, cmd)
	if len(reply.Answers) != 3 || reply.Answers[0] != "B" || reply.Answers[1] != "X" || reply.Answers[2] != "1" {
		t.Errorf("answers = %#v, want [B X 1]", reply.Answers)
	}
}

func TestQuestionModalCustomAnswerOnOptions(t *testing.T) {
	req := protocol.QuestionAsked{
		RequestID: "q-custom",
		Questions: []protocol.QuestionPrompt{{
			Question: "Pick or type?",
			Options:  []protocol.QuestionOption{{Label: "Red"}, {Label: "Blue"}},
		}},
	}
	m, ops := newTestQuestionModalFrom(req)

	view := strings.ToLower(ansi.Strip(m.view(60, theme.Default())))
	if !strings.Contains(view, "write your own") {
		t.Errorf("missing custom list row:\n%s", view)
	}

	// tab enters custom mode
	next, cmd := m.update(questionKey("tab"))
	qm := expectQuestionModal(t, next, "tab custom")
	runQuestionCmd(t, cmd)
	if !qm.customMode || !qm.isFreeform() {
		t.Fatal("expected custom freeform mode")
	}
	view = strings.ToLower(ansi.Strip(qm.view(60, theme.Default())))
	if !strings.Contains(view, "tab options") {
		t.Errorf("custom mode missing tab-back hint:\n%s", view)
	}

	next, cmd = qm.update(tea.KeyPressMsg{Text: "purple"})
	qm = expectQuestionModal(t, next, "type custom")
	runQuestionCmd(t, cmd)
	next, cmd = qm.update(questionKey("enter"))
	qm = expectQuestionModal(t, next, "after custom enter")
	runQuestionCmd(t, cmd)
	assertNoAppOp(t, ops)
	if qm.phase != questionPhaseConfirm {
		t.Fatalf("phase = %v, want confirm", qm.phase)
	}
	if qm.answers[0] != "purple" {
		t.Errorf("answer = %q, want purple", qm.answers[0])
	}

	// Confirm view shows custom text.
	view = strings.ToLower(ansi.Strip(qm.view(60, theme.Default())))
	if !strings.Contains(view, "purple") {
		t.Errorf("confirm missing custom answer:\n%s", view)
	}

	next, cmd = qm.update(questionKey("enter"))
	if next != nil {
		t.Fatal("confirm should close")
	}
	reply := receiveQuestionReply(t, ops, cmd)
	if len(reply.Answers) != 1 || reply.Answers[0] != "purple" {
		t.Errorf("answers = %#v, want [purple]", reply.Answers)
	}
}

func TestQuestionModalCustomViaListRow(t *testing.T) {
	req := protocol.QuestionAsked{
		RequestID: "q-row",
		Questions: []protocol.QuestionPrompt{{
			Question: "Color?",
			Options:  []protocol.QuestionOption{{Label: "Red"}, {Label: "Blue"}},
		}},
	}
	m, ops := newTestQuestionModalFrom(req)
	// Move to synthetic row (index 2) and enter.
	next, cmd := m.update(questionKey("down"))
	qm := expectQuestionModal(t, next, "down 1")
	runQuestionCmd(t, cmd)
	next, cmd = qm.update(questionKey("down"))
	qm = expectQuestionModal(t, next, "down 2")
	runQuestionCmd(t, cmd)
	if qm.cursor != 2 {
		t.Fatalf("cursor = %d, want 2 (custom row)", qm.cursor)
	}
	next, cmd = qm.update(questionKey("enter"))
	qm = expectQuestionModal(t, next, "enter custom row")
	runQuestionCmd(t, cmd)
	if !qm.customMode {
		t.Fatal("enter on custom row should enable custom mode")
	}
	assertNoAppOp(t, ops)
}

func TestQuestionModalConfirmEscDismisses(t *testing.T) {
	req := protocol.QuestionAsked{
		RequestID: "q-confirm-esc",
		Questions: []protocol.QuestionPrompt{{
			Question: "Sure?",
			Options:  []protocol.QuestionOption{{Label: "Yes"}, {Label: "No"}},
		}},
	}
	m, ops := newTestQuestionModalFrom(req)
	next, cmd := m.update(questionKey("1"))
	qm := expectQuestionModal(t, next, "to confirm")
	runQuestionCmd(t, cmd)
	if qm.phase != questionPhaseConfirm {
		t.Fatalf("phase = %v", qm.phase)
	}
	next, cmd = qm.update(questionKey("esc"))
	if next != nil {
		t.Fatal("esc on confirm should dismiss")
	}
	reply := receiveQuestionReply(t, ops, cmd)
	if len(reply.Answers) != 0 {
		t.Errorf("answers = %#v, want empty dismiss", reply.Answers)
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

func expectQuestionModal(t *testing.T, next modal, when string) *questionModal {
	t.Helper()
	if next == nil {
		t.Fatalf("%s: modal closed unexpectedly", when)
	}
	qm, ok := next.(*questionModal)
	if !ok {
		t.Fatalf("%s: modal = %T, want questionModal", when, next)
	}
	return qm
}

func questionKey(key string) tea.KeyPressMsg {
	switch key {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEsc}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "left":
		return tea.KeyPressMsg{Code: tea.KeyLeft}
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "shift+tab":
		return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	default:
		return tea.KeyPressMsg{Text: key}
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
