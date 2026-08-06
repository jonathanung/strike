package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

func TestSettingsModalMenuAndDefaults(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	fs := m.services.Settings.(*fakeSettings)
	fs.defaults = host.UserDefaults{
		Theme:          "dracula",
		VimMode:        "pane",
		PermissionMode: "default",
		Provider:       "echo",
	}

	m.composer.SetValue("/settings")
	m = updateApp(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	sm, ok := m.modal.(*settingsModal)
	if !ok {
		t.Fatalf("modal = %T", m.modal)
	}
	view := ansi.Strip(sm.view(80, m.th))
	if !strings.Contains(view, "Defaults") || !strings.Contains(view, "Compaction") || !strings.Contains(view, "Custom providers") {
		t.Fatalf("menu view = %q", view)
	}

	// Open Defaults.
	next, _ := sm.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	sm = next.(*settingsModal)
	view = ansi.Strip(sm.view(80, m.th))
	for _, want := range []string{
		"Theme", "dracula", "Vim mode", "Permission mode",
		"Sandbox", "Notify", "Provider", "echo",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("defaults view missing %q:\n%s", want, view)
		}
	}

	// Open vim mode picker and save overlay.
	sm.cursor = int(settingsFieldVim)
	next, _ = sm.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	sm = next.(*settingsModal)
	if sm.page != settingsPagePick {
		t.Fatalf("page = %v, want pick", sm.page)
	}
	// overlay is index 1
	sm.pickCursor = 1
	_, cmd := sm.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected save cmd")
	}
	msg := cmd()
	saved, ok := msg.(settingsSavedMsg)
	if !ok {
		t.Fatalf("msg = %#v", msg)
	}
	if saved.err != nil {
		t.Fatal(saved.err)
	}
	if saved.value != "overlay" || !saved.apply.hasVim || saved.apply.vimMode != VimModeOverlay {
		t.Fatalf("saved = %#v", saved)
	}
	if len(fs.savedPres) != 1 || fs.savedPres[0].vimMode != "overlay" {
		t.Fatalf("fake savedPres = %#v", fs.savedPres)
	}

	m = updateApp(t, m, saved)
	if m.vimMode != VimModeOverlay {
		t.Fatalf("session vimMode = %q, want overlay", m.vimMode)
	}
	sm, ok = m.modal.(*settingsModal)
	if !ok {
		t.Fatalf("modal after save = %T", m.modal)
	}
	if sm.page != settingsPageDefaults {
		t.Fatalf("page after save = %v", sm.page)
	}
	if sm.defaults.VimMode != "overlay" {
		t.Fatalf("modal defaults vim = %q", sm.defaults.VimMode)
	}
}

func TestSettingsModalSavePermissionMode(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	fs := m.services.Settings.(*fakeSettings)
	sm := newSettingsModal(m.services, m.ops, m.th, m.workDir)
	sm.page = settingsPageDefaults
	sm.cursor = int(settingsFieldPerm)
	next, _ := sm.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	sm = next.(*settingsModal)
	// find yolo
	for i, opt := range sm.pickOptions {
		if opt.value == "yolo" {
			sm.pickCursor = i
			break
		}
	}
	_, cmd := sm.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	msg := cmd().(settingsSavedMsg)
	if msg.err != nil {
		t.Fatal(msg.err)
	}
	if len(fs.saved) != 1 || fs.saved[0].mode != "yolo" {
		t.Fatalf("saved = %#v", fs.saved)
	}
	sm.afterSettingsSaved(msg)
	if sm.defaults.PermissionMode != "yolo" {
		t.Fatalf("defaults perm = %q", sm.defaults.PermissionMode)
	}
}

func TestSettingsModalSaveEffort(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	fs := m.services.Settings.(*fakeSettings)
	sm := newSettingsModal(m.services, m.ops, m.th, m.workDir)
	sm.page = settingsPageDefaults
	sm.cursor = int(settingsFieldEffort)
	next, _ := sm.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	sm = next.(*settingsModal)
	if sm.page != settingsPagePick {
		t.Fatalf("page = %v, want pick", sm.page)
	}
	for i, opt := range sm.pickOptions {
		if opt.value == "xhigh" {
			sm.pickCursor = i
			break
		}
	}
	_, cmd := sm.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	msg := cmd().(settingsSavedMsg)
	if msg.err != nil {
		t.Fatal(msg.err)
	}
	if len(fs.saved) != 1 || fs.saved[0].effort != "xhigh" {
		t.Fatalf("saved = %#v", fs.saved)
	}
	sm.afterSettingsSaved(msg)
	if sm.defaults.Effort != "xhigh" {
		t.Fatalf("defaults effort = %q", sm.defaults.Effort)
	}
}

