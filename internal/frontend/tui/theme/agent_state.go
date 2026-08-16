package theme

import "charm.land/lipgloss/v2"

// AgentState is the live runtime state used for dynamic coloring of session
// and agent chrome. Values map to semantic theme tokens — never raw colors in
// views. Dead is reserved for a future session-lifecycle state and must not be
// produced by reducers until that lifecycle exists.
type AgentState int

const (
	// AgentStateReady is idle and awaiting user input (Success / green).
	AgentStateReady AgentState = iota
	// AgentStateWorking is a turn or tool loop in flight (AccentAlt / blue).
	AgentStateWorking
	// AgentStateAttention needs the user (permission ask, gate, or other
	// blocking prompt) (Warning / yellow).
	AgentStateAttention
	// AgentStateError is a failed turn, tool, or provider (Error / red).
	AgentStateError
	// AgentStateDead is reserved for a terminal closed/cancelled/orphaned
	// session (TextMuted / grey). Not yet a runtime state.
	AgentStateDead
)

// Label is the short status-chrome word for this state.
func (s AgentState) Label() string {
	switch s {
	case AgentStateWorking:
		return "working"
	case AgentStateAttention:
		return "needs you"
	case AgentStateError:
		return "error"
	case AgentStateDead:
		return "dead"
	default:
		return "ready"
	}
}

// AgentStateColor resolves the semantic color token for a runtime state.
// Dead maps to TextMuted so the reserved state is already tokenized.
func (t Theme) AgentStateColor(s AgentState) AdaptiveColor {
	t = t.Resolve()
	switch s {
	case AgentStateWorking:
		return t.AccentAlt
	case AgentStateAttention:
		return t.Warning
	case AgentStateError:
		return t.Error
	case AgentStateDead:
		return t.TextMuted
	default:
		return t.Success
	}
}

// AgentStateStyle is the foreground style for status chrome in this state.
func (t Theme) AgentStateStyle(s AgentState) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(t.AgentStateColor(s))
}

// AgentStateStrongStyle is the bold foreground style for badges and emphasis.
func (t Theme) AgentStateStrongStyle(s AgentState) lipgloss.Style {
	return t.AgentStateStyle(s).Bold(true)
}
