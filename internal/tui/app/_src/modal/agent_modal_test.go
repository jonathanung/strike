package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

func TestBareAgentOpensPickerOnTheActiveAgent(t *testing.T) {
	m, _ := newAppTestModel([]string{"build", "plan", "review"}, nil)
	m.agentName = "plan"
	m.composer.SetValue("/agent")

	m = updateApp(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	picker, ok := m.modal.(*agentModal)
	if !ok {
		t.Fatalf("modal = %T, want *agentModal", m.modal)
	}
	if picker.agents[picker.cursor] != "plan" {
		t.Errorf("cursor on %q, want the active agent plan", picker.agents[picker.cursor])
	}
	if m.composer.Value() != "" {
		t.Error("bare agent command did not reset composer")
	}
}

func TestAgentCommandWithArgumentSendsSelectAgent(t *testing.T) {
	m, ops := newAppTestModel([]string{"build", "plan"}, nil)
	m.composer.SetValue("/agent plan")

	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	runAppCmd(t, cmd)

	op := receiveAppOp(t, ops)
	want := protocol.SelectAgent{Name: "plan"}
	if op != want {
		t.Errorf("operation = %#v, want %#v", op, want)
	}
	if m.composer.Value() != "" {
		t.Error("agent command did not reset composer")
	}
	if m.modal != nil {
		t.Errorf("named /agent left modal open as %T", m.modal)
	}
}

func TestAgentPickerEnterSendsSelectionAndClosesModal(t *testing.T) {
	ops := make(chan protocol.Op, 4)
	picker := newAgentModal("build", []string{"build", "plan"}, ops, nil)

	picker.update(tea.KeyPressMsg{Code: tea.KeyDown})
	next, cmd := picker.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if next != nil {
		t.Errorf("enter left modal open as %T, want closed", next)
	}
	runAppCmd(t, cmd)
	if op := receiveAppOp(t, ops); op != (protocol.SelectAgent{Name: "plan"}) {
		t.Errorf("operation = %#v, want SelectAgent{plan}", op)
	}
}

func TestAgentPickerCursorStaysInRange(t *testing.T) {
	picker := newAgentModal("build", []string{"build", "plan", "review"}, make(chan protocol.Op, 1), nil)
	for i := 0; i < 20; i++ {
		picker.update(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	if picker.cursor != len(picker.agents)-1 {
		t.Errorf("cursor = %d, want %d", picker.cursor, len(picker.agents)-1)
	}
	for i := 0; i < 20; i++ {
		picker.update(tea.KeyPressMsg{Code: tea.KeyUp})
	}
	if picker.cursor != 0 {
		t.Errorf("cursor = %d after scrolling up, want 0", picker.cursor)
	}
}

func TestAgentPickerEscClosesWithoutSelecting(t *testing.T) {
	ops := make(chan protocol.Op, 1)
	picker := newAgentModal("plan", []string{"build", "plan"}, ops, nil)
	next, cmd := picker.update(tea.KeyPressMsg{Code: tea.KeyEsc})
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

func TestAgentPickerOmitsInvalidNames(t *testing.T) {
	picker := newAgentModal("build", []string{"build", "evil\x1bagent", "plan", "  padded  "}, make(chan protocol.Op, 1), nil)
	if got, want := picker.agents, []string{"build", "plan"}; len(got) != len(want) {
		t.Fatalf("agents = %#v, want %#v", got, want)
	} else {
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("agents = %#v, want %#v", got, want)
			}
		}
	}
}

func TestAgentPickerRendersAgents(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	picker := newAgentModal("build", []string{"build", "plan", "code reviewer"}, make(chan protocol.Op, 1), nil)
	out := ansi.Strip(picker.view(72, m.th))

	for _, name := range []string{"Select agent", "build", "plan", "code reviewer"} {
		if !strings.Contains(out, name) {
			t.Errorf("picker omits %q:\n%s", name, out)
		}
	}
}

func TestAgentPickerEmptyList(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	picker := newAgentModal("", nil, make(chan protocol.Op, 1), nil)
	out := ansi.Strip(picker.view(72, m.th))
	if !strings.Contains(out, "no agents configured") {
		t.Errorf("empty picker missing empty copy:\n%s", out)
	}
	next, cmd := picker.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if next != picker {
		t.Errorf("enter on empty list closed modal as %T", next)
	}
	runAppCmd(t, cmd)
}

func TestAgentPickerCtrlDSavesOnlyTheAgentDefault(t *testing.T) {
	settings := &fakeSettings{}
	picker := newAgentModal("build", []string{"build", "plan"}, make(chan protocol.Op, 1), settings)
	picker.update(tea.KeyPressMsg{Code: tea.KeyDown})

	next, cmd := picker.update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	if next == nil {
		t.Error("ctrl+d closed the picker, want it to stay open")
	}
	runAppCmd(t, cmd)

	if len(settings.saved) != 1 {
		t.Fatalf("saved defaults = %d, want 1", len(settings.saved))
	}
	got := settings.saved[0]
	want := savedDefaults{agent: "plan"}
	if got != want {
		t.Errorf("saved = %#v, want %#v (provider/model/effort untouched)", got, want)
	}
}

func TestAgentModalUsesCustomDotInHint(t *testing.T) {
	th := theme.Default()
	th.Icons.Dot = "|"
	picker := newAgentModal("build", []string{"build", "plan"}, make(chan protocol.Op, 1), nil)
	plain := ansi.Strip(picker.view(72, th))
	if !strings.Contains(plain, "| enter select") {
		t.Errorf("hint omitted custom dot separator: %q", plain)
	}
	for _, want := range []string{"Select agent", "build", "plan"} {
		if !strings.Contains(plain, want) {
			t.Errorf("custom theme changed semantic text %q: %q", want, plain)
		}
	}
}
