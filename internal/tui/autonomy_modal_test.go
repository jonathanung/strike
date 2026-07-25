package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

func TestAutonomyModalUsesCustomDetailSeparator(t *testing.T) {
	th := theme.Default()
	th.Icons.DetailSeparator = "|"
	m := newAutonomyModal(protocol.AutonomySupervised, make(chan protocol.Op, 1))
	plain := ansi.Strip(m.view(72, th))
	if !strings.Contains(plain, "| "+protocol.AutonomySupervised.Describe()) {
		t.Errorf("autonomy detail omitted custom separator: %q", plain)
	}
	for _, want := range []string{"Autonomy", "supervised", protocol.AutonomySupervised.Describe()} {
		if !strings.Contains(plain, want) {
			t.Errorf("custom theme changed semantic text %q: %q", want, plain)
		}
	}
}

func TestAutonomyCommandWithArgumentSendsSetAutonomy(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	m.composer.SetValue("/autonomy agent")

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	runAppCmd(t, cmd)

	op := receiveAppOp(t, ops)
	want := protocol.SetAutonomy{Mode: protocol.AutonomyAgent}
	if op != want {
		t.Errorf("operation = %#v, want %#v", op, want)
	}
	if m.composer.Value() != "" {
		t.Error("autonomy command did not reset composer")
	}
}

func TestAutonomyCommandAcceptsEveryMode(t *testing.T) {
	for _, mode := range protocol.Autonomies() {
		t.Run(string(mode), func(t *testing.T) {
			m, ops := newAppTestModel(nil, nil)
			m.composer.SetValue("/autonomy " + string(mode))
			updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			m = updated.(Model)
			runAppCmd(t, cmd)
			if op := receiveAppOp(t, ops); op != (protocol.SetAutonomy{Mode: mode}) {
				t.Errorf("operation = %#v, want SetAutonomy{%q}", op, mode)
			}
			if m.noticeErr {
				t.Errorf("mode %q produced an error notice: %s", mode, m.notice)
			}
		})
	}
}

func TestAutonomyCommandRejectsUnknownModeWithoutSendingAnOp(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	m.composer.SetValue("/autonomy yolo")

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	runAppCmd(t, cmd)

	select {
	case op := <-ops:
		t.Fatalf("unknown autonomy sent %#v, want nothing", op)
	default:
	}
	if !m.noticeErr {
		t.Fatal("unknown autonomy produced no error notice")
	}
	for _, want := range []string{"yolo", "supervised", "agent", "checks"} {
		if !strings.Contains(m.notice, want) {
			t.Errorf("notice = %q, want it to mention %q", m.notice, want)
		}
	}
}

func TestBareAutonomyOpensPickerOnTheActiveMode(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.autonomy = protocol.AutonomyChecks
	m.composer.SetValue("/autonomy")

	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	picker, ok := m.modal.(*autonomyModal)
	if !ok {
		t.Fatalf("modal = %T, want *autonomyModal", m.modal)
	}
	if picker.modes[picker.cursor] != protocol.AutonomyChecks {
		t.Errorf("cursor on %q, want the active mode checks", picker.modes[picker.cursor])
	}
	if m.composer.Value() != "" {
		t.Error("bare autonomy command did not reset composer")
	}
}

func TestAutonomyPickerEnterSendsSelectionAndClosesModal(t *testing.T) {
	ops := make(chan protocol.Op, 4)
	picker := newAutonomyModal(protocol.AutonomySupervised, ops)

	// supervised, agent — move to agent.
	picker.update(tea.KeyMsg{Type: tea.KeyDown})
	next, cmd := picker.update(tea.KeyMsg{Type: tea.KeyEnter})
	if next != nil {
		t.Errorf("enter left modal open as %T, want closed", next)
	}
	runAppCmd(t, cmd)
	if op := receiveAppOp(t, ops); op != (protocol.SetAutonomy{Mode: protocol.AutonomyAgent}) {
		t.Errorf("operation = %#v, want SetAutonomy{agent}", op)
	}
}

func TestAutonomyPickerCursorStaysInRange(t *testing.T) {
	picker := newAutonomyModal(protocol.AutonomySupervised, make(chan protocol.Op, 1))
	for i := 0; i < 20; i++ {
		picker.update(tea.KeyMsg{Type: tea.KeyDown})
	}
	if picker.cursor != len(picker.modes)-1 {
		t.Errorf("cursor = %d, want %d", picker.cursor, len(picker.modes)-1)
	}
	for i := 0; i < 20; i++ {
		picker.update(tea.KeyMsg{Type: tea.KeyUp})
	}
	if picker.cursor != 0 {
		t.Errorf("cursor = %d after scrolling up, want 0", picker.cursor)
	}
}

func TestAutonomyPickerEscClosesWithoutSelecting(t *testing.T) {
	ops := make(chan protocol.Op, 1)
	picker := newAutonomyModal(protocol.AutonomyAgent, ops)
	next, cmd := picker.update(tea.KeyMsg{Type: tea.KeyEsc})
	if next != nil {
		t.Errorf("esc left modal open as %T", next)
	}
	runAppCmd(t, cmd)
	select {
	case op := <-ops:
		t.Fatalf("esc sent %#v, want nothing", op)
	default:
	}
}

func TestAutonomyPickerRendersEveryModeWithItsDescription(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	picker := newAutonomyModal(protocol.AutonomySupervised, make(chan protocol.Op, 1))
	out := picker.view(72, m.th)

	for _, mode := range protocol.Autonomies() {
		if !strings.Contains(out, string(mode)) {
			t.Errorf("picker omits mode %q", mode)
		}
		if !strings.Contains(out, mode.Describe()) {
			t.Errorf("picker omits description for %q", mode)
		}
	}
}

func TestAutonomySelectedEventUpdatesModelAndHeaderBadge(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.width, m.height, m.ready = 100, 30, true
	m.providerName, m.modelName = "anthropic", "claude-opus-5"

	m.applyEvent(protocol.AutonomySelected{Mode: protocol.AutonomyAgent})

	if m.autonomy != protocol.AutonomyAgent {
		t.Errorf("model autonomy = %q, want agent", m.autonomy)
	}
	header := ansi.Strip(m.headerView(100))
	if !strings.Contains(header, "auto agent") {
		t.Errorf("header omits the autonomy badge:\n%s", header)
	}
}

func TestHeaderAlwaysShowsAutonomyBadge(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.width, m.height, m.ready = 100, 30, true
	m.providerName, m.modelName = "anthropic", "claude-opus-5"

	header := ansi.Strip(m.headerView(100))
	if !strings.Contains(header, "auto sup") {
		t.Errorf("header omits default autonomy badge:\n%s", header)
	}
}

func TestHelpListsTheAutonomyCommand(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.composer.SetValue("/help")
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(m.notice, "/autonomy") {
		t.Errorf("help notice omits /autonomy: %q", m.notice)
	}
}
