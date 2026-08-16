package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/frontend/tui/theme"
	"github.com/jonathanung/strike-cli/internal/protocol"
)

func TestEffortModalUsesCustomDetailSeparatorWithoutChangingSemanticText(t *testing.T) {
	th := theme.Default()
	th.Icons.DetailSeparator = "|"
	m := newEffortModal(protocol.EffortMedium, make(chan protocol.Op, 1), &fakeSettings{})
	plain := ansi.Strip(m.view(60, th))
	if !strings.Contains(plain, "| "+protocol.EffortMedium.Describe()) {
		t.Errorf("effort detail omitted custom separator: %q", plain)
	}
	for _, want := range []string{"Reasoning effort", "medium", protocol.EffortMedium.Describe()} {
		if !strings.Contains(plain, want) {
			t.Errorf("custom theme changed semantic text %q: %q", want, plain)
		}
	}
}

func TestEffortCommandWithArgumentSendsSetEffort(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	m.providerName = "echo"
	m.composer.SetValue("/effort xhigh")

	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	runAppCmd(t, cmd)

	op := receiveAppOp(t, ops)
	want := protocol.SetEffort{Level: protocol.EffortXHigh}
	if op != want {
		t.Errorf("operation = %#v, want %#v", op, want)
	}
	if m.composer.Value() != "" {
		t.Error("effort command did not reset composer")
	}
}

// TestEffortCommandAcceptsEveryLevel guards against the dispatch and the
// protocol ladder drifting apart.
func TestEffortCommandAcceptsEveryLevel(t *testing.T) {
	for _, level := range protocol.Efforts() {
		t.Run(string(level), func(t *testing.T) {
			m, ops := newAppTestModel(nil, nil)
			m.composer.SetValue("/effort " + string(level))
			updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			m = updated.(Model)
			runAppCmd(t, cmd)
			if op := receiveAppOp(t, ops); op != (protocol.SetEffort{Level: level}) {
				t.Errorf("operation = %#v, want SetEffort{%q}", op, level)
			}
			if m.noticeErr {
				t.Errorf("level %q produced an error notice: %s", level, m.notice)
			}
		})
	}
}

func TestEffortCommandRejectsUnknownLevelWithoutSendingAnOp(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	m.composer.SetValue("/effort turbo")

	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	runAppCmd(t, cmd)

	select {
	case op := <-ops:
		t.Fatalf("unknown effort sent %#v, want nothing", op)
	default:
	}
	if !m.noticeErr {
		t.Fatal("unknown effort produced no error notice")
	}
	for _, want := range []string{"turbo", "off", "max"} {
		if !strings.Contains(m.notice, want) {
			t.Errorf("notice = %q, want it to mention %q", m.notice, want)
		}
	}
}

