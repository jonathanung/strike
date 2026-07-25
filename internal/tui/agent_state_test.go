package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

func TestAgentStateFromProtocolEvents(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)

	if got := m.agentState(); got != theme.AgentStateReady {
		t.Fatalf("initial state = %v, want ready", got)
	}

	m.applyEvent(protocol.TurnStarted{})
	if got := m.agentState(); got != theme.AgentStateWorking {
		t.Fatalf("after TurnStarted = %v, want working", got)
	}
	if !m.turnRunning {
		t.Fatal("turnRunning false after TurnStarted")
	}

	m.applyEvent(protocol.PermissionAsked{RequestID: "p1", Permission: "bash", Patterns: []string{"ls"}})
	if got := m.agentState(); got != theme.AgentStateAttention {
		t.Fatalf("after PermissionAsked = %v, want attention", got)
	}

	m.applyEvent(protocol.PermissionResolved{RequestID: "p1", Decision: protocol.DecisionOnce})
	if got := m.agentState(); got != theme.AgentStateWorking {
		t.Fatalf("after PermissionResolved = %v, want working", got)
	}

	m.applyEvent(protocol.TurnCompleted{StopReason: "end_turn"})
	if got := m.agentState(); got != theme.AgentStateReady {
		t.Fatalf("after successful TurnCompleted = %v, want ready", got)
	}
}

func TestAgentStateErrorFromFailedTurn(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)

	m.applyEvent(protocol.TurnStarted{})
	m.applyEvent(protocol.EngineError{Message: "provider boom"})
	if got := m.agentState(); got != theme.AgentStateWorking {
		t.Fatalf("mid-turn EngineError state = %v, want working until TurnCompleted", got)
	}
	if len(m.cells) != 1 {
		t.Fatalf("mid-turn error cells = %d, want 1 error cell", len(m.cells))
	}

	m.applyEvent(protocol.TurnCompleted{StopReason: "error"})
	if got := m.agentState(); got != theme.AgentStateError {
		t.Fatalf("after error TurnCompleted = %v, want error", got)
	}

	m.applyEvent(protocol.UserMessage{Text: "retry"})
	if got := m.agentState(); got != theme.AgentStateReady {
		t.Fatalf("after UserMessage clears error = %v, want ready", got)
	}
}

func TestAgentStateErrorFromIdleEngineError(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)

	m.applyEvent(protocol.EngineError{Message: "no model selected — use /provider <anthropic|openai|xai|echo> [model]"})
	if got := m.agentState(); got != theme.AgentStateError {
		t.Fatalf("idle EngineError state = %v, want error", got)
	}
	if !m.sessionErrored {
		t.Fatal("sessionErrored not set for idle EngineError")
	}

	m.applyEvent(protocol.TurnStarted{})
	if got := m.agentState(); got != theme.AgentStateWorking {
		t.Fatalf("TurnStarted after idle error = %v, want working", got)
	}
	if m.sessionErrored {
		t.Fatal("sessionErrored should clear on TurnStarted")
	}
}

func TestAgentStateDeadNeverProduced(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	events := []protocol.Event{
		protocol.UserMessage{Text: "hi"},
		protocol.TurnStarted{},
		protocol.TextDelta{Text: "x"},
		protocol.ToolCallBegin{CallID: "c1", Name: "bash"},
		protocol.ToolCallEnd{CallID: "c1", Title: "bash", IsError: true},
		protocol.PermissionAsked{RequestID: "p", Permission: "bash"},
		protocol.PermissionResolved{RequestID: "p", Decision: protocol.DecisionReject},
		protocol.EngineError{Message: "boom"},
		protocol.TurnCompleted{StopReason: "error"},
		protocol.TurnCompleted{StopReason: "interrupted"},
	}
	for _, ev := range events {
		m.applyEvent(ev)
		if got := m.agentState(); got == theme.AgentStateDead {
			t.Fatalf("agentState produced Dead after %T", ev)
		}
	}
}

func TestAgentStateToneMapping(t *testing.T) {
	cases := []struct {
		state theme.AgentState
		want  ui.Tone
	}{
		{theme.AgentStateReady, ui.ToneSuccess},
		{theme.AgentStateWorking, ui.ToneAccentAlt},
		{theme.AgentStateAttention, ui.ToneWarning},
		{theme.AgentStateError, ui.ToneError},
		{theme.AgentStateDead, ui.ToneMuted},
	}
	for _, tc := range cases {
		if got := agentStateTone(tc.state); got != tc.want {
			t.Errorf("agentStateTone(%v) = %v, want %v", tc.state, got, tc.want)
		}
	}
}