func TestSettingsModalSaveThemeApplies(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	fs := m.services.Settings.(*fakeSettings)
	sm := newSettingsModal(m.services, m.ops, m.th, "")
	sm.openPick(settingsFieldTheme, settingsPageDefaults)
	// pick first non-strike if available, else strike
	for i, opt := range sm.pickOptions {
		if opt.value == "dracula" {
			sm.pickCursor = i
			break
		}
	}
	want := sm.pickOptions[sm.pickCursor].value
	_, cmd := sm.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	msg := cmd().(settingsSavedMsg)
	if msg.err != nil {
		t.Fatal(msg.err)
	}
	if len(fs.savedThemes) != 1 || fs.savedThemes[0] != want {
		t.Fatalf("savedThemes = %#v want %q", fs.savedThemes, want)
	}
	if msg.apply.theme == nil || msg.apply.theme.ID != want {
		t.Fatalf("apply theme = %#v", msg.apply.theme)
	}
	m.modal = sm
	m = updateApp(t, m, msg)
	if m.themeID != want {
		t.Fatalf("themeID = %q, want %q", m.themeID, want)
	}
}

func TestSettingsModalEscNavigation(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	sm := newSettingsModal(m.services, m.ops, m.th, m.workDir)
	sm.page = settingsPagePick
	sm.pickField = settingsFieldVim
	next, _ := sm.update(tea.KeyPressMsg{Code: tea.KeyEsc})
	sm = next.(*settingsModal)
	if sm.page != settingsPageDefaults {
		t.Fatalf("pick esc → %v, want defaults", sm.page)
	}
	next, _ = sm.update(tea.KeyPressMsg{Code: tea.KeyEsc})
	sm = next.(*settingsModal)
	if sm.page != settingsPageMenu {
		t.Fatalf("defaults esc → %v, want menu", sm.page)
	}
	next, _ = sm.update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if next != nil {
		t.Fatalf("menu esc → %T, want nil", next)
	}
}

func TestSettingsModalDisplayOnlyRows(t *testing.T) {
	sm := newSettingsModal(host.Services{Settings: &fakeSettings{
		defaults: host.UserDefaults{Provider: "openai", Model: "gpt"},
	}}, nil, themeDefaultForTest(), "")
	sm.page = settingsPageDefaults
	sm.cursor = int(settingsFieldProvider)
	next, cmd := sm.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if next != sm || cmd != nil {
		t.Fatalf("provider row should not open picker: next=%T cmd=%v", next, cmd != nil)
	}
}

func TestSettingsModalSaveSandboxAndNotify(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	fs := m.services.Settings.(*fakeSettings)
	sm := newSettingsModal(m.services, m.ops, m.th, m.workDir)
	sm.page = settingsPageDefaults

	// Sandbox → read-only (new sessions only; no live apply).
	sm.cursor = int(settingsFieldSandbox)
	next, _ := sm.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	sm = next.(*settingsModal)
	for i, opt := range sm.pickOptions {
		if opt.value == "read-only" {
			sm.pickCursor = i
			break
		}
	}
	_, cmd := sm.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	msg := cmd().(settingsSavedMsg)
	if msg.err != nil {
		t.Fatal(msg.err)
	}
	if len(fs.savedDials) != 1 || fs.savedDials[0].sandbox != "read-only" {
		t.Fatalf("savedDials sandbox = %#v", fs.savedDials)
	}
	if msg.apply.hasNotify {
		t.Fatal("sandbox save should not apply notify")
	}
	sm.afterSettingsSaved(msg)
	if sm.defaults.Sandbox != "read-only" {
		t.Fatalf("defaults sandbox = %q", sm.defaults.Sandbox)
	}

	// Notify → on (applies live to session).
	sm.cursor = int(settingsFieldNotify)
	next, _ = sm.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	sm = next.(*settingsModal)
	for i, opt := range sm.pickOptions {
		if opt.value == "on" {
			sm.pickCursor = i
			break
		}
	}
	_, cmd = sm.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	msg = cmd().(settingsSavedMsg)
	if msg.err != nil {
		t.Fatal(msg.err)
	}
	if len(fs.savedDials) != 2 || fs.savedDials[1].notify != "on" {
		t.Fatalf("savedDials notify = %#v", fs.savedDials)
	}
	if !msg.apply.hasNotify || msg.apply.notifyMode != NotifyOn {
		t.Fatalf("apply notify = %#v", msg.apply)
	}
	m.modal = sm
	m = updateApp(t, m, msg)
	if m.notifyMode != NotifyOn {
		t.Fatalf("session notifyMode = %q, want on", m.notifyMode)
	}
}

