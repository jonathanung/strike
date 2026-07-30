package tui

import (
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

// isEscape reports whether msg is Escape (KeyEsc or the "esc" string form).
// Modals and dismiss paths should use this instead of comparing String() alone
// so CSI-u normalized bare ESC and classic KeyEsc both match.
func isEscape(msg tea.KeyPressMsg) bool {
	return msg.Code == tea.KeyEsc || msg.String() == "esc"
}

// matchesInterrupt reports whether msg is the interrupt chord. When the bind
// still includes esc (default), also accept isEscape so CSI-u / KeyEsc forms
// match even if key.Matches string tables diverge.
func (m Model) matchesInterrupt(msg tea.KeyPressMsg) bool {
	if key.Matches(msg, m.keyMap.Interrupt) {
		return true
	}
	if !isEscape(msg) {
		return false
	}
	for _, k := range m.keyMap.Interrupt.Keys() {
		if k == "esc" {
			return true
		}
	}
	return false
}

// handleInterruptKey cancels an in-flight turn or clears a leftover input
// queue when idle. handled is false when the key should fall through (idle,
// empty queue — e.g. child-view navParent on esc).
func (m *Model) handleInterruptKey() (handled bool, cmd tea.Cmd) {
	if m.textSel.active() {
		m.textSel.clear()
	}
	if m.leaderArmed {
		m.clearLeader()
	}
	if m.turnRunning {
		m.setNotice("interrupting…", false)
		m.reflow()
		return true, m.sendInterruptCmd()
	}
	// Idle: esc clears a leftover input queue (rare once auto-drain runs).
	if m.clearInputQueue() {
		m.reflow()
		return true, nil
	}
	return false, nil
}

// sendInterruptCmd enqueues protocol.Interrupt without blocking the Bubble Tea
// command forever when the ops buffer is full.
func (m Model) sendInterruptCmd() tea.Cmd {
	// Prefer Roots so multi-root uses the non-blocking engine path.
	if m.services.Roots != nil {
		return m.interruptRoot("")
	}
	ops := m.ops
	return func() tea.Msg {
		if ops == nil {
			return nil
		}
		select {
		case ops <- protocol.Interrupt{}:
		default:
			// Drop rather than hang the TUI; user can press esc again.
		}
		return nil
	}
}