func TestHeaderRecolorsForEachLiveAgentState(t *testing.T) {
	setTUITrueColor(t)

	th := theme.Default()
	th.Success = fixedColor("#111111")
	th.AccentAlt = fixedColor("#222222")
	th.Warning = fixedColor("#333333")
	th.Error = fixedColor("#444444")
	th.TextMuted = fixedColor("#555555")
	th.Background = theme.NoBackground()

	m, _ := newAppTestModelWithOptions(Options{Theme: &th})
	m.providerName = "echo"
	m.modelName = "echo-1"
	m.agentName = "build"
	m.width, m.height, m.ready = 120, 40, true

	cases := []struct {
		name  string
		setup func(*Model)
		state theme.AgentState
		color string
		label string
	}{
		{
			name:  "ready",
			setup: func(*Model) {},
			state: theme.AgentStateReady,
			color: "#111111",
			label: "ready",
		},
		{
			name: "working",
			setup: func(m *Model) {
				m.applyEvent(protocol.TurnStarted{})
			},
			state: theme.AgentStateWorking,
			color: "#222222",
			label: "working",
		},
		{
			name: "attention",
			setup: func(m *Model) {
				m.applyEvent(protocol.TurnStarted{})
				m.applyEvent(protocol.PermissionAsked{RequestID: "h", Permission: "bash", Patterns: []string{"ls"}})
			},
			state: theme.AgentStateAttention,
			color: "#333333",
			label: "attention",
		},
		{
			name: "error",
			setup: func(m *Model) {
				m.applyEvent(protocol.TurnStarted{})
				m.applyEvent(protocol.TurnCompleted{StopReason: "error"})
			},
			state: theme.AgentStateError,
			color: "#444444",
			label: "error",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			model, _ := newAppTestModelWithOptions(Options{Theme: &th})
			model.providerName = m.providerName
			model.modelName = m.modelName
			model.agentName = m.agentName
			model.width, model.height, model.ready = m.width, m.height, m.ready
			tc.setup(&model)

			if got := model.agentState(); got != tc.state {
				t.Fatalf("agentState = %v, want %v", got, tc.state)
			}

			header := model.headerView(120)
			if !strings.Contains(ansi.Strip(header), tc.label) {
				t.Fatalf("header missing %q:\n%s", tc.label, ansi.Strip(header))
			}
			if !strings.Contains(header, rgbSGR(tc.color)) {
				t.Fatalf("header did not use state token %s:\n%q", tc.color, header)
			}
		})
	}
}

func TestHeaderAgentBadgeFollowsAgentStateTone(t *testing.T) {
	setTUITrueColor(t)

	th := theme.Default()
	th.Success = fixedColor("#0a0a0a")
	th.Background = theme.NoBackground()

	m, _ := newAppTestModelWithOptions(Options{Theme: &th})
	m.providerName = "echo"
	m.modelName = "echo-1"
	m.agentName = "build"

	header := m.headerView(100)
	if !strings.Contains(header, rgbSGR("#0a0a0a")) {
		t.Fatalf("ready agent badge did not use Success token:\n%q", header)
	}
	if !strings.Contains(ansi.Strip(header), "build") {
		t.Fatalf("header missing agent name:\n%s", ansi.Strip(header))
	}
}

func TestPermissionResolvedClearsAttentionWithoutMatchingModal(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.applyEvent(protocol.TurnStarted{})
	m.applyEvent(protocol.PermissionAsked{RequestID: "a", Permission: "bash"})
	// Unrelated resolution must still clear the protocol-backed flag when ids match elsewhere;
	// a matching resolve is the normal path.
	m.applyEvent(protocol.PermissionResolved{RequestID: "a", Decision: protocol.DecisionOnce})
	if m.awaitingPermission {
		t.Fatal("awaitingPermission still set after PermissionResolved")
	}
	if got := m.agentState(); got != theme.AgentStateWorking {
		t.Fatalf("state = %v, want working", got)
	}
}

