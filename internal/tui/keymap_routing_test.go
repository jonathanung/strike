package tui

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

// keyMsgFromWrapInput feeds terminal wire bytes through WrapInput and maps the
// legacy stream Bubble Tea would read to the KeyMsg it emits. Shift/Alt+Enter
// CSI rewrites to ESC+\r; BT's sequence table decodes that as KeyEnter+Alt
// (bubbletea key_sequences: "\x1b"+CR → Key{Type: KeyEnter, Alt: true}).
func keyMsgFromWrapInput(t *testing.T, wire string) tea.KeyMsg {
	t.Helper()
	got, err := io.ReadAll(WrapInput(strings.NewReader(wire)))
	if err != nil {
		t.Fatalf("WrapInput(%q): %v", wire, err)
	}
	switch {
	case bytes.Equal(got, altEnter):
		return tea.KeyMsg{Type: tea.KeyEnter, Alt: true}
	default:
		t.Fatalf("WrapInput(%q) = %q (%v), no KeyMsg mapping for this test", wire, got, got)
		return tea.KeyMsg{}
	}
}

// TestLeftFocusKeymapRoutingMatrix pins left-focus chords so UX fixes cannot
// silently rebind each other (#58). Each case hits exactly one handler class.
func TestLeftFocusKeymapRoutingMatrix(t *testing.T) {
	type kind int
	const (
		kindSend kind = iota
		kindNewline
		kindScrollUp
		kindScrollDown
		kindJumpBottom
		kindToggle
		kindFocusLeft
		kindFocusRight
		kindKillLineEnd // left-focus ctrl+k is readline kill, not cycle prev
		kindPalette
	)

	tests := []struct {
		name string
		msg  tea.KeyMsg
		want kind
	}{
		{"send enter", tea.KeyMsg{Type: tea.KeyEnter}, kindSend},
		{"newline alt+enter", tea.KeyMsg{Type: tea.KeyEnter, Alt: true}, kindNewline},
		// Bare LF / ctrl+j on left focus is newline, never cycle (#187).
		{"newline ctrl+j bare LF", tea.KeyMsg{Type: tea.KeyCtrlJ}, kindNewline},
		{"scroll pgup", tea.KeyMsg{Type: tea.KeyPgUp}, kindScrollUp},
		{"scroll ctrl+up", tea.KeyMsg{Type: tea.KeyCtrlUp}, kindScrollUp},
		{"scroll pgdown", tea.KeyMsg{Type: tea.KeyPgDown}, kindScrollDown},
		{"scroll ctrl+down", tea.KeyMsg{Type: tea.KeyCtrlDown}, kindScrollDown},
		{"jump ctrl+t", tea.KeyMsg{Type: tea.KeyCtrlT}, kindJumpBottom},
		{"toggle alt+;", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{';'}, Alt: true}, kindToggle},
		{"focus left ctrl+h", tea.KeyMsg{Type: tea.KeyCtrlH}, kindFocusLeft},
		{"focus right ctrl+l", tea.KeyMsg{Type: tea.KeyCtrlL}, kindFocusRight},
		{"kill line end ctrl+k", tea.KeyMsg{Type: tea.KeyCtrlK}, kindKillLineEnd},
		{"palette ctrl+p", tea.KeyMsg{Type: tea.KeyCtrlP}, kindPalette},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, ops := newAppTestModel(nil, nil)
			m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
			m.providerName = "echo"
			m.modelName = "echo-1"
			m.windows = windowRegistry{windows: []window{
				statefulTestWindow{windowID: "a", windowTitle: "A"},
				statefulTestWindow{windowID: "b", windowTitle: "B"},
			}}
			// Long transcript so scroll offsets are observable.
			for i := range 60 {
				m.applyEvent(protocol.UserMessage{Text: strings.Repeat("line ", 8) + string(rune('a'+i%26))})
			}
			m.refreshViewport()
			m.viewport.GotoBottom()
			bottom := m.viewport.YOffset

			startOrient := m.splitOrientation
			startFocus := m.focus
			startWin := m.windows.index
			m.composer.SetValue("hello world")
			m.composer.SetCursor(5) // after "hello" so ctrl+k kills " world"
			composerBefore := m.composer.Value()

			updated, cmd := m.Update(tt.msg)
			m = updated.(Model)

			switch tt.want {
			case kindSend:
				if m.composer.Value() != "" {
					t.Errorf("send left composer = %q, want empty", m.composer.Value())
				}
				if cmd == nil {
					t.Fatal("send produced no cmd")
				}
				// Drain op without blocking the suite forever.
				_ = runAppCmd(t, cmd)
				select {
				case op := <-ops:
					if _, ok := op.(protocol.UserInput); !ok {
						t.Errorf("send op = %T, want UserInput", op)
					}
				default:
					// history batch may interleave; presence of reset is enough
				}
			case kindNewline:
				if !strings.Contains(m.composer.Value(), "\n") {
					t.Errorf("newline composer = %q, want embedded \\n", m.composer.Value())
				}
				if m.windows.index != startWin {
					t.Errorf("newline cycled window %d → %d", startWin, m.windows.index)
				}
				assertNoAppOp(t, ops)
			case kindScrollUp:
				if m.viewport.YOffset >= bottom {
					t.Errorf("scroll up offset=%d bottom=%d", m.viewport.YOffset, bottom)
				}
				if m.composer.Value() != composerBefore {
					t.Errorf("scroll up changed composer to %q", m.composer.Value())
				}
			case kindScrollDown:
				m.viewport.GotoTop()
				m = updateApp(t, m, tt.msg)
				if m.viewport.YOffset <= 0 {
					t.Errorf("scroll down offset=%d, want > 0", m.viewport.YOffset)
				}
				if m.composer.Value() != composerBefore {
					t.Errorf("scroll down changed composer to %q", m.composer.Value())
				}
			case kindJumpBottom:
				m.viewport.GotoTop()
				m = updateApp(t, m, tt.msg)
				if !m.viewport.AtBottom() {
					t.Errorf("jump bottom AtBottom=false offset=%d", m.viewport.YOffset)
				}
			case kindToggle:
				if m.splitOrientation == startOrient {
					t.Errorf("toggle left orientation = %v", m.splitOrientation)
				}
			case kindFocusLeft:
				m.focus = focusRight
				m = updateApp(t, m, tt.msg)
				if m.focus != focusLeft {
					t.Errorf("focus left = %v", m.focus)
				}
			case kindFocusRight:
				if m.focus != focusRight {
					// start was left; one ctrl+l → right
					m = updateApp(t, m, tt.msg)
				}
				if m.focus != focusRight {
					t.Errorf("focus right = %v", m.focus)
				}
			case kindKillLineEnd:
				if m.composer.Value() != "hello" {
					t.Errorf("ctrl+k composer = %q, want %q", m.composer.Value(), "hello")
				}
				if m.windows.index != startWin {
					t.Errorf("ctrl+k cycled window %d → %d", startWin, m.windows.index)
				}
				if m.focus != startFocus {
					t.Errorf("ctrl+k changed focus to %v", m.focus)
				}
				if _, ok := m.modal.(*paletteModal); ok {
					t.Error("ctrl+k opened palette")
				}
			case kindPalette:
				if _, ok := m.modal.(*paletteModal); !ok {
					t.Errorf("palette modal = %T", m.modal)
				}
			}
		})
	}
}

