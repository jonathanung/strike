package tui

import (
	"image/color"
	"io"
	"strings"
	"testing"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2/compat"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

func TestIsEscapeMatchesKeyEscAndStringEsc(t *testing.T) {
	esc := tea.KeyPressMsg{Code: tea.KeyEsc}
	if esc.String() != "esc" {
		t.Fatalf("KeyEsc.String() = %q, want esc", esc.String())
	}
	if !isEscape(esc) {
		t.Error("isEscape(KeyEsc) = false")
	}
	// String()=="esc" is the secondary path (CSI-u normalized bare ESC).
	// KeyEsc already satisfies both Type and String checks.
	if isEscape(tea.KeyPressMsg{Code: tea.KeyEnter}) {
		t.Error("isEscape(Enter) = true")
	}
	if isEscape(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}) {
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
	bottom := m.viewport.YOffset()
	if bottom == 0 {
		t.Fatal("setup: long transcript still at top")
	}

	m = updateApp(t, m, tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModCtrl})
	if m.viewport.YOffset() >= bottom {
		t.Errorf("ctrl+up did not scroll up: offset=%d bottom=%d", m.viewport.YOffset(), bottom)
	}
	afterUp := m.viewport.YOffset()

	m = updateApp(t, m, tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModCtrl})
	if m.viewport.YOffset() <= afterUp {
		t.Errorf("ctrl+down did not scroll down: offset=%d afterUp=%d", m.viewport.YOffset(), afterUp)
	}

	// Scroll away, then ctrl+t jumps to bottom and re-enables follow.
	m = updateApp(t, m, tea.KeyPressMsg{Code: tea.KeyPgUp})
	if m.viewport.AtBottom() {
		t.Fatal("pgup left viewport at bottom")
	}
	m = updateApp(t, m, tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	if !m.viewport.AtBottom() {
		t.Errorf("ctrl+t did not jump to bottom: YOffset=%d", m.viewport.YOffset())
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
	// Chords stay orientation-independent (#414).
	if !key.Matches(tea.KeyPressMsg{Code: 'h', Mod: tea.ModCtrl}, m.keyMap.FocusLeft) {
		t.Error("vertical: ctrl+h should focus left/primary")
	}
	if !key.Matches(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl}, m.keyMap.CycleWindowNext) {
		t.Error("vertical: ctrl+o should cycle windows")
	}
	if key.Matches(tea.KeyPressMsg{Code: 'j', Mod: tea.ModCtrl}, m.keyMap.FocusLeft, m.keyMap.CycleWindowNext) {
		t.Error("vertical: ctrl+j must remain newline-only")
	}

	// /layout flips back to horizontal; chords unchanged.
	m.composer.SetValue("/layout")
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	if m.splitOrientation != orientHorizontal {
		t.Fatalf("/layout orientation = %v, want horizontal", m.splitOrientation)
	}
	if !key.Matches(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl}, m.keyMap.CycleWindowNext) {
		t.Error("horizontal: ctrl+o should cycle windows")
	}
	if !strings.Contains(m.notice, "horizontal") {
		t.Errorf("layout notice = %q, want horizontal", m.notice)
	}

	// /split is an alias.
	m.composer.SetValue("/split")
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	if m.splitOrientation != orientVertical {
		t.Fatalf("/split orientation = %v, want vertical", m.splitOrientation)
	}
	if !strings.Contains(m.notice, "vertical") {
		t.Errorf("split notice = %q, want vertical", m.notice)
	}

	// Wire form is alt+; (after WrapInput rewrites ctrl+; CSI); help stays ctrl+;.
	if !key.Matches(tea.KeyPressMsg{Code: ';', Mod: tea.ModAlt}, m.keyMap.ToggleOrientation) {
		t.Error("ToggleOrientation should match alt+; KeyMsg")
	}
	if m.keyMap.ToggleOrientation.Help().Key != "ctrl+;" {
		t.Errorf("ToggleOrientation help key = %q, want ctrl+;", m.keyMap.ToggleOrientation.Help().Key)
	}
}