func TestSettingsModalSaveLeanDeferWorktree(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	fs := m.services.Settings.(*fakeSettings)
	sm := newSettingsModal(m.services, m.ops, m.th, m.workDir)
	sm.page = settingsPageDefaults

	cases := []struct {
		field settingsField
		value string
		check func(savedConfigDials) bool
	}{
		{settingsFieldLeanCode, "full", func(d savedConfigDials) bool { return d.leanCode == "full" }},
		{settingsFieldDeferTools, "on", func(d savedConfigDials) bool { return d.deferTools == "on" }},
		{settingsFieldWorktree, "always", func(d savedConfigDials) bool { return d.sessionWorktree == "always" }},
	}
	for _, tc := range cases {
		sm.cursor = int(tc.field)
		next, _ := sm.update(tea.KeyPressMsg{Code: tea.KeyEnter})
		sm = next.(*settingsModal)
		for i, opt := range sm.pickOptions {
			if opt.value == tc.value {
				sm.pickCursor = i
				break
			}
		}
		_, cmd := sm.update(tea.KeyPressMsg{Code: tea.KeyEnter})
		msg := cmd().(settingsSavedMsg)
		if msg.err != nil {
			t.Fatalf("%v: %v", tc.field, msg.err)
		}
		if len(fs.savedDials) == 0 || !tc.check(fs.savedDials[len(fs.savedDials)-1]) {
			t.Fatalf("%v savedDials = %#v", tc.field, fs.savedDials)
		}
		sm.afterSettingsSaved(msg)
	}
	if sm.defaults.LeanCode != "full" || sm.defaults.DeferTools != "on" || sm.defaults.SessionWorktree != "always" {
		t.Fatalf("defaults after dials = %#v", sm.defaults)
	}
}

func TestSettingsModalDefaultsListsPeerDials(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	sm := newSettingsModal(m.services, m.ops, m.th, m.workDir)
	sm.page = settingsPageDefaults
	view := ansi.Strip(sm.view(100, m.th))
	for _, want := range []string{
		"Sandbox", "workspace-write",
		"Notify", "unfocused-only",
		"Lean code", "lite",
		"Defer tools", "off",
		"Session worktree",
		"Permission mode",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("defaults view missing %q:\n%s", want, view)
		}
	}
}

