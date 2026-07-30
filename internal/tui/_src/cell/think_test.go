package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

func TestThinkCommandBareToggleAndAliases(t *testing.T) {
	tests := []struct {
		name    string
		initial bool
		command string
		want    bool
	}{
		{name: "bare toggles on", command: "/think", want: true},
		{name: "bare toggles off", initial: true, command: "/think"},
		{name: "on", command: "/think on", want: true},
		{name: "true", command: "/think true", want: true},
		{name: "one", command: "/think 1", want: true},
		{name: "yes", command: "/think yes", want: true},
		{name: "off", initial: true, command: "/think off"},
		{name: "false", initial: true, command: "/think false"},
		{name: "zero", initial: true, command: "/think 0"},
		{name: "no", initial: true, command: "/think no"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, ops := newAppTestModel(nil, nil)
			m.showThinking = tt.initial
			m.composer.SetValue(tt.command)
			updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			m = updated.(Model)
			if msg := runAppCmd(t, cmd); msg != nil {
				t.Errorf("unexpected message %#v", msg)
			}
			assertNoAppOp(t, ops)
			if m.showThinking != tt.want {
				t.Errorf("showThinking = %v, want %v", m.showThinking, tt.want)
			}
			if m.composer.Value() != "" {
				t.Errorf("composer = %q, want reset", m.composer.Value())
			}
		})
	}
}

func TestThinkCommandRejectsInvalidUsage(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	m.composer.SetValue("/think maybe")
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	if msg := runAppCmd(t, cmd); msg != nil {
		t.Errorf("invalid /think returned message %#v", msg)
	}
	assertNoAppOp(t, ops)
	if !m.noticeErr || m.notice != "usage: /think [on|off]" {
		t.Errorf("notice = %q, error = %v", m.notice, m.noticeErr)
	}
	if m.showThinking {
		t.Error("invalid /think enabled thinking")
	}
}

func TestReasoningDeltaHiddenByDefaultAndShownWhenToggled(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.applyEvent(protocol.UserMessage{Text: "hi"})
	m.applyEvent(protocol.TurnStarted{})
	m.applyEvent(protocol.ReasoningDelta{Text: "secret-cot-chain"})
	m.applyEvent(protocol.TextDelta{Text: "final-answer-body"})
	m.applyEvent(protocol.TurnCompleted{})
	m.refreshViewport()

	hidden := ansi.Strip(m.viewport.View())
	if strings.Contains(hidden, "secret-cot-chain") {
		t.Errorf("reasoning visible with toggle off:\n%s", hidden)
	}
	if !strings.Contains(hidden, "final-answer-body") {
		t.Errorf("answer missing with toggle off:\n%s", hidden)
	}

	m.composer.SetValue("/think on")
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	runAppCmd(t, cmd)
	if !m.showThinking {
		t.Fatal("showThinking still false after /think on")
	}
	shown := ansi.Strip(m.viewport.View())
	if !strings.Contains(shown, "secret-cot-chain") {
		t.Errorf("reasoning missing with toggle on:\n%s", shown)
	}
	if !strings.Contains(shown, "thinking") {
		t.Errorf("thinking label missing with toggle on:\n%s", shown)
	}
	if !strings.Contains(shown, "final-answer-body") {
		t.Errorf("answer missing with toggle on:\n%s", shown)
	}
	header := ansi.Strip(m.headerView(100))
	if !strings.Contains(header, ui.Badge(m.th, ui.ToneMuted, "think")) {
		t.Errorf("header does not render think as muted badge:\n%s", header)
	}
}

func TestReasoningDeltaStreamsIntoOneCell(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.showThinking = true
	m.applyEvent(protocol.ReasoningDelta{Text: "step one "})
	m.applyEvent(protocol.ReasoningDelta{Text: "step two"})
	if len(m.cells) != 1 {
		t.Fatalf("cells = %d, want 1 reasoning cell", len(m.cells))
	}
	rc, ok := m.cells[0].(*reasoningCell)
	if !ok {
		t.Fatalf("cell type = %T, want *reasoningCell", m.cells[0])
	}
	if rc.text != "step one step two" {
		t.Errorf("reasoning text = %q", rc.text)
	}
}

func TestReasoningCellDistinctFromAssistant(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.showThinking = true
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.applyEvent(protocol.ReasoningDelta{Text: "cot-only"})
	m.applyEvent(protocol.TextDelta{Text: "answer-only"})
	if len(m.cells) != 2 {
		t.Fatalf("cells = %d, want reasoning + assistant", len(m.cells))
	}
	if _, ok := m.cells[0].(*reasoningCell); !ok {
		t.Fatalf("cell[0] = %T, want *reasoningCell", m.cells[0])
	}
	if _, ok := m.cells[1].(*assistantCell); !ok {
		t.Fatalf("cell[1] = %T, want *assistantCell", m.cells[1])
	}
	out := (&reasoningCell{text: "cot-only"}).render(60, m.th)
	if !strings.Contains(ansi.Strip(out), "thinking") {
		t.Errorf("reasoning cell missing thinking label:\n%s", out)
	}
	if strings.Contains(ansi.Strip(out), "strike") {
		t.Errorf("reasoning cell must not use assistant label:\n%s", out)
	}
}

func TestThinkingPlaceholderWhileTurnRunningWithNoText(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.applyEvent(protocol.UserMessage{Text: "go"})
	m.applyEvent(protocol.TurnStarted{})
	m.turnStartedAt = time.Now().Add(-4 * time.Second)
	m.refreshViewport()

	body := ansi.Strip(m.viewport.View())
	if !strings.Contains(body, "thinking") {
		t.Errorf("live thinking chrome missing while turn runs with no text:\n%s", body)
	}
	header := ansi.Strip(m.headerView(100))
	if !strings.Contains(header, "working") {
		t.Errorf("header working status missing:\n%s", header)
	}

	m.applyEvent(protocol.TextDelta{Text: "here"})
	m.refreshViewport()
	// Transcript placeholder should clear once answer text arrives.
	after := ansi.Strip(m.viewport.View())
	if !strings.Contains(after, "here") {
		t.Errorf("answer missing after TextDelta:\n%s", after)
	}
}

func TestCellsFromEventsRebuildsReasoning(t *testing.T) {
	cells, _ := cellsFromEvents([]protocol.Event{
		protocol.UserMessage{Text: "q"},
		protocol.ReasoningDelta{Text: "r1"},
		protocol.ReasoningDelta{Text: "r2"},
		protocol.TextDelta{Text: "a"},
		protocol.TurnCompleted{},
	})
	if len(cells) != 3 {
		t.Fatalf("cells = %d, want user+reasoning+assistant", len(cells))
	}
	rc, ok := cells[1].(*reasoningCell)
	if !ok || rc.text != "r1r2" {
		t.Fatalf("reasoning cell = %#v", cells[1])
	}
}

func TestChildReasoningDeltaIgnored(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.applyEvent(protocol.ReasoningDelta{
		Correlation: protocol.Correlation{ParentSessionID: "parent", Depth: 1},
		Text:        "child-cot",
	})
	if len(m.cells) != 0 {
		t.Fatalf("child ReasoningDelta leaked cells: %#v", m.cells)
	}
}
