package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

func TestSchedulerPresetsModalCatalogAndToggle(t *testing.T) {
	cat := newFakeSchedulerPresets()
	cat.global.Presets = []string{"cargo"}
	cat.global.Limits = map[string]int{"process": 4}
	cat.global.Commands = []host.SchedulerCommandRule{{Pattern: "go test *", Class: "test"}}

	sm := newSchedulerPresetsModal(cat, theme.Default())
	if len(sm.items) != 3 {
		t.Fatalf("items=%d", len(sm.items))
	}
	if !sm.selected["cargo"] {
		t.Fatal("baseline cargo should be pre-selected")
	}
	if sm.selected["cmake"] {
		t.Fatal("cmake should start unchecked")
	}
	if len(sm.customCommands) != 1 || sm.customLimits["process"] != 4 {
		t.Fatalf("custom state: limits=%v cmds=%+v", sm.customLimits, sm.customCommands)
	}

	// Toggle cmake on (cursor 0).
	sm.cursor = 0
	next, _ := sm.update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	sm = next.(*schedulerPresetsModal)
	if !sm.selected["cmake"] {
		t.Fatal("space should toggle cmake on")
	}
	// Toggle cargo off.
	for i, p := range sm.items {
		if p.ID == "cargo" {
			sm.cursor = i
			break
		}
	}
	next, _ = sm.update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	sm = next.(*schedulerPresetsModal)
	if sm.selected["cargo"] {
		t.Fatal("space should toggle cargo off")
	}

	ids := sm.selectedIDs()
	if len(ids) != 1 || ids[0] != "cmake" {
		t.Fatalf("selectedIDs=%v", ids)
	}
}

func TestSchedulerPresetsModalCancelDoesNotWrite(t *testing.T) {
	cat := newFakeSchedulerPresets()
	cat.global.Presets = []string{"npm"}
	sm := newSchedulerPresetsModal(cat, theme.Default())
	sm.cursor = 0
	sm.toggle() // mutate selection
	next, cmd := sm.update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if next != nil {
		t.Fatalf("esc should close, got %T", next)
	}
	if cmd != nil {
		t.Fatal("esc must not schedule apply")
	}
	if len(cat.applied) != 0 {
		t.Fatalf("cancel wrote: %v", cat.applied)
	}
	// Global unchanged.
	st, _ := cat.Global()
	if len(st.Presets) != 1 || st.Presets[0] != "npm" {
		t.Fatalf("global mutated on cancel: %v", st.Presets)
	}
}

func TestSchedulerPresetsModalApplyWrites(t *testing.T) {
	cat := newFakeSchedulerPresets()
	cat.global.Commands = []host.SchedulerCommandRule{{Pattern: "keep *", Class: "general"}}
	sm := newSchedulerPresetsModal(cat, theme.Default())
	// Select cargo + npm.
	for i, p := range sm.items {
		if p.ID == "cargo" || p.ID == "npm" {
			sm.cursor = i
			sm.toggle()
		}
	}
	next, cmd := sm.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if next == nil {
		t.Fatal("apply keeps modal until result")
	}
	if cmd == nil {
		t.Fatal("apply should schedule write")
	}
	msg := cmd()
	applied, ok := msg.(schedulerPresetsAppliedMsg)
	if !ok {
		t.Fatalf("msg=%T", msg)
	}
	if applied.err != "" {
		t.Fatalf("err=%s", applied.err)
	}
	if len(applied.ids) != 2 {
		t.Fatalf("ids=%v", applied.ids)
	}
	if len(cat.applied) != 1 {
		t.Fatalf("applied calls=%d", len(cat.applied))
	}
	// Custom commands still present (fake only tracks presets; real host preserves).
	if len(cat.global.Commands) != 1 {
		t.Fatalf("custom commands lost in fake: %+v", cat.global.Commands)
	}
}