// TestShiftEnterNewlineWithoutPaneCycle covers the full WrapInput CSI → KeyMsg
// → Model.Update path for shift/alt+enter (#53, #187). Composer must gain a real
// newline, no UserInput op is sent, and CycleWindow* must not fire under either
// split orientation. Also pins bare LF (KeyCtrlJ) — the actual sequence many
// terminals still emit for shift+enter when enhanced keys are unavailable.
func TestShiftEnterNewlineWithoutPaneCycle(t *testing.T) {
	wires := []struct {
		name string
		wire string
		// bareLF skips WrapInput and injects KeyCtrlJ directly (legacy shift+enter).
		bareLF bool
	}{
		{"kitty shift+enter", "\x1b[13;2u", false},
		{"xterm shift+enter", "\x1b[27;2;13~", false},
		{"kitty alt+enter", "\x1b[13;3u", false},
		{"xterm alt+enter", "\x1b[27;3;13~", false},
		// Shift+Alt+Enter (mods=4 → bits shift|alt) also rewrites to alt+enter.
		{"kitty shift+alt+enter", "\x1b[13;4u", false},
		{"xterm shift+alt+enter", "\x1b[27;4;13~", false},
		{"bare LF shift+enter", "\n", true},
	}
	orients := []struct {
		name   string
		orient splitOrientation
	}{
		{"horizontal", orientHorizontal},
		{"vertical", orientVertical},
	}

	for _, o := range orients {
		for _, tt := range wires {
			t.Run(o.name+"/"+tt.name, func(t *testing.T) {
				var msg tea.KeyMsg
				if tt.bareLF {
					// Bubble Tea maps bare LF to KeyCtrlJ — the regression wire.
					msg = tea.KeyMsg{Type: tea.KeyCtrlJ}
				} else {
					msg = keyMsgFromWrapInput(t, tt.wire)
				}

				m, ops := newAppTestModel(nil, nil)
				m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
				m.providerName = "echo"
				if o.orient == orientVertical {
					m.toggleOrientation()
				}
				m.windows = windowRegistry{windows: []window{
					statefulTestWindow{windowID: "a", windowTitle: "A"},
					statefulTestWindow{windowID: "b", windowTitle: "B"},
				}}
				startWin := m.windows.index
				startFocus := m.focus
				startOrient := m.splitOrientation
				m.composer.SetValue("line1")
				// Cursor at end so InsertString("\n") appends.
				m.composer.SetCursor(len("line1"))

				if !key.Matches(msg, m.keyMap.Newline) {
					t.Fatalf("msg %v does not match Newline", msg)
				}
				if key.Matches(msg, m.keyMap.Send) {
					t.Fatalf("msg %v matched Send", msg)
				}
				// CSI path must not collide with cycle/focus bindings. Bare LF
				// (KeyCtrlJ) intentionally shares the ctrl+j chord with cycle;
				// left-focus Update routing prefers Newline (#187).
				if !tt.bareLF {
					if key.Matches(msg, m.keyMap.CycleWindowNext, m.keyMap.CycleWindowPrev) {
						t.Fatalf("post-WrapInput msg %v matched CycleWindow*", msg)
					}
					if key.Matches(msg, m.keyMap.FocusLeft, m.keyMap.FocusRight) {
						t.Fatalf("post-WrapInput msg %v matched Focus*", msg)
					}
				}

				updated, cmd := m.Update(msg)
				m = updated.(Model)
				if cmd != nil {
					// Newline must be pure state; drain only if a cmd slipped through.
					if drained := runAppCmd(t, cmd); drained != nil {
						t.Errorf("newline produced msg %T", drained)
					}
				}

				if got := m.composer.Value(); got != "line1\n" {
					t.Errorf("composer = %q, want %q", got, "line1\n")
				}
				if m.windows.index != startWin {
					t.Errorf("window cycled %d → %d", startWin, m.windows.index)
				}
				if m.windows.active().id() != "a" {
					t.Errorf("active window = %q, want a", m.windows.active().id())
				}
				if m.focus != startFocus {
					t.Errorf("focus changed %v → %v", startFocus, m.focus)
				}
				if m.splitOrientation != startOrient {
					t.Errorf("orientation changed %v → %v", startOrient, m.splitOrientation)
				}
				assertNoAppOp(t, ops)

				// Continue typing on the new line; still no send.
				m = typeAppText(t, m, "line2")
				if got := m.composer.Value(); got != "line1\nline2" {
					t.Errorf("after type composer = %q, want line1\\nline2", got)
				}
				assertNoAppOp(t, ops)
			})
		}
	}
}

