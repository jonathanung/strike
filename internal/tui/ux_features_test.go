package tui

import (
	"io"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

func TestIsEscapeMatchesKeyEscAndStringEsc(t *testing.T) {
	esc := tea.KeyMsg{Type: tea.KeyEsc}
	if esc.String() != "esc" {
		t.Fatalf("KeyEsc.String() = %q, want esc", esc.String())
	}
	if !isEscape(esc) {
		t.Error("isEscape(KeyEsc) = false")
	}
	// String()=="esc" is the secondary path (CSI-u normalized bare ESC).
	// KeyEsc already satisfies both Type and String checks.
	if isEscape(tea.KeyMsg{Type: tea.KeyEnter}) {
		t.Error("isEscape(Enter) = true")
	}
	if isEscape(tea.KeyMsg{Type: tea.KeyCtrlC}) {
		t.Error("isEscape(CtrlC) = true")
	}
}

func TestWrapInputBareEscapeCSIU(t *testing.T) {
	// Bare Escape CSI-u (code 27, mods none or explicit 1) → 0x1b.
	for _, tt := range []struct {
		name, in, want string
	}{
		{"CSI-u bare 27u", "\x1b[27u", "\x1b"},
		{"CSI-u bare 27;1u", "\x1b[27;1u", "\x1b"},
		{"xterm modifyOtherKeys bare esc", "\x1b[27;1;27~", "\x1b"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := io.ReadAll(WrapInput(strings.NewReader(tt.in)))
			if err != nil {
				t.Fatalf("ReadAll: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("WrapInput(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestScrollBindingsCtrlUpDownAndJumpBottom(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	for i := range 60 {
		m.applyEvent(protocol.UserMessage{Text: strings.Repeat("scroll-line ", 6) + string(rune('a'+i%26))})
	}
	m.refreshViewport()
	m.viewport.GotoBottom()
	bottom := m.viewport.YOffset
	if bottom == 0 {
		t.Fatal("setup: long transcript still at top")
	}

	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlUp})
	if m.viewport.YOffset >= bottom {
		t.Errorf("ctrl+up did not scroll up: offset=%d bottom=%d", m.viewport.YOffset, bottom)
	}
	afterUp := m.viewport.YOffset

	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlDown})
	if m.viewport.YOffset <= afterUp {
		t.Errorf("ctrl+down did not scroll down: offset=%d afterUp=%d", m.viewport.YOffset, afterUp)
	}

	// Scroll away, then ctrl+t jumps to bottom and re-enables follow.
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyPgUp})
	if m.viewport.AtBottom() {
		t.Fatal("pgup left viewport at bottom")
	}
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlT})
	if !m.viewport.AtBottom() {
		t.Errorf("ctrl+t did not jump to bottom: YOffset=%d", m.viewport.YOffset)
	}
	m = updateApp(t, m, engineEventMsg{ev: protocol.TextDelta{Text: "follow-after-end"}})
	if !m.viewport.AtBottom() {
		t.Error("after JumpBottom, TextDelta should keep AtBottom follow")
	}
}

func TestDefaultOrientationIsHorizontal(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	if m.splitOrientation != orientHorizontal {
		t.Errorf("default splitOrientation = %v, want horizontal", m.splitOrientation)
	}
}

func TestToggleOrientationAndLayoutCommand(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	if m.splitOrientation != orientHorizontal {
		t.Fatal("expected horizontal start")
	}

	m.toggleOrientation()
	if m.splitOrientation != orientVertical {
		t.Fatalf("after toggle orientation = %v, want vertical", m.splitOrientation)
	}
	if !key.Matches(tea.KeyMsg{Type: tea.KeyCtrlJ}, m.keyMap.FocusLeft) {
		t.Error("vertical: ctrl+j should focus top/left")
	}
	if key.Matches(tea.KeyMsg{Type: tea.KeyCtrlJ}, m.keyMap.CycleWindowNext) {
		t.Error("vertical: ctrl+j should not cycle windows")
	}

	// /layout flips back to horizontal and restores default chords.
	m.composer.SetValue("/layout")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if m.splitOrientation != orientHorizontal {
		t.Fatalf("/layout orientation = %v, want horizontal", m.splitOrientation)
	}
	if !key.Matches(tea.KeyMsg{Type: tea.KeyCtrlJ}, m.keyMap.CycleWindowNext) {
		t.Error("horizontal: ctrl+j should cycle windows")
	}
	if !strings.Contains(m.notice, "horizontal") {
		t.Errorf("layout notice = %q, want horizontal", m.notice)
	}

	// /split is an alias.
	m.composer.SetValue("/split")
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if m.splitOrientation != orientVertical {
		t.Fatalf("/split orientation = %v, want vertical", m.splitOrientation)
	}
	if !strings.Contains(m.notice, "vertical") {
		t.Errorf("split notice = %q, want vertical", m.notice)
	}

	// Wire form is alt+; (after WrapInput rewrites ctrl+; CSI); help stays ctrl+;.
	if !key.Matches(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{';'}, Alt: true}, m.keyMap.ToggleOrientation) {
		t.Error("ToggleOrientation should match alt+; KeyMsg")
	}
	if m.keyMap.ToggleOrientation.Help().Key != "ctrl+;" {
		t.Errorf("ToggleOrientation help key = %q, want ctrl+;", m.keyMap.ToggleOrientation.Help().Key)
	}
}

