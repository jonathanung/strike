package tui

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/frontend/host"
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
	// Drain optimistically marks busy so esc can interrupt the next turn
	// before TurnStarted arrives.
	if !m.turnRunning {
		t.Fatal("turnRunning false after drain dispatch; want optimistic busy")
	}
	if len(m.inputQueue) != 2 {
		t.Fatalf("after first drain queue len = %d, want 2", len(m.inputQueue))
	}
	assertUserInputText(t, receiveAppOp(t, ops), "first queued")

	_ = m.applyEvent(protocol.TurnStarted{})
	m = runEvent(t, m, protocol.TurnCompleted{StopReason: "end_turn"})
	assertUserInputText(t, receiveAppOp(t, ops), "second queued")

	_ = m.applyEvent(protocol.TurnStarted{})
	m = runEvent(t, m, protocol.TurnCompleted{StopReason: "end_turn"})
	assertUserInputText(t, receiveAppOp(t, ops), "third queued")
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
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	for _, msg := range runAllAppCmds(t, cmd) {
		m = updateApp(t, m, msg)
	}
	assertNoAppOp(t, ops)
	if len(m.inputQueue) != 1 {
		t.Fatalf("queue len = %d", len(m.inputQueue))
	}

	updated, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
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
	assertUserInputText(t, receiveAppOp(t, ops), "after interrupt")
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

	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
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
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
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
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
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
	plain := ansi.Strip(viewString(m))
	if !strings.Contains(plain, "QUEUED 1") {
		t.Fatalf("view missing queue badge:\n%s", plain)
	}
}

// updateQueue drains tea cmds so modal mutations (replace/run-next/edit) apply,
// including nested cmds produced while handling those msgs (interrupt/drain).
func updateQueue(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	updated, cmd := m.Update(msg)
	m = updated.(Model)
	return drainQueueCmds(t, m, cmd)
}

func drainQueueCmds(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	for _, out := range runAllAppCmds(t, cmd) {
		if out == nil {
			continue
		}
		updated, next := m.Update(out)
		m = updated.(Model)
		m = drainQueueCmds(t, m, next)
	}
	return m
}

func TestInputQueueOpenModalAndMutations(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	m.providerName = "echo"
	m.turnRunning = true
	m.inputQueue = []queuedInput{
		{modelText: "a", displayPrompt: "a"},
		{modelText: "b", displayPrompt: "b"},
		{modelText: "c", displayPrompt: "c"},
	}

	// /queue focuses the right pane; open overlay browser with "m".
	next, _ := m.handleCommand("/queue")
	m = next.(Model)
	if m.windows.active().id() != queueWindowID {
		t.Fatalf("active = %q, want queue pane", m.windows.active().id())
	}
	if m.focus != focusRight {
		t.Fatalf("focus = %v, want right", m.focus)
	}
	handled, _ := m.handleQueuePaneKeys(tea.KeyPressMsg{Text: "m"})
	if !handled {
		t.Fatal("m should open modal from queue pane")
	}
	qm, ok := m.modal.(*queueModal)
	if !ok {
		t.Fatalf("modal = %T, want *queueModal", m.modal)
	}
	if len(qm.items) != 3 {
		t.Fatalf("modal items = %#v", qm.items)
	}

	// Promote c to front via modal keys.
	m = updateQueue(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = updateQueue(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = updateQueue(t, m, tea.KeyPressMsg{Text: "p"})
	if got := queueLabels(m.inputQueue); got != "c|a|b" {
		t.Fatalf("after promote queue = %s", got)
	}

	// Delete head.
	m = updateQueue(t, m, tea.KeyPressMsg{Text: "d"})
	if got := queueLabels(m.inputQueue); got != "a|b" {
		t.Fatalf("after delete queue = %s", got)
	}

	// Edit head text in place.
	m = updateQueue(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	qm = m.modal.(*queueModal)
	qm.input.SetValue("alpha")
	m = updateQueue(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.inputQueue[0].modelText != "alpha" {
		t.Fatalf("after edit = %#v", m.inputQueue[0])
	}

	// e → composer
	m = updateQueue(t, m, tea.KeyPressMsg{Text: "e"})
	if m.modal != nil {
		t.Fatalf("modal still open: %T", m.modal)
	}
	if m.composer.Value() != "alpha" {
		t.Fatalf("composer = %q", m.composer.Value())
	}
	if got := queueLabels(m.inputQueue); got != "b" {
		t.Fatalf("queue after e = %s", got)
	}
	assertNoAppOp(t, ops)
}

func TestInputQueueRunNextInterruptsThenDrains(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	m.providerName = "echo"
	m.turnRunning = true
	m.inputQueue = []queuedInput{
		{modelText: "next-one", displayPrompt: "next-one"},
		{modelText: "later", displayPrompt: "later"},
	}
	next, _ := m.handleCommand("/queue")
	m = next.(Model)
	// Run-next from the focused queue pane (no modal required).
	handled, cmd := m.handleQueuePaneKeys(tea.KeyPressMsg{Text: "x"})
	if !handled {
		t.Fatal("x not handled on queue pane")
	}
	if cmd != nil {
		runAppCmd(t, cmd)
	}
	if got := receiveAppOp(t, ops); got != (protocol.Interrupt{}) {
		t.Fatalf("op = %#v, want Interrupt", got)
	}
	if got := queueLabels(m.inputQueue); got != "next-one|later" {
		t.Fatalf("queue should survive interrupt: %s", got)
	}

	m = runEvent(t, m, protocol.TurnCompleted{StopReason: "interrupted"})
	assertUserInputText(t, receiveAppOp(t, ops), "next-one")
	if got := queueLabels(m.inputQueue); got != "later" {
		t.Fatalf("after drain = %s", got)
	}
}

func TestInputQueueRunNextDrainsWhenIdle(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	m.providerName = "echo"
	m.turnRunning = false
	m.inputQueue = []queuedInput{
		{modelText: "go", displayPrompt: "go"},
	}
	next, _ := m.handleCommand("/queue")
	m = next.(Model)
	handled, cmd := m.handleQueuePaneKeys(tea.KeyPressMsg{Text: "x"})
	if !handled {
		t.Fatal("x not handled on queue pane")
	}
	if cmd != nil {
		runAppCmd(t, cmd)
	}
	assertUserInputText(t, receiveAppOp(t, ops), "go")
	if len(m.inputQueue) != 0 {
		t.Fatalf("queue = %#v", m.inputQueue)
	}
	if !m.turnRunning {
		t.Fatal("want optimistic busy after drain")
	}
}

func TestInputQueueNoticeMentionsSlashQueue(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.inputQueue = []queuedInput{{modelText: "hi", displayPrompt: "hi"}}
	m.setInputQueueNotice()
	if !strings.Contains(m.notice, "/queue") {
		t.Fatalf("notice = %q, want /queue hint", m.notice)
	}
}

func queueLabels(items []queuedInput) string {
	parts := make([]string, len(items))
	for i, q := range items {
		parts[i] = q.modelText
	}
	return strings.Join(parts, "|")
}

func TestInputQueueSkillEnqueuePreservesDisplayAndModelText(t *testing.T) {
	skill := fakeSkill("review", "", "Rendered: $ARGUMENTS")
	store := newFakeHistory()
	m, ops := newAppTestModelWithHistory(nil, []host.Skill{skill}, store)
	m.providerName = "echo"
	m.turnRunning = true
	m.composer.SetValue("/review exact")
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
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