// TestCtrlJCyclesWindowsOnlyWhenRightFocused pins that intentional ctrl+j still
// cycles the right-pane window list when focus is on the right (#187).
func TestCtrlJCyclesWindowsOnlyWhenRightFocused(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m.windows = windowRegistry{windows: []window{
		statefulTestWindow{windowID: "a", windowTitle: "A"},
		statefulTestWindow{windowID: "b", windowTitle: "B"},
	}}
	m.composer.SetValue("keep")
	// Left: ctrl+j → newline, window stays.
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlJ})
	if got := m.composer.Value(); got != "keep\n" {
		t.Fatalf("left ctrl+j composer = %q, want keep\\n", got)
	}
	if m.windows.index != 0 {
		t.Fatalf("left ctrl+j window = %d, want 0", m.windows.index)
	}
	// Right: ctrl+j → cycle.
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlL})
	if m.focus != focusRight {
		t.Fatalf("focus = %v, want right", m.focus)
	}
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlJ})
	if m.windows.index != 1 || m.windows.active().id() != "b" {
		t.Errorf("right ctrl+j window = %d/%s, want 1/b", m.windows.index, m.windows.active().id())
	}
	if got := m.composer.Value(); got != "keep\n" {
		t.Errorf("right ctrl+j changed composer to %q", got)
	}
	assertNoAppOp(t, ops)
}