func TestVerticalFocusKeysSwapNotCycle(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m.windows = windowRegistry{windows: []window{
		statefulTestWindow{windowID: "a", windowTitle: "A"},
		statefulTestWindow{windowID: "b", windowTitle: "B"},
	}}
	m.toggleOrientation()
	if m.splitOrientation != orientVertical {
		t.Fatal("need vertical orientation")
	}
	startIdx := m.windows.index
	// ctrl+j focuses top pane (left), does not cycle windows.
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlJ})
	if m.focus != focusLeft {
		t.Errorf("vertical ctrl+j focus = %v, want left/top", m.focus)
	}
	if m.windows.index != startIdx {
		t.Errorf("vertical ctrl+j cycled window index %d → %d", startIdx, m.windows.index)
	}
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlK})
	if m.focus != focusRight {
		t.Errorf("vertical ctrl+k focus = %v, want right/bottom", m.focus)
	}
	// ctrl+l cycles next window in vertical mode.
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlL})
	if m.windows.active().id() != "b" {
		t.Errorf("vertical ctrl+l window = %s, want b", m.windows.active().id())
	}
}

func TestHorizontalC2GeometryStillHolds(t *testing.T) {
	// Pin that default horizontal split numbers are unchanged.
	for _, tt := range []struct {
		width, left, gutter, right int
	}{
		{93, 60, 1, 32},
		{120, 80, 1, 39},
		{160, 106, 1, 53},
	} {
		got := computePaneGeometry(tt.width, 1, focusLeft)
		if got.leftWidth != tt.left || got.gutter != tt.gutter || got.rightWidth != tt.right {
			t.Errorf("width %d geometry = left=%d gutter=%d right=%d, want %d/%d/%d",
				tt.width, got.leftWidth, got.gutter, got.rightWidth, tt.left, tt.gutter, tt.right)
		}
		if got.mode != paneSplit {
			t.Errorf("width %d mode = %v, want split", tt.width, got.mode)
		}
	}
}

func TestThemeCommandAppearanceAndPicker(t *testing.T) {
	// Save/restore lipgloss background detection and package appearance cache.
	savedDark := lipgloss.HasDarkBackground()
	savedDetected := appearanceDetected
	savedDetectedDark := appearanceDetectedDark
	savedMDStyle := glamourStyleName
	t.Cleanup(func() {
		lipgloss.SetHasDarkBackground(savedDark)
		appearanceDetected = savedDetected
		appearanceDetectedDark = savedDetectedDark
		glamourStyleName = savedMDStyle
	})
	appearanceDetected = false

	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	runTheme := func(args string) Model {
		t.Helper()
		cmd := "/theme"
		if args != "" {
			cmd += " " + args
		}
		m.composer.SetValue(cmd)
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		return updated.(Model)
	}

	m = runTheme("dark")
	if m.appearance != appearanceDark {
		t.Errorf("/theme dark appearance = %q", m.appearance)
	}
	if !lipgloss.HasDarkBackground() {
		t.Error("dark theme did not set HasDarkBackground")
	}
	if !strings.Contains(m.notice, "dark") {
		t.Errorf("notice = %q", m.notice)
	}

	m = runTheme("light")
	if m.appearance != appearanceLight || lipgloss.HasDarkBackground() {
		t.Errorf("light: appearance=%q darkBg=%v", m.appearance, lipgloss.HasDarkBackground())
	}

	m = runTheme("auto")
	if m.appearance != appearanceAuto {
		t.Errorf("auto appearance = %q", m.appearance)
	}

	m = runTheme("nope")
	if !strings.Contains(m.notice, "unknown theme") {
		t.Errorf("bad arg notice = %q", m.notice)
	}

	// Bare /theme opens the theme picker.
	m = runTheme("")
	if _, ok := m.modal.(*themeModal); !ok {
		t.Fatalf("bare /theme modal type = %T, want *themeModal", m.modal)
	}
}