func TestSchedulerPresetsModalApplyBlockedWhenLoadFails(t *testing.T) {
	cat := newFakeSchedulerPresets()
	cat.globalErr = errors.New("read failed")
	cat.global.Presets = []string{"cargo"} // on disk, but Global() errors
	sm := newSchedulerPresetsModal(cat, theme.Default())
	if sm.loadErr == "" {
		t.Fatal("want loadErr")
	}
	next, cmd := sm.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	sm = next.(*schedulerPresetsModal)
	if cmd != nil {
		t.Fatal("apply must not schedule write when load failed")
	}
	if !strings.Contains(sm.flash, "failed to load") {
		t.Fatalf("flash=%q", sm.flash)
	}
	if len(cat.applied) != 0 {
		t.Fatalf("wrote despite load error: %v", cat.applied)
	}
}

func TestSchedulerPresetsModalApplyErrorKeepsModal(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.width, m.height, m.ready = 100, 40, true
	cat := newFakeSchedulerPresets()
	cat.applyErr = errors.New("disk full")
	m.services.SchedulerPresets = cat

	// Park FTUE and open presets.
	next, _ := m.handleCommand("/ftue")
	m = next.(Model)
	fm := m.modal.(*ftueModal)
	fm.cursor = int(ftueStepScheduler)
	m = updateAppDrain(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	sm, ok := m.modal.(*schedulerPresetsModal)
	if !ok {
		t.Fatalf("modal=%T", m.modal)
	}
	sm.cursor = 0
	sm.toggle()
	m = updateAppDrain(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	sm, ok = m.modal.(*schedulerPresetsModal)
	if !ok {
		t.Fatalf("after error modal=%T, want picker still open", m.modal)
	}
	if !strings.Contains(sm.flash, "disk full") {
		t.Fatalf("flash=%q", sm.flash)
	}
	if len(m.modalQueue) != 1 {
		t.Fatalf("wizard should stay parked: queue=%d", len(m.modalQueue))
	}
}

func TestSchedulerPresetsModalPreviewAndView(t *testing.T) {
	cat := newFakeSchedulerPresets()
	sm := newSchedulerPresetsModal(cat, theme.Default())
	sm.selected["cmake"] = true
	sm.selected["cargo"] = true
	th := theme.Default().Resolve()
	view := ansi.Strip(sm.view(72, th))
	if !strings.Contains(view, "CMake") || !strings.Contains(view, "Cargo") {
		t.Fatalf("missing names:\n%s", view)
	}
	if !strings.Contains(view, "Preview") {
		t.Fatalf("missing preview:\n%s", view)
	}
	if !strings.Contains(view, "cmake") && !strings.Contains(view, "cargo") {
		// patterns may appear as "cmake *"
		if !strings.Contains(view, "build") {
			t.Fatalf("preview missing rules:\n%s", view)
		}
	}
	for _, width := range []int{20, 40, 60, 80, ui.ModalWidth(120)} {
		v := ansi.Strip(sm.view(width, th))
		if v == "" {
			t.Fatalf("width %d empty", width)
		}
		for _, line := range strings.Split(v, "\n") {
			if ansi.StringWidth(line) > width+12 {
				t.Fatalf("width %d line too long: %q", width, line)
			}
		}
	}
}

func TestFTUESchedulerStepSkip(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	fm := newFTUEModal(m.services, "echo", "echo", m.th)
	fm.cursor = int(ftueStepScheduler)
	cat := m.services.SchedulerPresets.(*fakeSchedulerPresets)
	next, _ := fm.update(tea.KeyPressMsg{Code: 's', Text: "s"})
	fm = next.(*ftueModal)
	if !fm.schedulerSkipped || !fm.schedulerReady() {
		t.Fatalf("skip failed: skipped=%v ready=%v", fm.schedulerSkipped, fm.schedulerReady())
	}
	if len(cat.applied) != 0 {
		t.Fatal("skip must not write")
	}
	if fm.cursor != int(ftueStepReady) {
		t.Fatalf("cursor=%d want ready", fm.cursor)
	}
}

func TestFTUESchedulerChildOpensAndCancel(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.width, m.height, m.ready = 100, 40, true
	cat := m.services.SchedulerPresets.(*fakeSchedulerPresets)
	cat.global.Presets = []string{"npm"}

	next, _ := m.handleCommand("/ftue")
	m = next.(Model)
	fm := m.modal.(*ftueModal)
	fm.cursor = int(ftueStepScheduler)
	wantCursor := fm.cursor

	m = updateAppDrain(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	sm, ok := m.modal.(*schedulerPresetsModal)
	if !ok {
		t.Fatalf("child=%T", m.modal)
	}
	if !sm.selected["npm"] {
		t.Fatal("existing preset should be checked")
	}
	if len(m.modalQueue) != 1 {
		t.Fatalf("queue=%d", len(m.modalQueue))
	}

	m = updateAppDrain(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	fm, ok = m.modal.(*ftueModal)
	if !ok {
		t.Fatalf("after esc=%T", m.modal)
	}
	if fm.cursor != wantCursor {
		t.Fatalf("cursor=%d want %d", fm.cursor, wantCursor)
	}
	if fm.schedulerDone {
		t.Fatal("cancel must not mark done")
	}
	if len(cat.applied) != 0 {
		t.Fatal("cancel wrote")
	}
}

func TestFTUESchedulerApplyMarksDone(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.width, m.height, m.ready = 100, 40, true
	cat := m.services.SchedulerPresets.(*fakeSchedulerPresets)

	next, _ := m.handleCommand("/ftue")
	m = next.(Model)
	fm := m.modal.(*ftueModal)
	fm.cursor = int(ftueStepScheduler)

	m = updateAppDrain(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	sm, ok := m.modal.(*schedulerPresetsModal)
	if !ok {
		t.Fatalf("modal=%T", m.modal)
	}
	// Select first preset.
	sm.cursor = 0
	sm.toggle()
	m = updateAppDrain(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	fm, ok = m.modal.(*ftueModal)
	if !ok {
		t.Fatalf("after apply modal=%T", m.modal)
	}
	if !fm.schedulerDone {
		t.Fatal("should be done")
	}
	if !strings.Contains(fm.flash, "saved") {
		t.Fatalf("flash=%q", fm.flash)
	}
	if len(cat.applied) != 1 || len(cat.applied[0]) != 1 {
		t.Fatalf("applied=%v", cat.applied)
	}
}

func TestFTUESchedulerUnavailableDegrades(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.width, m.height, m.ready = 100, 40, true
	m.services.SchedulerPresets = nil
	fm := newFTUEModal(m.services, "echo", "echo", m.th)
	if !fm.schedulerReady() {
		t.Fatal("nil catalog should count ready")
	}
	if got := fm.schedulerDetail(); got != "unavailable" {
		t.Fatalf("detail=%q", got)
	}
	fm.cursor = int(ftueStepScheduler)
	m.modal = fm
	m = updateAppDrain(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	fm, ok := m.modal.(*ftueModal)
	if !ok {
		t.Fatalf("modal=%T", m.modal)
	}
	if !strings.Contains(fm.flash, "unavailable") {
		t.Fatalf("flash=%q", fm.flash)
	}
}

func TestFTUEEstablishedUserLandsOnSchedulerOrReady(t *testing.T) {
	// With provider/model/init ready and tour incomplete, focus tour first;
	// after tour+scheduler done, land on ready.
	m, _ := newAppTestModel(nil, nil)
	m.providerName = "echo"
	m.modelName = "echo-1"
	fi := &fakeInit{exists: true, path: "/tmp/AGENTS.md"}
	m.services.Init = fi
	fm := newFTUEModal(m.services, m.providerName, m.modelName, m.th)
	if fm.cursor != int(ftueStepTour) {
		t.Fatalf("cursor=%d want tour", fm.cursor)
	}
	fm.tourDone = true
	fm.focusFirstIncomplete()
	if fm.cursor != int(ftueStepScheduler) {
		t.Fatalf("cursor after tour=%d want scheduler", fm.cursor)
	}
	fm.schedulerDone = true
	fm.focusFirstIncomplete()
	if fm.cursor != int(ftueStepReady) {
		t.Fatalf("cursor after scheduler=%d want ready", fm.cursor)
	}
}

func TestFTUEViewListsSchedulerStep(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	fm := newFTUEModal(m.services, "echo", "echo", m.th)
	view := ansi.Strip(fm.view(72, theme.Default()))
	if !strings.Contains(view, "Scheduler") && !strings.Contains(strings.ToLower(view), "scheduler") {
		t.Fatalf("view missing scheduler step:\n%s", view)
	}
}