// TestBareEffortOpensPickerOnTheActiveLevel keeps enter from silently
// switching to something the user did not point at.
func TestBareEffortOpensPickerOnTheActiveLevel(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.effort = protocol.EffortMedium
	m.composer.SetValue("/effort")

	m = updateApp(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	picker, ok := m.modal.(*effortModal)
	if !ok {
		t.Fatalf("modal = %T, want *effortModal", m.modal)
	}
	if picker.choices[picker.cursor].Effort != protocol.EffortMedium {
		t.Errorf("cursor on %q, want the active level medium", picker.choices[picker.cursor].Effort)
	}
	if m.composer.Value() != "" {
		t.Error("bare effort command did not reset composer")
	}
}

// TestBareEffortWithNoLevelSetOpensAtTheFirstRung: with nothing selected the
// picker must still be usable rather than land out of range.
func TestBareEffortWithNoLevelSetOpensAtTheFirstRung(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.composer.SetValue("/effort")
	m = updateApp(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	picker := m.modal.(*effortModal)
	if picker.cursor != 0 {
		t.Errorf("cursor = %d, want 0", picker.cursor)
	}
}

func TestEffortPickerEnterSendsSelectionAndClosesModal(t *testing.T) {
	ops := make(chan protocol.Op, 4)
	picker := newEffortModal(protocol.EffortDefault, ops, &fakeSettings{})

	// Move to "medium" (off, low, medium).
	for i := 0; i < 2; i++ {
		picker.update(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	next, cmd := picker.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if next != nil {
		t.Errorf("enter left modal open as %T, want closed", next)
	}
	runAppCmd(t, cmd)
	if op := receiveAppOp(t, ops); op != (protocol.SetEffort{Level: protocol.EffortMedium}) {
		t.Errorf("operation = %#v, want SetEffort{medium}", op)
	}
}

func TestEffortPickerCursorStaysInRange(t *testing.T) {
	picker := newEffortModal(protocol.EffortOff, make(chan protocol.Op, 1), &fakeSettings{})
	for i := 0; i < 20; i++ {
		picker.update(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	if picker.cursor != len(picker.choices)-1 {
		t.Errorf("cursor = %d, want %d", picker.cursor, len(picker.choices)-1)
	}
	for i := 0; i < 20; i++ {
		picker.update(tea.KeyPressMsg{Code: tea.KeyUp})
	}
	if picker.cursor != 0 {
		t.Errorf("cursor = %d after scrolling up, want 0", picker.cursor)
	}
}

func TestEffortPickerCtrlDSavesOnlyTheEffortDefault(t *testing.T) {
	settings := &fakeSettings{}
	picker := newEffortModal(protocol.EffortDefault, make(chan protocol.Op, 1), settings)
	picker.update(tea.KeyPressMsg{Code: tea.KeyDown}) // -> low

	next, cmd := picker.update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	if next == nil {
		t.Error("ctrl+d closed the picker, want it to stay open")
	}
	runAppCmd(t, cmd)

	if len(settings.saved) != 1 {
		t.Fatalf("saved defaults = %d, want 1", len(settings.saved))
	}
	got := settings.saved[0]
	want := savedDefaults{effort: "low"}
	if got != want {
		t.Errorf("saved = %#v, want %#v (provider/model/agent untouched)", got, want)
	}
}

func TestEffortPickerEscClosesWithoutSelecting(t *testing.T) {
	ops := make(chan protocol.Op, 1)
	picker := newEffortModal(protocol.EffortHigh, ops, &fakeSettings{})
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

// TestEffortPickerListsEveryLevelWithItsDescription: the picker is the main
// place a user learns what the rungs mean.
func TestEffortPickerRendersEveryLevelWithItsDescription(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	picker := newEffortModal(protocol.EffortHigh, make(chan protocol.Op, 1), &fakeSettings{})
	out := picker.view(72, m.th)

	for _, level := range protocol.Efforts() {
		if !strings.Contains(out, string(level)) {
			t.Errorf("picker omits level %q", level)
		}
		if !strings.Contains(out, level.Describe()) {
			t.Errorf("picker omits description for %q", level)
		}
	}
}

func TestEffortSelectedEventUpdatesModelAndHeaderBadge(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.width, m.height, m.ready = 100, 30, true
	m.providerName, m.modelName = "anthropic", "claude-opus-5"

	m.applyEvent(protocol.EffortSelected{Level: protocol.EffortXHigh})

	if m.effort != protocol.EffortXHigh {
		t.Errorf("model effort = %q, want xhigh", m.effort)
	}
	if !strings.Contains(m.headerView(100), "effort xhigh") {
		t.Errorf("header omits the effort badge:\n%s", m.headerView(100))
	}
}

// TestHeaderOmitsEffortBadgeWhenUnset keeps the status bar quiet for users who
// never touch the dial.
func TestHeaderOmitsEffortBadgeWhenUnset(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.width, m.height, m.ready = 100, 30, true
	m.providerName, m.modelName = "anthropic", "claude-opus-5"

	if strings.Contains(m.headerView(100), "effort") {
		t.Errorf("header shows an effort badge with no level set:\n%s", m.headerView(100))
	}
}

// TestSaveDefaultsIncludesTheActiveEffort: the global ctrl+d shortcut should
// persist the whole current selection, effort included.
func TestSaveDefaultsIncludesTheActiveEffort(t *testing.T) {
	m, _ := newAppTestModel([]string{"build"}, nil)
	settings := m.services.Settings.(*fakeSettings)
	m.providerName, m.modelName, m.agentName = "anthropic", "claude-opus-5", "build"
	m.effort = protocol.EffortMax

	_, cmd := m.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	runAppCmd(t, cmd)

	if len(settings.saved) != 1 {
		t.Fatalf("ctrl+d saved %d defaults, want 1", len(settings.saved))
	}
	want := savedDefaults{provider: "anthropic", model: "claude-opus-5", agent: "build", effort: "max", mode: "default"}
	if settings.saved[0] != want {
		t.Errorf("saved = %#v, want %#v", settings.saved[0], want)
	}
}

func TestHelpListsTheEffortCommand(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.composer.SetValue("/help")
	m = updateApp(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	help, ok := m.modal.(*helpModal)
	if !ok {
		t.Fatalf("/help modal = %T, want helpModal", m.modal)
	}
	found := false
	for _, entry := range help.entries {
		if strings.HasPrefix(entry.Label, "/effort") {
			found = true
			break
		}
	}
	if !found {
		t.Error("help catalog omits /effort")
	}
}
