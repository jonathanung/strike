package tui

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

// E13.4 Bubbles v2 migration re-verify (#597).
//
// Import paths and UPGRADE_GUIDE_V2 API surface landed with E13.1 (#594).
// These tests lock the composer/viewport behaviors that must keep working
// after bubbles v2: Enter sends; shift/alt+enter and ctrl+j newline;
// completion; history; viewport GotoBottom only when AtBottom.

func TestBubblesV2ComposerEnterSends(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	m.providerName = "echo"
	m = typeAppText(t, m, "hello bubbles")
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	runAppCmd(t, cmd)
	op := receiveAppOp(t, ops)
	input, ok := op.(protocol.UserInput)
	if !ok || input.Text != "hello bubbles" {
		t.Fatalf("op = %#v, want UserInput hello bubbles", op)
	}
	if m.composer.Value() != "" {
		t.Errorf("composer not reset after send: %q", m.composer.Value())
	}
}

func TestBubblesV2ComposerNewlines(t *testing.T) {
	cases := []struct {
		name string
		msg  tea.KeyPressMsg
	}{
		{"alt+enter", tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt}},
		{"ctrl+j bare LF", tea.KeyPressMsg{Code: 'j', Mod: tea.ModCtrl}},
		{"shift+enter kitty CSI", keyMsgFromWrapInput(t, "\x1b[13;2u")},
		{"shift+enter xterm CSI", keyMsgFromWrapInput(t, "\x1b[27;2;13~")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, ops := newAppTestModel(nil, nil)
			m.providerName = "echo"
			m = typeAppText(t, m, "a")
			m = updateApp(t, m, tc.msg)
			m = typeAppText(t, m, "b")
			if got := m.composer.Value(); got != "a\nb" {
				t.Fatalf("composer = %q, want a\\nb", got)
			}
			assertNoAppOp(t, ops)
		})
	}
}

func TestBubblesV2CompletionAccept(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = typeAppText(t, m, "/hel")
	if m.completion == nil || len(m.completion.Candidates) == 0 {
		t.Fatal("expected slash completion candidates for /hel")
	}
	m = updateApp(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	if m.completion != nil {
		t.Fatal("tab should accept completion and clear popup")
	}
	if !strings.HasPrefix(m.composer.Value(), "/help") {
		t.Fatalf("composer = %q, want /help…", m.composer.Value())
	}
}

func TestBubblesV2HistoryRecall(t *testing.T) {
	m, _ := newAppTestModelWithHistory(nil, nil, newFakeHistory("older", "newer"))
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	if m.composer.Value() != "" {
		t.Fatalf("setup composer = %q", m.composer.Value())
	}
	m = updateApp(t, m, tea.KeyPressMsg{Code: tea.KeyUp})
	if got := m.composer.Value(); got != "newer" {
		t.Fatalf("history prev = %q, want newer", got)
	}
	m = updateApp(t, m, tea.KeyPressMsg{Code: tea.KeyUp})
	if got := m.composer.Value(); got != "older" {
		t.Fatalf("history prev again = %q, want older", got)
	}
	m = updateApp(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	if got := m.composer.Value(); got != "newer" {
		t.Fatalf("history next = %q, want newer", got)
	}
	m = updateApp(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	if got := m.composer.Value(); got != "" {
		t.Fatalf("history past newest = %q, want empty draft", got)
	}
}

func TestBubblesV2ViewportAnchoring(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	for i := range 50 {
		m.applyEvent(protocol.UserMessage{Text: strings.Repeat("row ", 12) + string(rune('a'+i%26))})
	}
	m.refreshViewport()
	m.viewport.GotoBottom()
	if !m.viewport.AtBottom() {
		t.Fatal("setup: not at bottom")
	}

	// At bottom: live deltas keep follow (GotoBottom).
	m = updateApp(t, m, engineEventMsg{ev: protocol.TextDelta{Text: "tail-follow"}})
	if !m.viewport.AtBottom() {
		t.Fatalf("at-bottom TextDelta lost follow: y=%d total=%d h=%d",
			m.viewport.YOffset(), m.viewport.TotalLineCount(), m.viewport.Height())
	}

	// Scrolled up: refresh must preserve offset (no GotoBottom).
	m = updateApp(t, m, tea.KeyPressMsg{Code: tea.KeyPgUp})
	off := m.viewport.YOffset()
	if m.viewport.AtBottom() || off == 0 {
		t.Fatalf("setup scroll-up failed: atBottom=%v y=%d", m.viewport.AtBottom(), off)
	}
	m = updateApp(t, m, engineEventMsg{ev: protocol.TextDelta{Text: "mid-stream"}})
	if m.viewport.AtBottom() {
		t.Fatal("scrolled-up TextDelta yanked to bottom")
	}
	if got := m.viewport.YOffset(); got < off-2 || got > off+2 {
		t.Fatalf("YOffset=%d want near %d", got, off)
	}
}

func TestBubblesV2WidgetAPISurface(t *testing.T) {
	// UPGRADE_GUIDE_V2: constructors, getters/setters, Styles, DefaultKeyMap().
	ta := textarea.New()
	if !ta.KeyMap.InsertNewline.Enabled() {
		t.Fatal("raw textarea DefaultKeyMap InsertNewline should start enabled")
	}
	ta.SetWidth(40)
	ta.SetHeight(3)
	// Width() may reserve columns for prompt/gutter; assert the setter stuck
	// at a positive size rather than a v1 field write no-op.
	if ta.Width() < 1 || ta.Height() != 3 {
		t.Fatalf("textarea size = %dx%d after SetWidth/SetHeight", ta.Width(), ta.Height())
	}
	ta.SetVirtualCursor(true)
	if !ta.VirtualCursor() {
		t.Fatal("SetVirtualCursor(true) did not stick")
	}
	ta.SetCursorColumn(0)
	styles := textarea.DefaultDarkStyles()
	ta.SetStyles(styles)
	_ = ta.Styles()

	in := textinput.New()
	in.SetWidth(20)
	if in.Width() < 1 {
		t.Fatalf("textinput width = %d after SetWidth", in.Width())
	}
	in.SetVirtualCursor(true)
	if !in.VirtualCursor() {
		t.Fatal("textinput SetVirtualCursor(true) did not stick")
	}
	in.SetStyles(textinput.DefaultDarkStyles())
	_ = in.Styles()

	vp := viewport.New(viewport.WithWidth(80), viewport.WithHeight(3))
	if vp.Width() != 80 || vp.Height() != 3 {
		t.Fatalf("viewport size = %dx%d", vp.Width(), vp.Height())
	}
	vp.SetContent(strings.Repeat("line\n", 20))
	vp.SetYOffset(5)
	if vp.YOffset() != 5 {
		t.Fatalf("YOffset = %d after SetYOffset(5)", vp.YOffset())
	}
	if vp.AtBottom() {
		t.Fatal("mid-scroll viewport should not report AtBottom")
	}
	vp.GotoBottom()
	if !vp.AtBottom() {
		t.Fatal("GotoBottom did not reach AtBottom")
	}
}
