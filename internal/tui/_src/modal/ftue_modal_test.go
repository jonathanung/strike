package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

func applyFTUECmds(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	for _, msg := range runAllAppCmds(t, cmd) {
		if msg == nil {
			continue
		}
		updated, next := m.Update(msg)
		m = updated.(Model)
		if next != nil {
			m = applyFTUECmds(t, m, next)
		}
	}
	return m
}

func updateAppDrain(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	updated, cmd := m.Update(msg)
	m = updated.(Model)
	if cmd != nil {
		m = applyFTUECmds(t, m, cmd)
	}
	return m
}

func TestFTUEInCommandCatalog(t *testing.T) {
	found := false
	for _, spec := range builtinCommandSpecs {
		if spec.ID == commandFTUE && spec.Name == "/ftue" {
			found = true
			if !strings.Contains(spec.Description, "setup wizard") {
				t.Fatalf("description = %q", spec.Description)
			}
			break
		}
	}
	if !found {
		t.Fatal("/ftue missing from builtinCommandSpecs")
	}
	if _, ok := reservedCommandNames["ftue"]; !ok {
		t.Fatal("ftue not reserved")
	}
	if validSkillName("ftue") {
		t.Fatal("ftue should not be a valid skill name")
	}
}

func TestFTUEOpenDoesNotWriteSettings(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	fs := &fakeSettings{}
	m.services.Settings = fs
	m.providerName = "echo"
	m.modelName = "echo"

	next, cmd := m.handleCommand("/ftue")
	nm := next.(Model)
	if cmd != nil {
		t.Fatal("open should not schedule cmds that write settings")
	}
	if _, ok := nm.modal.(*ftueModal); !ok {
		t.Fatalf("modal = %T, want *ftueModal", nm.modal)
	}
	if len(fs.saved) != 0 || len(fs.savedThemes) != 0 {
		t.Fatalf("settings writes on open: saved=%v themes=%v", fs.saved, fs.savedThemes)
	}
}

func TestFTUEOpenDoesNotAcknowledgeOnboarding(t *testing.T) {
	// Opening (manual or auto) must not consume onboarding state — only
	// finish/dismiss do, so an interrupted flow can reopen next launch.
	m, _ := newAppTestModel(nil, nil)
	ob := &fakeOnboarding{autoOpen: true}
	m.services.Onboarding = ob
	m.firstRun = true

	next, cmd := m.handleCommand("/ftue")
	if cmd != nil {
		t.Fatal("open must not schedule acknowledge cmd")
	}
	m = next.(Model)
	if ob.acks != 0 {
		t.Fatalf("open acknowledged: acks=%d", ob.acks)
	}
	if !ob.autoOpen {
		t.Fatal("open cleared autoOpen")
	}

	m = updateApp(t, m, firstRunSetupMsg{})
	// Already opened via /ftue; firstRunSetup should no-op (modal open / flag).
	if ob.acks != 0 {
		t.Fatalf("firstRunSetup acknowledged: acks=%d", ob.acks)
	}
}

