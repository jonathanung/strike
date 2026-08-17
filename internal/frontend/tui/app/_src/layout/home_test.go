package tui

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/frontend/host"
	"github.com/jonathanung/strike-cli/internal/frontend/tui/theme"
	"github.com/jonathanung/strike-cli/internal/frontend/tui/ui"
	"github.com/jonathanung/strike-cli/internal/protocol"
)

func TestShowHomeLayoutOnlyWhenTranscriptEmpty(t *testing.T) {
	m, _ := newAppTestModelHome(nil, nil)
	if !m.showHomeLayout() {
		t.Fatal("empty root transcript should use home layout")
	}
	m.applyEvent(protocol.UserMessage{Text: "hello"})
	if m.showHomeLayout() {
		t.Fatal("home layout should end after first user message")
	}
}

func TestHomeLayoutRendersCenteredPromptAndContextBar(t *testing.T) {
	m, _ := newAppTestModelHome([]string{"build"}, []host.Skill{fakeSkill("review", "review code", "Review $ARGUMENTS")})
	m.agentName = "build"
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})

	plain := ansi.Strip(viewString(m))
	for _, want := range []string{
		"CONTEXT",          // thin context kicker
		"build",            // agent in context / header
		"INSTRUCTION",      // composer kicker
		"Direct the work.", // empty-state title
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("home layout missing %q:\n%s", want, plain)
		}
	}
	// Multi-pane session stack titles should not dominate the home screen.
	if strings.Contains(plain, "╭─ activity") || strings.Contains(plain, "╭─ system") ||
		strings.Contains(plain, "┌─ activity") || strings.Contains(plain, "┌─ system") {
		t.Errorf("home layout showed multi-pane stack chrome:\n%s", plain)
	}
	// Footer is composer-oriented (#679).
	if !strings.Contains(plain, "send") {
		t.Errorf("home footer missing send hint:\n%s", plain)
	}
}

func TestHomeLayoutSurfacesRecentFromHistory(t *testing.T) {
	without, _ := newAppTestModelHome(nil, nil)
	without = updateApp(t, without, tea.WindowSizeMsg{Width: 120, Height: 40})
	if plain := ansi.Strip(viewString(without)); strings.Contains(plain, "recent") {
		t.Errorf("home recent line without history:\n%s", plain)
	}

	store := newFakeHistory("earlier prompt one", "earlier prompt two")
	with, _ := newAppTestModelHomeWithHistory(nil, nil, store)
	with = updateApp(t, with, tea.WindowSizeMsg{Width: 120, Height: 40})
	plain := ansi.Strip(viewString(with))
	if !strings.Contains(plain, "recent") || !strings.Contains(plain, "earlier prompt two") {
		t.Errorf("home layout missing recent history:\n%s", plain)
	}
}