func TestThemeCommandSelectsNamedTheme(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.composer.SetValue("/theme dracula")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if m.themeID != "dracula" {
		t.Fatalf("themeID = %q, want dracula", m.themeID)
	}
	if m.th.Accent.Dark == theme.Default().Accent.Dark {
		t.Error("dracula theme did not change accent from default")
	}
	if !strings.Contains(m.notice, "dracula") {
		t.Errorf("notice = %q", m.notice)
	}
}

func TestThemePickerSelectAndSave(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.composer.SetValue("/theme")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	picker, ok := m.modal.(*themeModal)
	if !ok {
		t.Fatalf("modal = %T", m.modal)
	}
	// Move cursor onto dracula if not already there.
	for i, e := range picker.filtered {
		if e.ID == "dracula" {
			picker.cursor = i
			break
		}
	}
	// ctrl+d saves default without closing.
	settings := m.services.Settings.(*fakeSettings)
	next, saveCmd := picker.update(tea.KeyMsg{Type: tea.KeyCtrlD})
	if next == nil {
		t.Fatal("ctrl+d closed picker")
	}
	if saveCmd == nil {
		t.Fatal("ctrl+d produced no cmd")
	}
	msg := saveCmd()
	saved, ok := msg.(themeSavedMsg)
	if !ok || saved.err != nil || saved.id != "dracula" {
		t.Fatalf("themeSavedMsg = %#v", msg)
	}
	if len(settings.savedThemes) != 1 || settings.savedThemes[0] != "dracula" {
		t.Fatalf("savedThemes = %v", settings.savedThemes)
	}

	// enter selects and closes.
	next, selCmd := picker.update(tea.KeyMsg{Type: tea.KeyEnter})
	if next != nil {
		t.Fatal("enter did not close picker")
	}
	if selCmd == nil {
		t.Fatal("enter produced no cmd")
	}
	selMsg := selCmd()
	entryMsg, ok := selMsg.(themeSelectedMsg)
	if !ok || entryMsg.entry.ID != "dracula" {
		t.Fatalf("themeSelectedMsg = %#v", selMsg)
	}
	m = updateApp(t, m, entryMsg)
	if m.themeID != "dracula" {
		t.Fatalf("after select themeID = %q", m.themeID)
	}
	_ = cmd
}

func TestApplyAppearanceIsTestable(t *testing.T) {
	savedDark := lipgloss.HasDarkBackground()
	savedDetected := appearanceDetected
	savedDetectedDark := appearanceDetectedDark
	savedMDStyle := glamourStyleName
	t.Cleanup(func() {
		lipgloss.SetHasDarkBackground(savedDark)
		appearanceDetected = savedDetected
		appearanceDetectedDark = savedDetectedDark
		glamourStyleName = savedMDStyle
	})
	// Seed detection cache as if the terminal reported dark, then force modes.
	appearanceDetected = true
	appearanceDetectedDark = true

	applyAppearance(appearanceLight)
	if lipgloss.HasDarkBackground() {
		t.Error("applyAppearance(light) left dark background")
	}
	if glamourStyle() != "light" {
		t.Errorf("glamour style after light = %q", glamourStyle())
	}
	applyAppearance(appearanceDark)
	if !lipgloss.HasDarkBackground() {
		t.Error("applyAppearance(dark) left light background")
	}
	if glamourStyle() != "dark" {
		t.Errorf("glamour style after dark = %q", glamourStyle())
	}
	applyAppearance(appearanceAuto)
	if !lipgloss.HasDarkBackground() {
		t.Error("applyAppearance(auto) did not restore detected dark")
	}
	if glamourStyle() != "dark" {
		t.Errorf("glamour style after auto = %q", glamourStyle())
	}
}