// TestShiftEnterNewlineWithCompletionOpen pins that shift+enter inserts a
// newline and dismisses neither panes nor completion via CycleWindow.
func TestShiftEnterNewlineWithCompletionOpen(t *testing.T) {
	msg := keyMsgFromWrapInput(t, "\x1b[13;2u")

	m, ops := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.windows = windowRegistry{windows: []window{
		statefulTestWindow{windowID: "a", windowTitle: "A"},
		statefulTestWindow{windowID: "b", windowTitle: "B"},
	}}
	startWin := m.windows.index
	m = typeAppText(t, m, "/hel")
	m.recomputeCompletion()
	if m.completion == nil {
		t.Fatal("expected completion open for /hel")
	}

	m = updateApp(t, m, msg)
	if got := m.composer.Value(); !strings.Contains(got, "\n") {
		t.Errorf("composer = %q, want embedded newline", got)
	}
	if m.windows.index != startWin {
		t.Errorf("window cycled %d → %d", startWin, m.windows.index)
	}
	assertNoAppOp(t, ops)
}

// TestCtrlSemicolonToggleOrientationViaKeyMsg covers the real chord (#54 / #26).
func TestCtrlSemicolonToggleOrientationViaKeyMsg(t *testing.T) {
	// Wire: enhanced ctrl+; → alt+;.
	for _, wire := range []string{"\x1b[59;5u", "\x1b[27;5;59~"} {
		got, err := io.ReadAll(WrapInput(strings.NewReader(wire)))
		if err != nil {
			t.Fatalf("WrapInput(%q): %v", wire, err)
		}
		if string(got) != string(altSemicolon) {
			t.Fatalf("WrapInput(%q) = %q, want alt+; %q", wire, got, altSemicolon)
		}
	}

	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	if m.splitOrientation != orientHorizontal {
		t.Fatal("want horizontal start")
	}
	// Update path: alt+; KeyMsg (post-rewrite form).
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{';'}, Alt: true}
	if !key.Matches(msg, m.keyMap.ToggleOrientation) {
		t.Fatal("alt+; does not match ToggleOrientation")
	}
	m = updateApp(t, m, msg)
	if m.splitOrientation != orientVertical {
		t.Fatalf("after ctrl+; orientation = %v, want vertical", m.splitOrientation)
	}
	// Focus/cycle keys swap under vertical.
	if !key.Matches(tea.KeyMsg{Type: tea.KeyCtrlJ}, m.keyMap.FocusLeft) {
		t.Error("vertical: ctrl+j should focus top")
	}
	if key.Matches(tea.KeyMsg{Type: tea.KeyCtrlJ}, m.keyMap.CycleWindowNext) {
		t.Error("vertical: ctrl+j should not cycle")
	}
	m = updateApp(t, m, msg)
	if m.splitOrientation != orientHorizontal {
		t.Fatalf("second toggle orientation = %v, want horizontal", m.splitOrientation)
	}
	if m.keyMap.ToggleOrientation.Help().Key != "ctrl+;" {
		t.Errorf("help = %q, want ctrl+;", m.keyMap.ToggleOrientation.Help().Key)
	}
}