func TestHomeLayoutSwitchesToMultiPaneAfterFirstPrompt(t *testing.T) {
	m, _ := newAppTestModelHome(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	if plain := ansi.Strip(viewString(m)); !strings.Contains(plain, "INSTRUCTION") {
		t.Fatalf("expected home composer kicker:\n%s", plain)
	}

	m.applyEvent(protocol.UserMessage{Text: "hello strike"})
	m.refreshViewport()
	m.reflow()
	plain := ansi.Strip(viewString(m))
	if !strings.Contains(plain, "hello strike") {
		t.Errorf("transcript missing user message:\n%s", plain)
	}
	// Right pane stack becomes available in multi-pane layout.
	if !strings.Contains(plain, "CONTEXT") {
		t.Errorf("multi-pane missing context after first prompt:\n%s", plain)
	}
}

// TestHomeCtrlLOpensMultiPane covers #684: on the lean launch screen, ctrl+l
// opens the right pane column; the launch stack stays as the left pane.
func TestHomeCtrlLOpensMultiPane(t *testing.T) {
	m, _ := newAppTestModelHome(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	if !m.showHomeLayout() {
		t.Fatal("expected lean home before ctrl+l")
	}
	plainHome := ansi.Strip(viewString(m))
	if !strings.Contains(plainHome, "Direct the work.") {
		t.Fatalf("home missing empty-state title:\n%s", plainHome)
	}
	// Discoverability: lean home footerHints include focus-right (may truncate
	// in a narrow KeyHints row, so assert the hint set directly).
	if !footerHintsContain(m.footerHints(), m.keyMap.FocusRight) {
		t.Errorf("home footerHints missing focus-right: %+v", m.footerHints())
	}
	// Lean home has no right-pane stack chrome.
	if strings.Contains(plainHome, "╭─ activity") || strings.Contains(plainHome, "╭─ system") ||
		strings.Contains(plainHome, "┌─ activity") || strings.Contains(plainHome, "┌─ system") {
		t.Errorf("lean home showed multi-pane stack chrome:\n%s", plainHome)
	}

	m = updateApp(t, m, tea.KeyPressMsg{Code: 'l', Mod: tea.ModCtrl})
	if m.showHomeLayout() {
		t.Fatal("ctrl+l should leave lean home for multi-pane")
	}
	if !m.homePanesOpen {
		t.Fatal("homePanesOpen should stick after focus-right from home")
	}
	if m.focus != focusRight {
		t.Fatalf("focus = %v, want right", m.focus)
	}
	if m.composer.Focused() {
		t.Fatal("composer should blur when right pane is focused")
	}
	plain := ansi.Strip(viewString(m))
	// Right column panels visible (session stack titles).
	if !strings.Contains(plain, "ACTIVITY") && !strings.Contains(plain, "SYSTEM") {
		t.Errorf("multi-pane missing right panels after ctrl+l:\n%s", plain)
	}
	// Left launch stack still has the composer (instruction kicker).
	if !strings.Contains(plain, "INSTRUCTION") {
		t.Errorf("left launch stack missing composer after ctrl+l:\n%s", plain)
	}
	// Split geometry: right pane column is present (not full-screen lean home).
	// Empty left may still show welcome cards/logo in the transcript slot.
	if computePaneGeometry(m.width, m.paneGutter(), m.focus).mode != paneSplit {
		// Wide enough terminal should split; if single, right-only is ok when
		// focused right — still not lean home.
		if m.showHomeLayout() {
			t.Fatal("still on lean home after ctrl+l")
		}
	}

	// Sticky: ctrl+h focuses left without collapsing back to lean home.
	m = updateApp(t, m, tea.KeyPressMsg{Code: 'h', Mod: tea.ModCtrl})
	if m.focus != focusLeft {
		t.Fatalf("ctrl+h focus = %v, want left", m.focus)
	}
	if m.showHomeLayout() {
		t.Fatal("ctrl+h must not collapse multi-pane back to lean home")
	}
	if !m.homePanesOpen {
		t.Fatal("homePanesOpen should remain sticky on focus-left")
	}
	plainLeft := ansi.Strip(viewString(m))
	if !strings.Contains(plainLeft, "ACTIVITY") && !strings.Contains(plainLeft, "SYSTEM") {
		t.Errorf("right panels should stay after focus-left:\n%s", plainLeft)
	}
	if !m.composer.Focused() {
		t.Fatal("composer should refocus on left")
	}
}

func footerHintsContain(hints []ui.KeyHint, binding key.Binding) bool {
	want := keyHint(binding).Key
	for _, h := range hints {
		if h.Key == want {
			return true
		}
	}
	return false
}

func TestHomeFocusRightSlashOpensMultiPane(t *testing.T) {
	m, _ := newAppTestModelHome(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	next, _ := m.handleCommand("/focus-right")
	m = next.(Model)
	m.reflow()
	if m.showHomeLayout() || !m.homePanesOpen || m.focus != focusRight {
		t.Fatalf("home=%v panesOpen=%v focus=%v after /focus-right", m.showHomeLayout(), m.homePanesOpen, m.focus)
	}
}

func TestHomePromptWidthClamps(t *testing.T) {
	if got := homePromptWidth(20); got != 20 {
		t.Errorf("narrow homePromptWidth = %d, want 20", got)
	}
	if got := homePromptWidth(200); got != homePromptMaxWidth {
		t.Errorf("wide homePromptWidth = %d, want %d", got, homePromptMaxWidth)
	}
	if got := homePromptWidth(40); got < homePromptMinWidth || got > homePromptMaxWidth {
		t.Errorf("mid homePromptWidth = %d out of range", got)
	}
}

func TestComputeHomeLayoutBudgetsExactHeight(t *testing.T) {
	for _, h := range []int{24, 30, 40, 12} {
		hl := computeHomeLayout(100, h, 3, 0, false, 0, true)
		sum := hl.header + hl.context + hl.center + hl.notice + hl.popup + hl.hints + hl.danger
		if sum != h {
			t.Errorf("height %d: regions sum %d (h=%+v)", h, sum, hl)
		}
		if hl.center < hl.composer && hl.center > 0 {
			t.Errorf("height %d: center %d < composer %d", h, hl.center, hl.composer)
		}
	}
}

func TestComposerInputModeDetection(t *testing.T) {
	m, _ := newAppTestModelHome(nil, nil)
	cases := []struct {
		val  string
		want string
	}{
		{"", "chat"},
		{"hello", "chat"},
		{"!ls", "shell"},
		{"  !pwd", "shell"},
		{"/model", "command"},
		{"/help me", "command"},
	}
	for _, tt := range cases {
		m.composer.SetValue(tt.val)
		if got := m.composerInputMode(); got != tt.want {
			t.Errorf("composerInputMode(%q) = %q, want %q", tt.val, got, tt.want)
		}
	}
}

func TestComposerTitleShowsModeAndQueue(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.focus = focusLeft
	m.composer.SetValue("hello")
	title := ansi.Strip(m.composerTitle(m.th.Resolve(), true))
	if !strings.Contains(title, "INSTRUCTION") {
		t.Errorf("title missing instruction kicker: %q", title)
	}
	if !strings.Contains(title, "READY") {
		t.Errorf("title missing send-state: %q", title)
	}
	m.composer.SetValue("!echo hi")
	title = ansi.Strip(m.composerTitle(m.th.Resolve(), true))
	if !strings.Contains(title, "SHELL") {
		t.Errorf("title missing shell mode: %q", title)
	}
}

func TestFooterHintsContextSensitive(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m.focus = focusLeft
	left := ansi.Strip(ui.KeyHints(m.th, 160, m.footerHints()))
	if !strings.Contains(left, "send") {
		t.Errorf("left footer missing send: %q", left)
	}
	// Should not dump the old overloaded pane/window sentence.
	if strings.Contains(left, "windows") && strings.Contains(left, "panes") {
		t.Errorf("left footer still overloaded: %q", left)
	}

	m.focus = focusRight
	right := ansi.Strip(ui.KeyHints(m.th, 160, m.footerHints()))
	if !strings.Contains(right, "select") || !strings.Contains(right, "open") {
		t.Errorf("right footer missing nav: %q", right)
	}
	if strings.Contains(right, keyHint(m.keyMap.Send).Label) {
		t.Errorf("right footer retained send: %q", right)
	}
}

func TestDistributeFlexSizesPrefersContent(t *testing.T) {
	// context=5, activity=flex, telemetry=5 in 30 cells → activity gets remainder.
	got := distributeFlexSizes(30, 3, 6, []int{8, 0, 8})
	if got == nil {
		t.Fatal("nil sizes")
	}
	if got[0] != 8 || got[2] != 8 {
		t.Fatalf("preferred panes = %v, want 8 and 8", got)
	}
	if got[1] != 14 {
		t.Fatalf("flex pane = %d, want 14", got[1])
	}
	sum := got[0] + got[1] + got[2]
	if sum != 30 {
		t.Fatalf("sum = %d, want 30", sum)
	}
}

func TestComputeMemberSlotsFlexShrinksSparsePanes(t *testing.T) {
	// Equal split would give ~13 each; preferred keeps context/telemetry tight.
	slots := computeMemberSlots(40, 40, 1, 3, false, []int{8, 0, 7})
	if slots == nil {
		t.Fatal("nil slots")
	}
	if slots[0].height != 8 {
		t.Errorf("context height = %d, want 8", slots[0].height)
	}
	if slots[2].height != 7 {
		t.Errorf("telemetry height = %d, want 7", slots[2].height)
	}
	if slots[1].height < 20 {
		t.Errorf("activity flex height = %d, want >= 20", slots[1].height)
	}
}

func TestLeftFocusComposerBorderTokens(t *testing.T) {
	setTUITrueColor(t)
	th := theme.Default()
	th.Surface = fixedColor("#112233")
	th.SurfaceFocus = fixedColor("#445566")
	th.BorderFocus = fixedColor("#778899")
	m, _ := newAppTestModelWithOptions(Options{Theme: &th})
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	composer := m.composerView(false, 60, 6)
	if plain := ansi.Strip(composer); !strings.Contains(plain, "INSTRUCTION") {
		t.Fatalf("composer missing instruction kicker:\n%s", plain)
	}
	if !strings.Contains(composer, rgbSGR("#778899")) {
		t.Fatal("prompt box missing BorderFocus when left-focused")
	}
	if strings.Contains(composer, rgbBGSGR("#445566")) {
		t.Fatal("bordered prompt should not wash title edge with SurfaceFocus")
	}
}

func TestHomeCompletionStaysAttachedToPrompt(t *testing.T) {
	m, _ := newAppTestModelHome(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.composer.SetValue("/")
	m.recomputeCompletion()
	if m.completion == nil {
		t.Fatal("slash completion did not open")
	}
	lines := strings.Split(ansi.Strip(viewString(m)), "\n")
	popupRow, promptRow := -1, -1
	for i, line := range lines {
		if popupRow < 0 && strings.Contains(line, "select a provider") {
			popupRow = i
		}
		if promptRow < 0 && strings.Contains(line, "COMMAND") {
			promptRow = i
		}
	}
	if popupRow < 0 || promptRow < 0 {
		t.Fatalf("completion or prompt missing (popup=%d prompt=%d):\n%s", popupRow, promptRow, strings.Join(lines, "\n"))
	}
	if promptRow <= popupRow {
		t.Errorf("completion rendered below prompt: popup row %d, prompt row %d", popupRow, promptRow)
	}
	for i := popupRow; i < promptRow; i++ {
		if strings.TrimSpace(lines[i]) == "" {
			t.Errorf("blank row separates completion from prompt at row %d", i)
		}
	}
}
