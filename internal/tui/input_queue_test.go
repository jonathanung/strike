package tui

import (
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/protocol"
)

func runEvent(t *testing.T, m Model, ev protocol.Event) Model {
	t.Helper()
	cmd := m.applyEvent(ev)
	if cmd == nil {
		return m
	}
	for _, msg := range runAllAppCmds(t, cmd) {
		if msg != nil {
			m = updateApp(t, m, msg)
		}
	}
	return m
}

func TestInputQueueDrainsFIFOOnTurnCompleted(t *testing.T) {
	m, ops := newAppTestModelWithHistory(nil, nil, newFakeHistory())
	m.providerName = "echo"
	m.turnRunning = true
	m.inputQueue = []queuedInput{
		{modelText: "first queued", displayPrompt: "first queued"},
		{modelText: "second queued", displayPrompt: "second queued"},
		{modelText: "third queued", displayPrompt: "third queued"},
	}

	m = runEvent(t, m, protocol.TurnCompleted{StopReason: "end_turn"})
	if m.turnRunning {
		t.Fatal("turnRunning still true after TurnCompleted")
	}
	if len(m.inputQueue) != 2 {
		t.Fatalf("after first drain queue len = %d, want 2", len(m.inputQueue))
	}
	if got := receiveAppOp(t, ops); got != (protocol.UserInput{Text: "first queued"}) {
		t.Fatalf("drained op = %#v, want first queued", got)
	}

	m.turnRunning = true
	_ = m.applyEvent(protocol.TurnStarted{})
	m = runEvent(t, m, protocol.TurnCompleted{StopReason: "end_turn"})
	if got := receiveAppOp(t, ops); got != (protocol.UserInput{Text: "second queued"}) {
		t.Fatalf("second drain = %#v", got)
	}

	m.turnRunning = true
	_ = m.applyEvent(protocol.TurnStarted{})
	m = runEvent(t, m, protocol.TurnCompleted{StopReason: "end_turn"})
	if got := receiveAppOp(t, ops); got != (protocol.UserInput{Text: "third queued"}) {
		t.Fatalf("third drain = %#v", got)
	}
	if len(m.inputQueue) != 0 {
		t.Fatalf("queue not empty after full drain: %#v", m.inputQueue)
	}
	assertNoAppOp(t, ops)
}

func TestInputQueueSurvivesInterruptThenDrains(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	m.providerName = "echo"
	m.turnRunning = true
	m.composer.SetValue("after interrupt")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	for _, msg := range runAllAppCmds(t, cmd) {
		m = updateApp(t, m, msg)
	}
	assertNoAppOp(t, ops)
	if len(m.inputQueue) != 1 {
		t.Fatalf("queue len = %d", len(m.inputQueue))
	}

	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(Model)
	for _, msg := range runAllAppCmds(t, cmd) {
		m = updateApp(t, m, msg)
	}
	if got := receiveAppOp(t, ops); got != (protocol.Interrupt{}) {
		t.Fatalf("interrupt op = %#v", got)
	}
	if len(m.inputQueue) != 1 {
		t.Fatalf("queue cleared on interrupt: %#v", m.inputQueue)
	}

	m = runEvent(t, m, protocol.TurnCompleted{StopReason: "interrupted"})
	if got := receiveAppOp(t, ops); got != (protocol.UserInput{Text: "after interrupt"}) {
		t.Fatalf("post-interrupt drain = %#v", got)
	}
}

func TestInputQueuePopLastToComposerAndClearWhenIdle(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	m.providerName = "echo"
	m.turnRunning = true
	m.inputQueue = []queuedInput{
		{modelText: "a", displayPrompt: "a"},
		{modelText: "b", displayPrompt: "b"},
	}
	m.composer.SetValue("")
	m.focus = focusLeft

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	m = updated.(Model)
	assertNoAppOp(t, ops)
	if got := m.composer.Value(); got != "b" {
		t.Fatalf("composer after pop = %q, want b", got)
	}
	if len(m.inputQueue) != 1 || m.inputQueue[0].modelText != "a" {
		t.Fatalf("queue after pop = %#v", m.inputQueue)
	}

	m.turnRunning = false
	m.composer.SetValue("")
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(Model)
	if len(m.inputQueue) != 0 {
		t.Fatalf("esc did not clear queue: %#v", m.inputQueue)
	}
	if !strings.Contains(m.notice, "cleared") {
		t.Fatalf("notice = %q, want cleared", m.notice)
	}
}

func TestInputQueueFullKeepsComposerDraft(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	m.providerName = "echo"
	m.turnRunning = true
	m.inputQueue = make([]queuedInput, maxInputQueue)
	for i := range m.inputQueue {
		m.inputQueue[i] = queuedInput{modelText: "x", displayPrompt: "x"}
	}
	m.composer.SetValue("overflow draft")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	for _, msg := range runAllAppCmds(t, cmd) {
		m = updateApp(t, m, msg)
	}
	assertNoAppOp(t, ops)
	if got := m.composer.Value(); got != "overflow draft" {
		t.Fatalf("composer = %q, want draft kept on full queue", got)
	}
	if len(m.inputQueue) != maxInputQueue {
		t.Fatalf("queue grew past max: %d", len(m.inputQueue))
	}
	if !m.noticeErr || !strings.Contains(m.notice, "full") {
		t.Fatalf("notice = %q, want full error", m.notice)
	}
}

func TestInputQueueBadgeRendersInComposerTitle(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.providerName = "echo"
	m.turnRunning = true
	m.inputQueue = []queuedInput{{modelText: "hello", displayPrompt: "hello"}}
	m.reflow()
	plain := ansi.Strip(m.View())
	if !strings.Contains(plain, "queued 1") {
		t.Fatalf("view missing queue badge:\n%s", plain)
	}
}

func TestInputQueueSkillEnqueuePreservesDisplayAndModelText(t *testing.T) {
	skill := fakeSkill("review", "", "Rendered: $ARGUMENTS")
	store := newFakeHistory()
	m, ops := newAppTestModelWithHistory(nil, []host.Skill{skill}, store)
	m.providerName = "echo"
	m.turnRunning = true
	m.composer.SetValue("/review exact")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	for _, msg := range runAllAppCmds(t, cmd) {
		m = updateApp(t, m, msg)
	}
	assertNoAppOp(t, ops)
	if len(m.inputQueue) != 1 {
		t.Fatalf("queue = %#v", m.inputQueue)
	}
	if m.inputQueue[0].modelText != "Rendered: exact" || m.inputQueue[0].displayPrompt != "/review exact" {
		t.Fatalf("queued item = %#v", m.inputQueue[0])
	}
	if got := store.Entries(); !slices.Equal(got, []string{"/review exact"}) {
		t.Fatalf("history = %q", got)
	}
}