// TestTranscriptScrollChordsAndMouseWheel covers scroll ownership (#55 / #32).
func TestTranscriptScrollChordsAndMouseWheel(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	for i := range 80 {
		m.applyEvent(protocol.UserMessage{Text: strings.Repeat("scroll ", 10) + string(rune('a'+i%26))})
	}
	m.refreshViewport()
	m.viewport.GotoBottom()
	bottom := m.viewport.YOffset
	if bottom == 0 {
		t.Fatal("setup: expected scrollable transcript")
	}
	m.composer.SetValue("keep-me")

	// Left-focus keyboard scroll.
	for _, msg := range []tea.KeyMsg{
		{Type: tea.KeyPgUp},
		{Type: tea.KeyCtrlUp},
	} {
		m.viewport.GotoBottom()
		before := m.composer.Value()
		m = updateApp(t, m, msg)
		if m.viewport.YOffset >= bottom {
			t.Errorf("%v did not scroll transcript up", msg)
		}
		if m.composer.Value() != before {
			t.Errorf("%v changed composer to %q", msg, m.composer.Value())
		}
	}
	m.viewport.GotoTop()
	top := m.viewport.YOffset
	for _, msg := range []tea.KeyMsg{
		{Type: tea.KeyPgDown},
		{Type: tea.KeyCtrlDown},
	} {
		m.viewport.GotoTop()
		m = updateApp(t, m, msg)
		if m.viewport.YOffset <= top {
			t.Errorf("%v did not scroll transcript down", msg)
		}
		if m.composer.Value() != "keep-me" {
			t.Errorf("%v changed composer to %q", msg, m.composer.Value())
		}
	}

	// Right-focus: scroll still moves transcript, not only the terminal pane.
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlL})
	if m.focus != focusRight {
		t.Fatalf("focus = %v, want right", m.focus)
	}
	m.viewport.GotoBottom()
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyPgUp})
	if m.viewport.YOffset >= bottom {
		t.Error("pgup with right focus did not scroll transcript")
	}
	if m.composer.Value() != "keep-me" {
		t.Errorf("right-focus scroll changed composer to %q", m.composer.Value())
	}

	// Mouse wheel → transcript.
	m.viewport.GotoBottom()
	m = updateApp(t, m, tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonWheelUp,
	})
	if m.viewport.YOffset >= bottom {
		t.Error("wheel up did not scroll transcript")
	}
	afterWheelUp := m.viewport.YOffset
	m = updateApp(t, m, tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonWheelDown,
	})
	if m.viewport.YOffset <= afterWheelUp {
		t.Error("wheel down did not scroll transcript")
	}
	if m.composer.Value() != "keep-me" {
		t.Errorf("wheel changed composer to %q", m.composer.Value())
	}
}