func TestSettingsModalCompactionPageAndSave(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	fs := m.services.Settings.(*fakeSettings)
	sm := newSettingsModal(m.services, m.ops, m.th, m.workDir)

	// Menu → Compaction.
	sm.page = settingsPageMenu
	sm.cursor = int(settingsMenuCompaction)
	next, _ := sm.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	sm = next.(*settingsModal)
	if sm.page != settingsPageCompaction {
		t.Fatalf("page = %v, want compaction", sm.page)
	}
	view := ansi.Strip(sm.view(100, m.th))
	for _, want := range []string{
		"Strategy", "trim",
		"Threshold", "0.70",
		"Buffer", "4096",
		"Keep user turns",
		"Prune protect tokens",
		"Prune protect tools",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("compaction view missing %q:\n%s", want, view)
		}
	}

	// Strategy → summarize.
	sm.cursor = 0 // strategy is first
	next, _ = sm.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	sm = next.(*settingsModal)
	if sm.page != settingsPagePick {
		t.Fatalf("page = %v, want pick", sm.page)
	}
	for i, opt := range sm.pickOptions {
		if opt.value == "summarize" {
			sm.pickCursor = i
			break
		}
	}
	_, cmd := sm.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	msg := cmd().(settingsSavedMsg)
	if msg.err != nil {
		t.Fatal(msg.err)
	}
	if len(fs.savedCompaction) != 1 || fs.savedCompaction[0].Strategy != "summarize" {
		t.Fatalf("savedCompaction = %#v", fs.savedCompaction)
	}
	sm.afterSettingsSaved(msg)
	if sm.page != settingsPageCompaction {
		t.Fatalf("after save page = %v", sm.page)
	}
	if sm.defaults.CompactionStrategy != "summarize" {
		t.Fatalf("strategy = %q", sm.defaults.CompactionStrategy)
	}

	// Threshold → off (1).
	for i, f := range sm.compactionFields() {
		if f == settingsFieldCompactionThreshold {
			sm.cursor = i
			break
		}
	}
	next, _ = sm.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	sm = next.(*settingsModal)
	for i, opt := range sm.pickOptions {
		if opt.value == "1" {
			sm.pickCursor = i
			break
		}
	}
	_, cmd = sm.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	msg = cmd().(settingsSavedMsg)
	if msg.err != nil {
		t.Fatal(msg.err)
	}
	if fs.savedCompaction[len(fs.savedCompaction)-1].Threshold != "1" {
		t.Fatalf("threshold dial = %#v", fs.savedCompaction)
	}
	sm.afterSettingsSaved(msg)
	if sm.defaults.CompactionThreshold != 1 {
		t.Fatalf("threshold = %v", sm.defaults.CompactionThreshold)
	}
}

func TestSettingsModalCompactionModelInput(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	fs := m.services.Settings.(*fakeSettings)
	sm := newSettingsModal(m.services, m.ops, m.th, m.workDir)
	sm.page = settingsPageCompaction
	for i, f := range sm.compactionFields() {
		if f == settingsFieldCompactionModel {
			sm.cursor = i
			break
		}
	}
	next, _ := sm.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	sm = next.(*settingsModal)
	if sm.page != settingsPageInput {
		t.Fatalf("page = %v, want input", sm.page)
	}
	sm.input.SetValue("gpt-compact")
	_, cmd := sm.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	msg := cmd().(settingsSavedMsg)
	if msg.err != nil {
		t.Fatal(msg.err)
	}
	if len(fs.savedCompaction) != 1 || fs.savedCompaction[0].Model != "gpt-compact" {
		t.Fatalf("savedCompaction = %#v", fs.savedCompaction)
	}
	sm.afterSettingsSaved(msg)
	if sm.defaults.CompactionModel != "gpt-compact" {
		t.Fatalf("model = %q", sm.defaults.CompactionModel)
	}

	// Clear model via empty input.
	sm.openInput(settingsFieldCompactionModel, settingsPageCompaction)
	sm.input.SetValue("")
	_, cmd = sm.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	msg = cmd().(settingsSavedMsg)
	if msg.err != nil {
		t.Fatal(msg.err)
	}
	if fs.savedCompaction[len(fs.savedCompaction)-1].Model != "-" {
		t.Fatalf("clear model = %#v", fs.savedCompaction)
	}
	sm.afterSettingsSaved(msg)
	if sm.defaults.CompactionModel != "" {
		t.Fatalf("cleared model = %q", sm.defaults.CompactionModel)
	}
}

func TestSettingsModalCompactionEscNavigation(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	sm := newSettingsModal(m.services, m.ops, m.th, m.workDir)
	sm.page = settingsPagePick
	sm.pickField = settingsFieldCompactionStrategy
	sm.pickReturnPage = settingsPageCompaction
	next, _ := sm.update(tea.KeyPressMsg{Code: tea.KeyEsc})
	sm = next.(*settingsModal)
	if sm.page != settingsPageCompaction {
		t.Fatalf("pick esc → %v, want compaction", sm.page)
	}
	next, _ = sm.update(tea.KeyPressMsg{Code: tea.KeyEsc})
	sm = next.(*settingsModal)
	if sm.page != settingsPageMenu {
		t.Fatalf("compaction esc → %v, want menu", sm.page)
	}
}

func themeDefaultForTest() theme.Theme {
	return theme.Default()
}