func TestChildActivityPaneUpdatesWithoutTranscriptCells(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	cellsBefore := len(m.cells)

	corr := protocol.Correlation{
		SessionID:       "child-act-1",
		ParentSessionID: "parent",
		Depth:           1,
		TurnID:          "ct1",
	}
	m.applyEvent(protocol.ChildStarted{
		Correlation: corr,
		Agent:       "explore-agent",
		Prompt:      "scan the repo for TODOs",
	})
	if len(m.children) != 1 {
		t.Fatalf("children = %d, want 1", len(m.children))
	}
	if m.children[0].status != "running" || m.children[0].agent != "explore-agent" {
		t.Errorf("child state = %+v", m.children[0])
	}
	body := ansi.Strip(m.activityPaneBody(48, 8))
	if !strings.Contains(body, "explore-agent") {
		t.Errorf("activity body missing agent: %q", body)
	}
	if !strings.Contains(body, "scan the repo") && !strings.Contains(body, "TODOs") {
		t.Errorf("activity body missing prompt snippet: %q", body)
	}
	if len(m.cells) != cellsBefore {
		t.Errorf("ChildStarted appended transcript cells: %d → %d", cellsBefore, len(m.cells))
	}

	// Child TextDelta must not create cells or clear activity.
	m.applyEvent(protocol.TextDelta{Correlation: corr, Text: "secret child stream"})
	if len(m.cells) != cellsBefore {
		t.Error("child TextDelta appended cells")
	}
	for _, c := range m.cells {
		if ac, ok := c.(*assistantCell); ok && strings.Contains(ac.text, "secret child") {
			t.Error("child TextDelta leaked into assistant cell")
		}
	}

	m.applyEvent(protocol.ChildCompleted{
		Correlation: corr,
		Status:      protocol.ChildStatusCompleted,
		Summary:     "found three",
	})
	if m.children[0].status != string(protocol.ChildStatusCompleted) {
		t.Errorf("after complete status = %q", m.children[0].status)
	}
	body = ansi.Strip(m.activityPaneBody(48, 8))
	if !strings.Contains(body, "explore-agent") {
		t.Errorf("completed child missing from activity: %q", body)
	}
	if len(m.cells) != cellsBefore {
		t.Error("ChildCompleted appended transcript cells")
	}
}

func TestChildPermissionAskedStillOpensModalFromActivityPath(t *testing.T) {
	// Sibling of child_events_test: ensure activity tracking does not block perms.
	m, _ := newAppTestModel(nil, nil)
	corr := protocol.Correlation{
		SessionID:       "child-perm",
		ParentSessionID: "parent",
		Depth:           1,
	}
	m.applyEvent(protocol.ChildStarted{Correlation: corr, Agent: "build", Prompt: "run"})
	m.applyEvent(protocol.PermissionAsked{
		Correlation: corr,
		RequestID:   "p1",
		Permission:  "bash",
		Patterns:    []string{"ls"},
	})
	if _, ok := m.modal.(*permissionModal); !ok {
		t.Fatalf("modal = %T, want permissionModal", m.modal)
	}
}

func TestNoticeRowsForWrapsLongHelp(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.composer.SetValue("/help")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if m.notice == "" {
		t.Fatal("/help left empty notice")
	}
	rows := m.noticeRowsFor(40)
	if rows <= 1 {
		t.Errorf("noticeRowsFor(40) = %d, want > 1 for long /help text", rows)
	}
	// Visible notice keeps leading commands (later ones may ellipsize at maxNoticeRows).
	plain := ansi.Strip(m.noticeView(40, maxNoticeRows))
	for _, want := range []string{"/provider", "/model", "/agent"} {
		if !strings.Contains(plain, want) {
			t.Errorf("/help notice missing %q:\n%s", want, plain)
		}
	}
	// Full notice text (before row cap) includes the new theme/layout commands.
	if !strings.Contains(m.notice, "/theme") || !strings.Contains(m.notice, "/layout") {
		t.Errorf("/help notice text missing theme/layout: %q", m.notice)
	}
	if strings.Count(plain, "\n")+1 < 2 {
		t.Errorf("wrapped notice still single line: %q", plain)
	}
}

func TestKeybindCatalogIncludesJumpBottomToggleAndScroll(t *testing.T) {
	keys := defaultKeyMap()
	catalog := keybindCatalog(keys)
	want := map[string]string{
		"nav.jump-bottom":   keys.JumpBottom.Help().Key,
		"nav.toggle-orient": keys.ToggleOrientation.Help().Key,
		"nav.scroll-up":     keys.ScrollUp.Help().Key,
		"nav.scroll-down":   keys.ScrollDown.Help().Key,
	}
	seen := map[string]keybindEntry{}
	for _, e := range catalog {
		seen[e.ID] = e
	}
	for id, helpKey := range want {
		e, ok := seen[id]
		if !ok {
			t.Errorf("catalog missing %q", id)
			continue
		}
		if e.Keys != helpKey || e.Action == "" {
			t.Errorf("%s = keys=%q action=%q, want keys=%q", id, e.Keys, e.Action, helpKey)
		}
	}
}