func TestFTUECancelLeavesSettingsUntouched(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	fs := &fakeSettings{defaults: host.UserDefaults{Provider: "echo", Model: "echo"}}
	m.services.Settings = fs
	m.providerName = "echo"
	m.modelName = "echo"

	next, _ := m.handleCommand("/ftue")
	m = next.(Model)
	m = updateAppDrain(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.modal != nil {
		t.Fatalf("modal after esc = %T, want nil", m.modal)
	}
	if len(fs.saved) != 0 {
		t.Fatalf("cancel wrote settings: %+v", fs.saved)
	}
	if m.providerName != "echo" || m.modelName != "echo" {
		t.Fatalf("session selection changed: %s/%s", m.providerName, m.modelName)
	}
}

func TestFTUEManualRemainsAfterAcknowledge(t *testing.T) {
	// After global ack, /ftue must still open (manual re-run).
	m, _ := newAppTestModel(nil, nil)
	ob := &fakeOnboarding{autoOpen: false}
	m.services.Onboarding = ob
	m.firstRun = false
	next, _ := m.handleCommand("/ftue")
	m = next.(Model)
	if _, ok := m.modal.(*ftueModal); !ok {
		t.Fatalf("modal = %T, want *ftueModal", m.modal)
	}
	m = updateAppDrain(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if ob.acks != 1 {
		t.Fatalf("manual dismiss should still ack (idempotent): acks=%d", ob.acks)
	}
	// Open again after ack.
	next, _ = m.handleCommand("/ftue")
	m = next.(Model)
	if _, ok := m.modal.(*ftueModal); !ok {
		t.Fatalf("re-open after ack = %T, want *ftueModal", m.modal)
	}
}

func TestFTUEFinishFocusesComposer(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.providerName = "echo"
	m.modelName = "echo"
	m.width, m.height, m.ready = 100, 40, true
	m.focus = focusRight

	next, _ := m.handleCommand("/ftue")
	m = next.(Model)
	m = updateAppDrain(t, m, tea.KeyPressMsg{Code: 'f', Text: "f"})
	if m.modal != nil {
		t.Fatalf("modal after f = %T", m.modal)
	}
	if m.focus != focusLeft {
		t.Fatalf("focus = %v, want left (composer)", m.focus)
	}
	if !strings.Contains(m.notice, "setup complete") {
		t.Fatalf("notice = %q", m.notice)
	}
}

func TestFTUEPreservesStepAcrossProviderChild(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	m.width, m.height, m.ready = 100, 40, true

	next, _ := m.handleCommand("/ftue")
	m = next.(Model)
	fm, ok := m.modal.(*ftueModal)
	if !ok {
		t.Fatalf("modal = %T", m.modal)
	}
	fm.cursor = int(ftueStepProvider)
	wantCursor := fm.cursor

	m = updateAppDrain(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if _, ok := m.modal.(*providerModal); !ok {
		t.Fatalf("child modal = %T, want *providerModal", m.modal)
	}
	if len(m.modalQueue) != 1 {
		t.Fatalf("queue len = %d, want 1", len(m.modalQueue))
	}
	parked, ok := m.modalQueue[0].(*ftueModal)
	if !ok || parked.cursor != wantCursor {
		t.Fatalf("parked wizard cursor = %v ok=%v, want %d", m.modalQueue[0], ok, wantCursor)
	}

	m = updateAppDrain(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	fm, ok = m.modal.(*ftueModal)
	if !ok {
		t.Fatalf("after child esc modal = %T", m.modal)
	}
	if fm.cursor != wantCursor {
		t.Fatalf("cursor after return = %d, want %d", fm.cursor, wantCursor)
	}
	assertNoAppOp(t, ops)
}

func TestFTUEProviderChildSuccessReflectsState(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	m.width, m.height, m.ready = 100, 40, true

	next, _ := m.handleCommand("/ftue")
	m = next.(Model)
	fm := m.modal.(*ftueModal)
	fm.cursor = int(ftueStepProvider)

	m = updateAppDrain(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	pm, ok := m.modal.(*providerModal)
	if !ok {
		t.Fatalf("modal = %T", m.modal)
	}
	for i, s := range pm.statuses {
		if s.Name == "echo" {
			pm.cursor = i
			break
		}
	}
	m = updateAppDrain(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	op := receiveAppOp(t, ops)
	sel, ok := op.(protocol.SelectModel)
	if !ok || sel.Provider != "echo" {
		t.Fatalf("op = %#v", op)
	}
	// Engine would emit ModelSelected; simulate and ensure wizard tracks it.
	_ = m.applyEvent(protocol.ModelSelected{Provider: "echo", Model: "echo"})
	if f, ok := m.modal.(*ftueModal); ok {
		if f.provider != "echo" || f.model != "echo" {
			t.Fatalf("wizard state = %s/%s", f.provider, f.model)
		}
	} else if len(m.modalQueue) > 0 {
		if f, ok := m.modalQueue[0].(*ftueModal); ok {
			if f.provider != "echo" {
				t.Fatalf("queued wizard provider = %q", f.provider)
			}
		}
	} else {
		t.Fatalf("wizard lost after provider select; modal=%T", m.modal)
	}
}

func TestFTUEModelChildRequiresProvider(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.width, m.height, m.ready = 100, 40, true
	m.providerName = ""
	m.modelName = ""

	next, _ := m.handleCommand("/ftue")
	m = next.(Model)
	fm := m.modal.(*ftueModal)
	fm.cursor = int(ftueStepModel)

	m = updateAppDrain(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	fm, ok := m.modal.(*ftueModal)
	if !ok {
		t.Fatalf("modal = %T, want wizard back", m.modal)
	}
	if !strings.Contains(fm.flash, "provider") {
		t.Fatalf("flash = %q, want provider hint", fm.flash)
	}
}

func TestFTUEModelChildOpensWhenProviderSet(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.width, m.height, m.ready = 100, 40, true
	m.providerName = "echo"
	m.modelName = ""

	next, _ := m.handleCommand("/ftue")
	m = next.(Model)
	fm := m.modal.(*ftueModal)
	fm.cursor = int(ftueStepModel)

	m = updateAppDrain(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if _, ok := m.modal.(*modelModal); !ok {
		t.Fatalf("modal = %T, want *modelModal", m.modal)
	}
	if len(m.modalQueue) != 1 {
		t.Fatalf("queue len = %d", len(m.modalQueue))
	}
	// Cancel model picker → wizard returns.
	m = updateAppDrain(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if _, ok := m.modal.(*ftueModal); !ok {
		t.Fatalf("after model esc = %T", m.modal)
	}
}

func TestFTUEInitOptionalConfirmPreserved(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.width, m.height, m.ready = 100, 40, true
	fi := &fakeInit{exists: true, path: "/tmp/proj/AGENTS.md"}
	m.services.Init = fi

	next, _ := m.handleCommand("/ftue")
	m = next.(Model)
	fm := m.modal.(*ftueModal)
	fm.cursor = int(ftueStepInit)
	wantCursor := fm.cursor

	m = updateAppDrain(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if _, ok := m.modal.(*initConfirmModal); !ok {
		t.Fatalf("modal = %T, want init confirm", m.modal)
	}
	m = updateAppDrain(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if fi.writeN != 0 {
		t.Fatalf("cancel wrote init writeN=%d", fi.writeN)
	}
	fm, ok := m.modal.(*ftueModal)
	if !ok {
		t.Fatalf("after init cancel modal = %T", m.modal)
	}
	if fm.cursor != wantCursor {
		t.Fatalf("cursor = %d, want %d", fm.cursor, wantCursor)
	}
}

func TestFTUEInitCreateFromWizard(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.width, m.height, m.ready = 100, 40, true
	fi := &fakeInit{path: "/tmp/proj/AGENTS.md"}
	m.services.Init = fi

	next, _ := m.handleCommand("/ftue")
	m = next.(Model)
	fm := m.modal.(*ftueModal)
	fm.cursor = int(ftueStepInit)

	m = updateAppDrain(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if fi.writeN != 1 || fi.forceN != 0 {
		t.Fatalf("writeN=%d forceN=%d", fi.writeN, fi.forceN)
	}
	if _, ok := m.modal.(*ftueModal); !ok {
		t.Fatalf("modal after init create = %T", m.modal)
	}
}

func TestFTUEInitUnavailableDegrades(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.width, m.height, m.ready = 100, 40, true
	m.services.Init = nil

	next, _ := m.handleCommand("/ftue")
	m = next.(Model)
	fm := m.modal.(*ftueModal)
	if !fm.initReady() {
		t.Fatal("nil Init should count as ready (optional/unavailable)")
	}
	if got := fm.initDetail(); got != "unavailable" {
		t.Fatalf("initDetail = %q", got)
	}
	fm.cursor = int(ftueStepInit)
	m = updateAppDrain(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	fm, ok := m.modal.(*ftueModal)
	if !ok {
		t.Fatalf("modal = %T", m.modal)
	}
	if !strings.Contains(fm.flash, "unavailable") {
		t.Fatalf("flash = %q", fm.flash)
	}
}

func TestFTUEAuthUnavailableDegrades(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.services.Auth = nil
	m.providerName = ""
	fm := newFTUEModal(m.services, "", "", m.th)
	if fm.providerReady() {
		t.Fatal("empty provider should not be ready")
	}
	if got := fm.providerDetail(); got != "auth unavailable" {
		t.Fatalf("providerDetail = %q", got)
	}
	fm.syncFrom("echo", "")
	if !fm.providerReady() {
		t.Fatal("selected provider with nil Auth should degrade to ready")
	}
}

func TestFTUESkipInit(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	fi := &fakeInit{exists: false, path: "/tmp/proj/AGENTS.md"}
	m.services.Init = fi
	fm := newFTUEModal(m.services, "echo", "echo", m.th)
	fm.cursor = int(ftueStepInit)
	next, _ := fm.update(tea.KeyPressMsg{Code: 's', Text: "s"})
	fm = next.(*ftueModal)
	if !fm.initSkipped || !fm.initReady() {
		t.Fatalf("skip failed: skipped=%v ready=%v", fm.initSkipped, fm.initReady())
	}
	if fi.writeN != 0 {
		t.Fatal("skip must not write")
	}
	if fm.cursor != int(ftueStepReady) {
		t.Fatalf("cursor after skip = %d, want ready", fm.cursor)
	}
}

func TestFTUEEstablishedUserShowsComplete(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.providerName = "echo"
	m.modelName = "echo-1"
	fi := &fakeInit{exists: true, path: "/tmp/AGENTS.md"}
	m.services.Init = fi
	fm := newFTUEModal(m.services, m.providerName, m.modelName, m.th)
	if !fm.providerReady() || !fm.modelReady() || !fm.initReady() {
		t.Fatalf("established user incomplete: p=%v m=%v i=%v", fm.providerReady(), fm.modelReady(), fm.initReady())
	}
	if fm.cursor != int(ftueStepReady) {
		t.Fatalf("cursor = %d, want ready", fm.cursor)
	}
}

func TestFTUEConstrainedWidths(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.providerName = "echo"
	m.modelName = "echo"
	fm := newFTUEModal(m.services, m.providerName, m.modelName, m.th)
	th := theme.Default().Resolve()
	for _, width := range []int{20, 40, 60, 80, ui.ModalWidth(40), ui.ModalWidth(120)} {
		view := ansi.Strip(fm.view(width, th))
		if view == "" {
			t.Fatalf("width %d: empty view", width)
		}
		if !strings.Contains(view, "Connect") && !strings.Contains(strings.ToLower(view), "provider") {
			t.Fatalf("width %d missing step content:\n%s", width, view)
		}
		for _, line := range strings.Split(view, "\n") {
			if ansi.StringWidth(line) > width+12 {
				t.Fatalf("width %d line too long (%d): %q", width, ansi.StringWidth(line), line)
			}
		}
	}
}

func TestFTUEViewReflectsStatus(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.providerName = "echo"
	m.modelName = "echo-1"
	fm := newFTUEModal(m.services, m.providerName, m.modelName, m.th)
	view := ansi.Strip(fm.view(72, theme.Default()))
	if !strings.Contains(view, "echo") {
		t.Fatalf("view missing provider:\n%s", view)
	}
	if !strings.Contains(view, "echo-1") && !strings.Contains(view, "echo/echo-1") {
		t.Fatalf("view missing model:\n%s", view)
	}
}

func TestFTUEHelpListsCommand(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	next, _ := m.handleCommand("/help")
	m = next.(Model)
	hm, ok := m.modal.(*helpModal)
	if !ok {
		t.Fatalf("modal = %T", m.modal)
	}
	hm.filter = "ftue"
	view := ansi.Strip(hm.view(80, m.th))
	if !strings.Contains(view, "/ftue") {
		t.Fatalf("help missing /ftue:\n%s", view)
	}
}