// TestJumpToBottomBindingCtrlT covers ctrl+t target (#56 / #34).
func TestJumpToBottomBindingCtrlT(t *testing.T) {
	keys := defaultKeyMap()
	if keys.JumpBottom.Help().Key != "ctrl+t" {
		t.Fatalf("JumpBottom help = %q, want ctrl+t", keys.JumpBottom.Help().Key)
	}
	if !key.Matches(tea.KeyMsg{Type: tea.KeyCtrlT}, keys.JumpBottom) {
		t.Fatal("ctrl+t does not match JumpBottom")
	}
	// Catalog / help stay in sync.
	var found bool
	for _, e := range keybindCatalog(keys) {
		if e.ID == "nav.jump-bottom" {
			found = true
			if e.Keys != "ctrl+t" {
				t.Errorf("catalog jump-bottom keys = %q, want ctrl+t", e.Keys)
			}
		}
	}
	if !found {
		t.Error("catalog missing nav.jump-bottom")
	}

	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	for i := range 50 {
		m.applyEvent(protocol.UserMessage{Text: strings.Repeat("j ", 20) + string(rune('a'+i%26))})
	}
	m.refreshViewport()
	m.viewport.GotoTop()
	if m.viewport.AtBottom() {
		t.Fatal("setup still at bottom")
	}
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlT})
	if !m.viewport.AtBottom() {
		t.Errorf("ctrl+t did not jump to bottom: offset=%d", m.viewport.YOffset)
	}
	// Footer hint uses keymap, not a hardcoded ctrl+end.
	m.reflow()
	footer := m.transcriptFooter()
	if footer != "" && strings.Contains(footer, "ctrl+end") {
		t.Errorf("footer still advertises ctrl+end: %q", footer)
	}
	if footer != "" && !strings.Contains(footer, "ctrl+t") {
		t.Errorf("footer missing ctrl+t: %q", footer)
	}
}

// TestNoOSCBackgroundJunkInComposerAfterSubmit covers #57 / #52.
func TestNoOSCBackgroundJunkInComposerAfterSubmit(t *testing.T) {
	const junk = "]11;rgb:0000/0000/0000\\"
	const fullOSC = "\x1b]11;rgb:0000/0000/0000\x07"
	const fullOSCST = "\x1b]11;rgb:0000/0000/0000\x1b\\"

	// WrapInput drops full OSC replies and leaked payloads.
	for _, wire := range []string{fullOSC, fullOSCST, junk + "ok", "pre" + fullOSC + "post"} {
		got, err := io.ReadAll(WrapInput(strings.NewReader(wire)))
		if err != nil {
			t.Fatalf("WrapInput: %v", err)
		}
		s := string(got)
		if strings.Contains(s, "rgb:0000") || strings.Contains(s, "]11;") {
			t.Errorf("WrapInput(%q) leaked OSC junk: %q", wire, s)
		}
	}

	// Byte-at-a-time leaked OSC reaches the composer as runes; strip there.
	mChunk, _ := newAppTestModel(nil, nil)
	mChunk = updateApp(t, mChunk, tea.WindowSizeMsg{Width: 80, Height: 24})
	for _, r := range junk {
		mChunk = updateApp(t, mChunk, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if strings.Contains(mChunk.composer.Value(), "rgb:0000") || strings.Contains(mChunk.composer.Value(), "]11;") {
		t.Errorf("composer after chunked OSC runes = %q", mChunk.composer.Value())
	}
	mChunk = updateApp(t, mChunk, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("ok")})
	if mChunk.composer.Value() != "ok" {
		t.Errorf("composer after chunked OSC + ok = %q, want ok", mChunk.composer.Value())
	}

	// Normal typing of "]11;notes" must not be eaten.
	if got := stripComposerOSCLeak("]11;notes"); got != "]11;notes" {
		t.Errorf("stripComposerOSCLeak(notes) = %q, want preserved", got)
	}
	got, err := io.ReadAll(WrapInput(strings.NewReader("]11;notes")))
	if err != nil {
		t.Fatalf("typing WrapInput: %v", err)
	}
	if string(got) != "]11;notes" {
		t.Errorf("typing WrapInput = %q, want preserved", got)
	}

	m, ops := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.providerName = "echo"
	m.modelName = "echo-1"
	m.composer.SetValue("hello world")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if cmd != nil {
		_ = runAllAppCmds(t, cmd)
	}
	// Drain any ops.
	select {
	case <-ops:
	default:
	}

	// Simulate late OSC reply after submit (the race that paints junk into the box).
	for _, leak := range []string{fullOSC, junk} {
		got, err := io.ReadAll(WrapInput(strings.NewReader(leak)))
		if err != nil {
			t.Fatalf("post-submit WrapInput: %v", err)
		}
		if len(got) > 0 {
			// If anything remains, feed as runes only when non-control text.
			if strings.Contains(string(got), "rgb:") || strings.Contains(string(got), "]11") {
				t.Fatalf("post-submit rewrite still has junk: %q", got)
			}
			m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(string(got))})
		}
	}

	if strings.Contains(m.composer.Value(), "rgb:0000") || strings.Contains(m.composer.Value(), "]11;") {
		t.Errorf("composer value has OSC junk: %q", m.composer.Value())
	}
	view := m.View()
	plain := ansi.Strip(view)
	if strings.Contains(plain, "rgb:0000") || strings.Contains(plain, "]11;rgb") {
		t.Errorf("rendered view has OSC junk: %q", plain)
	}
}