func TestVerticalFocusKeysStayOrientationIndependent(t *testing.T) {
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
	// ctrl+j newlines even in vertical split (#414).
	m.composer.SetValue("v")
	m.composer.SetCursorColumn(1)
	m = updateApp(t, m, tea.KeyPressMsg{Code: 'j', Mod: tea.ModCtrl})
	if got := m.composer.Value(); got != "v\n" {
		t.Errorf("vertical left bare LF composer = %q, want v\\n", got)
	}
	if m.focus != focusLeft {
		t.Errorf("vertical left bare LF focus = %v, want left", m.focus)
	}
	if m.windows.index != startIdx {
		t.Errorf("vertical left bare LF cycled window index %d → %d", startIdx, m.windows.index)
	}
	// ctrl+h / ctrl+l still focus primary/secondary.
	m = updateApp(t, m, tea.KeyPressMsg{Code: 'l', Mod: tea.ModCtrl})
	if m.focus != focusRight {
		t.Errorf("vertical ctrl+l focus = %v, want right/secondary", m.focus)
	}
	m = updateApp(t, m, tea.KeyPressMsg{Code: 'h', Mod: tea.ModCtrl})
	if m.focus != focusLeft {
		t.Errorf("vertical ctrl+h focus = %v, want left/primary", m.focus)
	}
	// ctrl+o cycles secondary panes.
	m = updateApp(t, m, tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	if m.windows.active().id() != "b" {
		t.Errorf("vertical ctrl+o window = %s, want b", m.windows.active().id())
	}
	// Mid-line ctrl+k kills; empty ctrl+k opens palette.
	m.focus = focusLeft
	m.composer.Focus()
	m.composer.SetValue("top bottom")
	m.composer.SetCursorColumn(3)
	m = updateApp(t, m, tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl})
	if m.focus != focusLeft {
		t.Errorf("vertical left ctrl+k with text focus = %v, want left", m.focus)
	}
	if got := m.composer.Value(); got != "top" {
		t.Errorf("vertical left ctrl+k composer = %q, want top", got)
	}
	if m.modal != nil {
		t.Errorf("mid-line kill opened modal %T", m.modal)
	}
	m.composer.SetValue("")
	m = updateApp(t, m, tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl})
	if _, ok := m.modal.(*paletteModal); !ok {
		t.Errorf("empty ctrl+k modal = %T, want palette", m.modal)
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
	savedDark := compat.HasDarkBackground
	savedMDStyle := glamourStyleName
	t.Cleanup(func() {
		compat.HasDarkBackground = savedDark
		glamourStyleName = savedMDStyle
	})

	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	// Seed terminal detection so /theme auto restores a known value.
	m.detectedDark = true
	m.applyAppearance()

	runTheme := func(args string) Model {
		t.Helper()
		cmd := "/theme"
		if args != "" {
			cmd += " " + args
		}
		m.composer.SetValue(cmd)
		updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		return updated.(Model)
	}

	m = runTheme("dark")
	if m.appearance != appearanceDark {
		t.Errorf("/theme dark appearance = %q", m.appearance)
	}
	if !compat.HasDarkBackground {
		t.Error("dark theme did not set HasDarkBackground")
	}
	if !strings.Contains(m.notice, "dark") {
		t.Errorf("notice = %q", m.notice)
	}

	m = runTheme("light")
	if m.appearance != appearanceLight || compat.HasDarkBackground {
		t.Errorf("light: appearance=%q darkBg=%v", m.appearance, compat.HasDarkBackground)
	}

	m = runTheme("auto")
	if m.appearance != appearanceAuto {
		t.Errorf("auto appearance = %q", m.appearance)
	}
	if !compat.HasDarkBackground {
		t.Error("auto did not restore detected dark background")
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
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
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
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
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
	next, saveCmd := picker.update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
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
	next, selCmd := picker.update(tea.KeyPressMsg{Code: tea.KeyEnter})
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
	savedDark := compat.HasDarkBackground
	savedMDStyle := glamourStyleName
	t.Cleanup(func() {
		compat.HasDarkBackground = savedDark
		glamourStyleName = savedMDStyle
	})

	m, _ := newAppTestModel(nil, nil)
	m.detectedDark = true

	m.appearance = appearanceLight
	m.applyAppearance()
	if compat.HasDarkBackground {
		t.Error("applyAppearance(light) left dark background")
	}
	if glamourStyle() != "light" {
		t.Errorf("glamour style after light = %q", glamourStyle())
	}
	m.appearance = appearanceDark
	m.applyAppearance()
	if !compat.HasDarkBackground {
		t.Error("applyAppearance(dark) left light background")
	}
	if glamourStyle() != "dark" {
		t.Errorf("glamour style after dark = %q", glamourStyle())
	}
	m.appearance = appearanceAuto
	m.applyAppearance()
	if !compat.HasDarkBackground {
		t.Error("applyAppearance(auto) did not restore detected dark")
	}
	if glamourStyle() != "dark" {
		t.Errorf("glamour style after auto = %q", glamourStyle())
	}
}

func TestBackgroundColorMsgFeedsAppearance(t *testing.T) {
	savedDark := compat.HasDarkBackground
	savedMDStyle := glamourStyleName
	t.Cleanup(func() {
		compat.HasDarkBackground = savedDark
		glamourStyleName = savedMDStyle
	})

	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.appearance = appearanceAuto
	m.detectedDark = true
	m.applyAppearance()

	// Light terminal background.
	m = updateApp(t, m, tea.BackgroundColorMsg{Color: color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}})
	if m.detectedDark {
		t.Fatal("light BackgroundColorMsg left detectedDark true")
	}
	if compat.HasDarkBackground || glamourStyle() != "light" {
		t.Errorf("auto+light bg: darkBg=%v glamour=%q", compat.HasDarkBackground, glamourStyle())
	}

	// Forced dark ignores a subsequent light detection update for styling,
	// but still records detectedDark for when the user returns to auto.
	m.appearance = appearanceDark
	m.applyAppearance()
	m = updateApp(t, m, tea.BackgroundColorMsg{Color: color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}})
	if m.detectedDark {
		t.Fatal("forced dark path should still record light detection")
	}
	if !compat.HasDarkBackground || glamourStyle() != "dark" {
		t.Errorf("forced dark with light detect: darkBg=%v glamour=%q", compat.HasDarkBackground, glamourStyle())
	}

	m.appearance = appearanceAuto
	m.applyAppearance()
	if compat.HasDarkBackground {
		t.Error("auto after light detect should be light")
	}

	// Dark terminal background.
	m = updateApp(t, m, tea.BackgroundColorMsg{Color: color.RGBA{R: 0, G: 0, B: 0, A: 0xff}})
	if !m.detectedDark || !compat.HasDarkBackground || glamourStyle() != "dark" {
		t.Errorf("dark msg: detected=%v darkBg=%v glamour=%q", m.detectedDark, compat.HasDarkBackground, glamourStyle())
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
	// ChildCompleted adds exactly one collapsed subagent result row.
	if len(m.cells) != cellsBefore+1 {
		t.Errorf("ChildCompleted cells = %d, want %d", len(m.cells), cellsBefore+1)
	}
	sc, ok := m.cells[len(m.cells)-1].(*subagentResultCell)
	if !ok {
		t.Fatalf("last cell = %T, want *subagentResultCell", m.cells[len(m.cells)-1])
	}
	if sc.expanded {
		t.Error("subagent result should default collapsed")
	}
	if sc.agent != "explore-agent" || sc.summary != "found three" {
		t.Errorf("subagent cell = %+v", sc)
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

func TestHelpCommandOpensFilterableCatalogModal(t *testing.T) {
	m, _ := newAppTestModel(nil, []host.Skill{fakeSkill("review", "review a change", "")})
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.composer.SetValue("/help")
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	help, ok := m.modal.(*helpModal)
	if !ok {
		t.Fatalf("/help modal = %T, want helpModal", m.modal)
	}
	if m.notice != "" {
		t.Errorf("/help set notice %q, want empty (modal is unclipped)", m.notice)
	}
	wantLabels := []string{
		"/provider", "/model", "/settings", "/session", "/rename", "/export", "/timeline", "/copy", "/theme", "/memory",
		"/issues", "/compact", "/fork", "/undo", "/rewind", "/fast", "/think", "/layout", "/md-read", "/keys", "/legend", "/exit", "/quit", "/palette", "/interrupt", "/agent-next", "/focus-left", "/review", "tab",
	}
	for _, want := range wantLabels {
		found := false
		for _, entry := range help.entries {
			if entry.Label == want || strings.HasPrefix(entry.Label, want+" ") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("/help catalog omitted %q", want)
		}
	}
	m = updateApp(t, m, tea.KeyPressMsg{Text: "session"})
	help = m.modal.(*helpModal)
	filtered := help.filtered()
	if len(filtered) == 0 || !strings.HasPrefix(filtered[0].Label, "/session") {
		t.Fatalf("filter session = %#v, want /session first", filtered)
	}
	plain := ansi.Strip(viewString(m))
	if !strings.Contains(plain, "/session") {
		t.Errorf("help modal view missing filtered command:\n%s", plain)
	}
	m = updateApp(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.modal != nil {
		t.Errorf("esc left help modal open: %T", m.modal)
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
		"global.copy-last":  keys.CopyLastResponse.Help().Key,
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
