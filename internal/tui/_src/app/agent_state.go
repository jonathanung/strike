package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

// spinTickCmd arms the header spinner only while the agent is Working and
// chrome is animated. Idle must not tick (#481). SSH/static mode keeps a
// static working glyph with no tick chain (#497).
func (m Model) spinTickCmd() tea.Cmd {
	if m.agentState() != theme.AgentStateWorking || staticWorkingChrome() {
		return nil
	}
	return m.spin.Tick
}

// agentState derives the live runtime coloring state from protocol-backed
// fields on the model. Views must call this rather than inventing status from
// modal types or other UI-local guesses. Dead is intentionally never returned:
// no session-lifecycle signal exists yet.
func (m Model) agentState() theme.AgentState {
	if m.awaitingPermission {
		return theme.AgentStateAttention
	}
	// Queued admission is live work on a constrained pool — not idle.
	if m.turnRunning || len(m.queuePools) > 0 {
		return theme.AgentStateWorking
	}
	if m.sessionErrored {
		return theme.AgentStateError
	}
	return theme.AgentStateReady
}

// applyAgentStateEvent updates protocol-derived status fields used by
// agentState. Call from applyEvent for every engine event so coloring stays
// correlated with the event stream.
func (m *Model) applyAgentStateEvent(ev protocol.Event) {
	switch ev := ev.(type) {
	case protocol.TurnStarted:
		m.turnRunning = true
		m.sessionErrored = false
	case protocol.PermissionAsked:
		m.awaitingPermission = true
	case protocol.PermissionResolved:
		m.awaitingPermission = false
	case protocol.QuestionAsked:
		// Same attention state as permission: turn is blocked on the user.
		m.awaitingPermission = true
	case protocol.QuestionResolved:
		m.awaitingPermission = false
	case protocol.TurnCompleted:
		m.turnRunning = false
		m.awaitingPermission = false
		if ev.StopReason == "error" {
			m.sessionErrored = true
		}
	case protocol.EngineError:
		// Mid-turn failures stay Working until TurnCompleted (stopReason
		// "error") arrives. Idle-state engine errors surface as Error.
		if !m.turnRunning {
			m.sessionErrored = true
		}
	case protocol.UserMessage:
		// A new user turn clears a sticky error once the engine accepts input.
		m.sessionErrored = false
	}
}

// agentStateTone maps a runtime state onto a component Tone so badges and
// panels stay on theme tokens.
func agentStateTone(s theme.AgentState) ui.Tone {
	switch s {
	case theme.AgentStateWorking:
		return ui.ToneAccentAlt
	case theme.AgentStateAttention:
		return ui.ToneWarning
	case theme.AgentStateError:
		return ui.ToneError
	case theme.AgentStateDead:
		return ui.ToneMuted
	default:
		return ui.ToneSuccess
	}
}
