package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/jonathanung/strike-cli/internal/frontend/tui/ui"
	"github.com/jonathanung/strike-cli/internal/protocol"
)

// maxInputQueue caps prompts buffered while a turn runs. On overflow the
// draft stays in the composer and the user sees an error notice.
const maxInputQueue = 32

// queuedInput is one user prompt waiting for the active turn to finish.
// modelText/images is what the engine receives; displayPrompt is history text.
type queuedInput struct {
	modelText     string
	images        []protocol.ImageAttachment
	displayPrompt string
}

// enqueueUserInput buffers a prompt while turnRunning. Clears the composer on
// success; keeps the draft when the queue is full or text is empty.
func (m Model) enqueueUserInput(op protocol.UserInput, displayPrompt string) (tea.Model, tea.Cmd) {
	if strings.TrimSpace(op.Text) == "" && len(op.Images) == 0 {
		return m, nil
	}
	if len(m.inputQueue) >= maxInputQueue {
		m.setNotice(fmt.Sprintf("input queue full (%d) — wait or edit/clear queued prompts", maxInputQueue), true)
		return m, nil
	}
	m.inputQueue = append(m.inputQueue, queuedInput{
		modelText:     op.Text,
		images:        append([]protocol.ImageAttachment(nil), op.Images...),
		displayPrompt: displayPrompt,
	})
	m.resetComposer()
	m.setInputQueueNotice()
	m.refreshOpenQueueModal()
	if m.services.History == nil {
		return m, nil
	}
	done := m.services.History.Enqueue(displayPrompt)
	return m, func() tea.Msg {
		err := <-done
		return historyAddedMsg{err: err}
	}
}

// dispatchUserInput sends UserInput to the engine and optionally records history.
// Caller must ensure the turn is idle.
func (m Model) dispatchUserInput(op protocol.UserInput, displayPrompt string) (tea.Model, tea.Cmd) {
	m.resetComposer()
	m.clearNotice()
	// Optimistic busy so esc can interrupt during the gap before TurnStarted
	// arrives from the engine (ops queue + event round-trip).
	m.turnRunning = true
	ops := m.ops
	send := func() tea.Msg {
		ops <- op
		return nil
	}
	if m.services.History == nil {
		return m, send
	}
	done := m.services.History.Enqueue(displayPrompt)
	persist := func() tea.Msg {
		err := <-done
		return historyAddedMsg{err: err}
	}
	return m, tea.Batch(send, persist)
}

// tryDrainInputQueue pops the FIFO head and returns a cmd that submits it.
// History was already recorded at enqueue time. No-op while a turn is running
// or the queue is empty. Skips drain when no provider is selected.
func (m *Model) tryDrainInputQueue() tea.Cmd {
	if m.turnRunning || len(m.inputQueue) == 0 {
		return nil
	}
	if m.providerName == "" {
		m.setInputQueueNotice()
		return nil
	}
	if m.viewingChild() {
		return nil
	}
	item := m.inputQueue[0]
	m.inputQueue = m.inputQueue[1:]
	if len(m.inputQueue) == 0 {
		m.inputQueue = nil
		m.clearNotice()
	} else {
		m.setInputQueueNotice()
	}
	m.refreshOpenQueueModal()
	// Match dispatchUserInput: busy immediately so esc works before TurnStarted.
	// Spinner arms on TurnStarted (spinTickCmd) so submit stays a single send
	// cmd for callers that execute it directly (#481).
	m.turnRunning = true
	ops := m.ops
	op := protocol.UserInput{Text: item.modelText, Images: item.images}
	return func() tea.Msg {
		ops <- op
		return nil
	}
}

// setInputQueueNotice refreshes the notice row with queue depth and next chip.
func (m *Model) setInputQueueNotice() {
	n := len(m.inputQueue)
	if n == 0 {
		return
	}
	th := m.th.Resolve()
	next := strings.Join(strings.Fields(m.inputQueue[0].displayPrompt), " ")
	if cut := truncateRunes(next, 48); cut != next {
		next = cut + th.Icons.Ellipsis
	}
	msg := dotJoin(th,
		fmt.Sprintf("queued (%d)", n),
		"next: "+next,
		"/queue manage",
		"bksp edits last",
	)
	m.setNotice(msg, false)
}

// openInputQueueModal opens the queue browser overlay.
func (m *Model) openInputQueueModal() {
	m.modal = newQueueModal(m.inputQueue, m.th)
}

// refreshOpenQueueModal keeps a visible queue browser in sync after enqueue/drain.
func (m *Model) refreshOpenQueueModal() {
	if qm, ok := m.modal.(*queueModal); ok {
		qm.syncFrom(m.inputQueue)
	}
}

// applyInputQueueReplace sets the live queue from a modal mutation and refreshes chrome.
func (m *Model) applyInputQueueReplace(items []queuedInput) {
	m.inputQueue = cloneQueuedInputs(items)
	if len(m.inputQueue) == 0 {
		m.inputQueue = nil
		m.clearNotice()
	} else {
		m.setInputQueueNotice()
	}
	m.refreshOpenQueueModal()
}

// interruptToNextQueued interrupts a running turn so the FIFO head drains on
// TurnCompleted, or drains immediately when idle. No-op when the queue is empty.
func (m *Model) interruptToNextQueued() tea.Cmd {
	if len(m.inputQueue) == 0 {
		m.setNotice("queue empty — nothing to run next", true)
		return nil
	}
	if m.turnRunning {
		m.setNotice("interrupting to run next…", false)
		m.reflow()
		return m.sendInterruptCmd()
	}
	return m.tryDrainInputQueue()
}

// applyInputQueueEditComposer drops the selected item from the queue and loads
// its display text into the composer (same text-only restore as pop-last).
func (m *Model) applyInputQueueEditComposer(remaining []queuedInput, text string) {
	m.applyInputQueueReplace(remaining)
	m.setComposerValueAt(text, len([]rune(text)))
	m.recomputeCompletion()
	m.reflow()
}

// popInputQueueToComposer moves the last queued item into the composer for
// edit/cancel. Returns true when a pop occurred.
func (m *Model) popInputQueueToComposer() bool {
	n := len(m.inputQueue)
	if n == 0 {
		return false
	}
	item := m.inputQueue[n-1]
	m.inputQueue = m.inputQueue[:n-1]
	if len(m.inputQueue) == 0 {
		m.inputQueue = nil
		m.clearNotice()
	} else {
		m.setInputQueueNotice()
	}
	m.refreshOpenQueueModal()
	// Prefer display form so the user edits what they typed (incl. skills).
	text := item.displayPrompt
	if text == "" {
		text = item.modelText
	}
	m.setComposerValueAt(text, len([]rune(text)))
	m.recomputeCompletion()
	m.reflow()
	return true
}

// clearInputQueue drops all buffered prompts. Returns true when anything was cleared.
func (m *Model) clearInputQueue() bool {
	if len(m.inputQueue) == 0 {
		return false
	}
	m.inputQueue = nil
	m.setNotice("cleared input queue", false)
	m.refreshOpenQueueModal()
	return true
}

// inputQueueBadge is a compact chip for the composer title when prompts wait.
func (m Model) inputQueueBadge() string {
	n := len(m.inputQueue)
	if n == 0 {
		return ""
	}
	return headerKicker(m.th, ui.ToneAccentAlt, fmt.Sprintf("queued %d", n))
}