// TestMarkdownRenderDoesNotUseAutoStyle pins #52: WithAutoStyle re-queries OSC 11
// on every complete assistant cell and dumps the reply into the composer.
func TestMarkdownRenderDoesNotUseAutoStyle(t *testing.T) {
	savedMD := glamourStyleName
	savedDetected := appearanceDetected
	savedDetectedDark := appearanceDetectedDark
	savedDark := lipgloss.HasDarkBackground()
	t.Cleanup(func() {
		glamourStyleName = savedMD
		appearanceDetected = savedDetected
		appearanceDetectedDark = savedDetectedDark
		lipgloss.SetHasDarkBackground(savedDark)
	})

	PinAppearance()
	if !appearanceDetected {
		t.Fatal("PinAppearance did not mark appearance detected")
	}
	style := glamourStyle()
	if style != "dark" && style != "light" {
		t.Fatalf("glamourStyle = %q, want dark or light (never auto)", style)
	}

	// Rendering markdown must keep the pinned style (no "auto" path).
	out, err := glamourRender("# hi\n\n**bold**", 40)
	if err != nil {
		t.Fatalf("glamourRender: %v", err)
	}
	if out == "" {
		t.Fatal("glamourRender returned empty")
	}
	if glamourStyle() != style {
		t.Errorf("style changed during render: before %q after %q", style, glamourStyle())
	}
	if glamourStyleName == "auto" || glamourStyleName == "" {
		t.Errorf("glamourStyleName = %q after render, want pinned dark/light", glamourStyleName)
	}

	// Complete assistant cell after submit must not leave OSC junk in composer.
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.composer.SetValue("prompt")
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	_ = m.applyEvent(protocol.TextDelta{Text: "# Title\n\n- item"})
	_ = m.applyEvent(protocol.TurnCompleted{})
	_ = m.View() // forces markdown render on complete cell
	if strings.Contains(m.composer.Value(), "rgb:0000") || strings.Contains(m.composer.Value(), "]11;") {
		t.Errorf("composer after markdown view has OSC junk: %q", m.composer.Value())
	}
	plain := ansi.Strip(m.View())
	if strings.Contains(plain, "rgb:0000") || strings.Contains(plain, "]11;rgb") {
		t.Errorf("view after markdown has OSC junk: %q", plain)
	}
}
