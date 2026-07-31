package tui

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2/compat"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

// keyMsgFromWrapInput feeds terminal wire bytes through WrapInput and maps the
// legacy stream Bubble Tea would read to the KeyMsg it emits. Shift/Alt+Enter
// CSI rewrites to ESC+\r; BT's sequence table decodes that as KeyEnter+Alt
// (bubbletea key_sequences: "\x1b"+CR → Key{Type: KeyEnter, Alt: true}).
// Enhanced ctrl+j rewrites to ESC+j → KeyRunes{'j'}+Alt (#240).
func keyMsgFromWrapInput(t *testing.T, wire string) tea.KeyPressMsg {
	t.Helper()
	got, err := io.ReadAll(WrapInput(strings.NewReader(wire)))
	if err != nil {
		t.Fatalf("WrapInput(%q): %v", wire, err)
	}
	switch {
	case bytes.Equal(got, altEnter):
		return tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt}
	case bytes.Equal(got, altJ):
		return keyMsgAltJ()
	default:
		t.Fatalf("WrapInput(%q) = %q (%v), no KeyMsg mapping for this test", wire, got, got)
		return tea.KeyPressMsg{}
	}
}

// TestLeftFocusKeymapRoutingMatrix pins left-focus chords so UX fixes cannot
// silently rebind each other (#58, #414). Each case hits exactly one handler class.
func TestLeftFocusKeymapRoutingMatrix(t *testing.T) {
	type kind int
	const (
		kindSend kind = iota
		kindNewline
		kindCycleNext // ctrl+o cycles even on left focus (#414)
		kindCyclePrev // ctrl+p cycles prev (#414); not palette
		kindScrollUp
		kindScrollDown
		kindJumpBottom
		kindToggle
		kindFocusLeft
		kindFocusRight
		kindKillLineEnd // left-focus mid-line ctrl+k is readline kill, not palette
		kindPalette     // empty/EOL ctrl+k opens palette; tested via separate empty case
	)

	tests := []struct {
		name string
		msg  tea.KeyPressMsg
		want kind
	}{
		{"send enter", tea.KeyPressMsg{Code: tea.KeyEnter}, kindSend},
		{"newline alt+enter", tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt}, kindNewline},
		// Bare LF (Ubuntu ctrl+j) and enhanced alt+j both newline (#414).
		{"newline bare LF ctrl+j", tea.KeyPressMsg{Code: 'j', Mod: tea.ModCtrl}, kindNewline},
		{"newline enhanced ctrl+j", keyMsgAltJ(), kindNewline},
		{"cycle next ctrl+o", tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl}, kindCycleNext},
		{"cycle prev ctrl+p", tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl}, kindCyclePrev},
		{"scroll pgup", tea.KeyPressMsg{Code: tea.KeyPgUp}, kindScrollUp},
		{"scroll ctrl+up", tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModCtrl}, kindScrollUp},
		{"scroll pgdown", tea.KeyPressMsg{Code: tea.KeyPgDown}, kindScrollDown},
		{"scroll ctrl+down", tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModCtrl}, kindScrollDown},
		{"jump ctrl+t", tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl}, kindJumpBottom},
		{"toggle alt+;", tea.KeyPressMsg{Code: ';', Mod: tea.ModAlt}, kindToggle},
		{"focus left ctrl+h", tea.KeyPressMsg{Code: 'h', Mod: tea.ModCtrl}, kindFocusLeft},
		{"focus right ctrl+l", tea.KeyPressMsg{Code: 'l', Mod: tea.ModCtrl}, kindFocusRight},
		{"kill line end ctrl+k", tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl}, kindKillLineEnd},
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
			bottom := m.viewport.YOffset()

			startOrient := m.splitOrientation
			startFocus := m.focus
			startWin := m.windows.index
			m.composer.SetValue("hello world")
			m.composer.SetCursorColumn(5) // after "hello" so ctrl+k kills " world"
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
				if _, ok := m.modal.(*paletteModal); ok {
					t.Error("newline opened palette")
				}
				assertNoAppOp(t, ops)
			case kindCycleNext:
				if m.windows.index != startWin+1 {
					t.Errorf("cycle window = %d, want %d", m.windows.index, startWin+1)
				}
				if m.composer.Value() != composerBefore {
					t.Errorf("cycle changed composer to %q", m.composer.Value())
				}
				assertNoAppOp(t, ops)
			case kindCyclePrev:
				// startWin is 0; prev wraps to last window (index 1 with two windows).
				if m.windows.index != 1 {
					t.Errorf("cycle prev window = %d, want 1", m.windows.index)
				}
				if m.composer.Value() != composerBefore {
					t.Errorf("cycle prev changed composer to %q", m.composer.Value())
				}
				if _, ok := m.modal.(*paletteModal); ok {
					t.Error("ctrl+p opened palette")
				}
				assertNoAppOp(t, ops)
			case kindScrollUp:
				if m.viewport.YOffset() >= bottom {
					t.Errorf("scroll up offset=%d bottom=%d", m.viewport.YOffset(), bottom)
				}
				if m.composer.Value() != composerBefore {
					t.Errorf("scroll up changed composer to %q", m.composer.Value())
				}
			case kindScrollDown:
				m.viewport.GotoTop()
				m = updateApp(t, m, tt.msg)
				if m.viewport.YOffset() <= 0 {
					t.Errorf("scroll down offset=%d, want > 0", m.viewport.YOffset())
				}
				if m.composer.Value() != composerBefore {
					t.Errorf("scroll down changed composer to %q", m.composer.Value())
				}
			case kindJumpBottom:
				m.viewport.GotoTop()
				m = updateApp(t, m, tt.msg)
				if !m.viewport.AtBottom() {
					t.Errorf("jump bottom AtBottom=false offset=%d", m.viewport.YOffset())
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
					t.Error("mid-line ctrl+k opened palette")
				}
			case kindPalette:
				if _, ok := m.modal.(*paletteModal); !ok {
					t.Errorf("palette modal = %T", m.modal)
				}
			}
		})
	}

	// Empty composer: ctrl+k opens palette (kill does not claim) (#414).
	t.Run("palette empty ctrl+k", func(t *testing.T) {
		m, _ := newAppTestModel(nil, nil)
		m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
		m.composer.SetValue("")
		m = updateApp(t, m, tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl})
		if _, ok := m.modal.(*paletteModal); !ok {
			t.Fatalf("empty ctrl+k modal = %T, want palette", m.modal)
		}
	})
}

// TestShiftEnterNewlineWithoutPaneCycle covers the full WrapInput CSI → KeyMsg
// → Model.Update path for shift/alt+enter (#53, #187). Composer must gain a real
// newline, no UserInput op is sent, and CycleWindow* must not fire under either
// split orientation.
func TestShiftEnterNewlineWithoutPaneCycle(t *testing.T) {
	wires := []struct {
		name string
		wire string
	}{
		{"kitty shift+enter", "\x1b[13;2u"},
		{"xterm shift+enter", "\x1b[27;2;13~"},
		{"kitty alt+enter", "\x1b[13;3u"},
		{"xterm alt+enter", "\x1b[27;3;13~"},
		// Shift+Alt+Enter (mods=4 → bits shift|alt) also rewrites to alt+enter.
		{"kitty shift+alt+enter", "\x1b[13;4u"},
		{"xterm shift+alt+enter", "\x1b[27;4;13~"},
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
				msg := keyMsgFromWrapInput(t, tt.wire)

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
				m.composer.SetCursorColumn(len("line1"))

				if !key.Matches(msg, m.keyMap.Newline) {
					t.Fatalf("msg %v does not match Newline", msg)
				}
				if key.Matches(msg, m.keyMap.Send) {
					t.Fatalf("msg %v matched Send", msg)
				}
				if key.Matches(msg, m.keyMap.CycleWindowNext, m.keyMap.CycleWindowPrev) {
					t.Fatalf("post-WrapInput msg %v matched CycleWindow*", msg)
				}
				if key.Matches(msg, m.keyMap.FocusLeft, m.keyMap.FocusRight) {
					t.Fatalf("post-WrapInput msg %v matched Focus*", msg)
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

// TestUbuntuBareLFCtrlJNewlinesNotCycle pins the Ubuntu case: terminals send
// bare LF for ctrl+j (Bubble Tea KeyCtrlJ). That must insert newline, never
// cycle panes; Enter still sends; shift+enter CSI still newlines (#414).
func TestUbuntuBareLFCtrlJNewlinesNotCycle(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m.providerName = "echo"
	m.modelName = "echo-1"
	m.windows = windowRegistry{windows: []window{
		statefulTestWindow{windowID: "a", windowTitle: "A"},
		statefulTestWindow{windowID: "b", windowTitle: "B"},
	}}
	m.composer.SetValue("keep")
	m.composer.SetCursorColumn(len("keep"))

	// Left-focus bare LF → newline, no cycle.
	if !key.Matches(tea.KeyPressMsg{Code: 'j', Mod: tea.ModCtrl}, m.keyMap.Newline) {
		t.Fatal("KeyCtrlJ must match Newline")
	}
	if key.Matches(tea.KeyPressMsg{Code: 'j', Mod: tea.ModCtrl}, m.keyMap.CycleWindowNext) {
		t.Fatal("KeyCtrlJ must not match CycleWindowNext")
	}
	m = updateApp(t, m, tea.KeyPressMsg{Code: 'j', Mod: tea.ModCtrl})
	if got := m.composer.Value(); got != "keep\n" {
		t.Fatalf("bare LF composer = %q, want keep\\n", got)
	}
	if m.windows.index != 0 || m.windows.active().id() != "a" {
		t.Fatalf("bare LF window = %d/%s, want 0/a", m.windows.index, m.windows.active().id())
	}

	// ctrl+o still cycles from left.
	m = updateApp(t, m, tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	if m.windows.index != 1 || m.windows.active().id() != "b" {
		t.Fatalf("ctrl+o window = %d/%s, want 1/b", m.windows.index, m.windows.active().id())
	}

	// Enter still sends.
	m.focus = focusLeft
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	if m.composer.Value() != "" {
		t.Errorf("enter left composer = %q, want empty", m.composer.Value())
	}
	if cmd != nil {
		_ = runAppCmd(t, cmd)
	}
	select {
	case op := <-ops:
		if _, ok := op.(protocol.UserInput); !ok {
			t.Errorf("enter op = %T, want UserInput", op)
		}
	default:
	}

	// Shift+enter CSI still newlines.
	m.composer.SetValue("line")
	m.composer.SetCursorColumn(len("line"))
	m = updateApp(t, m, keyMsgFromWrapInput(t, "\x1b[13;2u"))
	if got := m.composer.Value(); got != "line\n" {
		t.Errorf("shift+enter composer = %q, want line\\n", got)
	}
	assertNoAppOp(t, ops)
}

// TestCtrlOPCyclesWindows pins ctrl+o / ctrl+p cycle from either focus (#414).
func TestCtrlOPCyclesWindows(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m.windows = windowRegistry{windows: []window{
		statefulTestWindow{windowID: "a", windowTitle: "A"},
		statefulTestWindow{windowID: "b", windowTitle: "B"},
	}}
	m.composer.SetValue("keep")
	m = updateApp(t, m, tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	if got := m.composer.Value(); got != "keep" {
		t.Fatalf("ctrl+o composer = %q, want keep", got)
	}
	if m.windows.index != 1 || m.windows.active().id() != "b" {
		t.Fatalf("ctrl+o window = %d/%s, want 1/b", m.windows.index, m.windows.active().id())
	}
	m = updateApp(t, m, tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	if m.windows.index != 0 || m.windows.active().id() != "a" {
		t.Fatalf("ctrl+p window = %d/%s, want 0/a", m.windows.index, m.windows.active().id())
	}
	m = updateApp(t, m, tea.KeyPressMsg{Code: 'l', Mod: tea.ModCtrl})
	if m.focus != focusRight {
		t.Fatalf("focus = %v, want right", m.focus)
	}
	m = updateApp(t, m, tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	if m.windows.index != 1 || m.windows.active().id() != "b" {
		t.Errorf("right ctrl+o window = %d/%s, want 1/b", m.windows.index, m.windows.active().id())
	}
	if got := m.composer.Value(); got != "keep" {
		t.Errorf("right ctrl+o changed composer to %q", got)
	}
	assertNoAppOp(t, ops)
}

// TestEnhancedCtrlJWireNewlines covers WrapInput CSI → KeyMsg → Update for
// real ctrl+j sequences under both orientations (#414).
func TestEnhancedCtrlJWireNewlines(t *testing.T) {
	wires := []string{
		"\x1b[106;5u",    // Kitty ctrl+j
		"\x1b[27;5;106~", // xterm ctrl+j
		"\x1b[74;5u",     // Kitty Ctrl+J uppercase
	}
	for _, orient := range []splitOrientation{orientHorizontal, orientVertical} {
		for _, wire := range wires {
			name := "horizontal"
			if orient == orientVertical {
				name = "vertical"
			}
			t.Run(name+"/"+wire, func(t *testing.T) {
				msg := keyMsgFromWrapInput(t, wire)
				m, ops := newAppTestModel(nil, nil)
				m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
				if orient == orientVertical {
					m.toggleOrientation()
				}
				m.windows = windowRegistry{windows: []window{
					statefulTestWindow{windowID: "a", windowTitle: "A"},
					statefulTestWindow{windowID: "b", windowTitle: "B"},
				}}
				m.composer.SetValue("draft")
				m.composer.SetCursorColumn(len("draft"))
				startWin := m.windows.index
				startFocus := m.focus

				if !key.Matches(msg, m.keyMap.Newline) {
					t.Fatal("enhanced ctrl+j must match Newline")
				}
				if key.Matches(msg, m.keyMap.Send) {
					t.Fatal("enhanced ctrl+j must not match Send")
				}
				if key.Matches(msg, m.keyMap.CycleWindowNext, m.keyMap.FocusLeft) {
					t.Fatal("enhanced ctrl+j must not match cycle/focus")
				}

				m = updateApp(t, m, msg)
				if got := m.composer.Value(); got != "draft\n" {
					t.Errorf("composer = %q, want draft\\n", got)
				}
				if m.windows.index != startWin {
					t.Errorf("window = %d, want %d", m.windows.index, startWin)
				}
				if m.focus != startFocus {
					t.Errorf("focus changed to %v", m.focus)
				}
				assertNoAppOp(t, ops)
			})
		}
	}
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
	msg := tea.KeyPressMsg{Code: ';', Mod: tea.ModAlt}
	if !key.Matches(msg, m.keyMap.ToggleOrientation) {
		t.Fatal("alt+; does not match ToggleOrientation")
	}
	m = updateApp(t, m, msg)
	if m.splitOrientation != orientVertical {
		t.Fatalf("after ctrl+; orientation = %v, want vertical", m.splitOrientation)
	}
	// Orientation-independent chords: focus stays h/l, cycle stays o/p (#414).
	if !key.Matches(tea.KeyPressMsg{Code: 'h', Mod: tea.ModCtrl}, m.keyMap.FocusLeft) {
		t.Error("vertical: ctrl+h should still focus left/primary")
	}
	if !key.Matches(tea.KeyPressMsg{Code: 'l', Mod: tea.ModCtrl}, m.keyMap.FocusRight) {
		t.Error("vertical: ctrl+l should still focus right/secondary")
	}
	if !key.Matches(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl}, m.keyMap.CycleWindowNext) {
		t.Error("vertical: ctrl+o should still cycle next")
	}
	if !key.Matches(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl}, m.keyMap.CycleWindowPrev) {
		t.Error("vertical: ctrl+p should still cycle prev")
	}
	if !key.Matches(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl | tea.ModShift}, m.keyMap.CycleGroupNext) {
		t.Error("vertical: ctrl+shift+o should still cycle group next")
	}
	if !key.Matches(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl | tea.ModShift}, m.keyMap.CycleGroupPrev) {
		t.Error("vertical: ctrl+shift+p should still cycle group prev")
	}
	if key.Matches(tea.KeyPressMsg{Code: 'j', Mod: tea.ModCtrl}, m.keyMap.FocusLeft, m.keyMap.CycleWindowNext) {
		t.Error("vertical: ctrl+j must remain newline-only")
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
	bottom := m.viewport.YOffset()
	if bottom == 0 {
		t.Fatal("setup: expected scrollable transcript")
	}
	m.composer.SetValue("keep-me")

	// Left-focus keyboard scroll.
	for _, msg := range []tea.KeyPressMsg{{Code: tea.KeyPgUp}} {
		m.viewport.GotoBottom()
		before := m.composer.Value()
		m = updateApp(t, m, msg)
		if m.viewport.YOffset() >= bottom {
			t.Errorf("%v did not scroll transcript up", msg)
		}
		if m.composer.Value() != before {
			t.Errorf("%v changed composer to %q", msg, m.composer.Value())
		}
	}
	m.viewport.GotoTop()
	top := m.viewport.YOffset()
	for _, msg := range []tea.KeyPressMsg{{Code: tea.KeyPgDown}} {
		m.viewport.GotoTop()
		m = updateApp(t, m, msg)
		if m.viewport.YOffset() <= top {
			t.Errorf("%v did not scroll transcript down", msg)
		}
		if m.composer.Value() != "keep-me" {
			t.Errorf("%v changed composer to %q", msg, m.composer.Value())
		}
	}

	// Right-focus: scroll still moves transcript, not only the terminal pane.
	m = updateApp(t, m, tea.KeyPressMsg{Code: 'l', Mod: tea.ModCtrl})
	if m.focus != focusRight {
		t.Fatalf("focus = %v, want right", m.focus)
	}
	m.viewport.GotoBottom()
	m = updateApp(t, m, tea.KeyPressMsg{Code: tea.KeyPgUp})
	if m.viewport.YOffset() >= bottom {
		t.Error("pgup with right focus did not scroll transcript")
	}
	if m.composer.Value() != "keep-me" {
		t.Errorf("right-focus scroll changed composer to %q", m.composer.Value())
	}

	// Mouse wheel → transcript.
	m.viewport.GotoBottom()
	m = updateApp(t, m, tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	if m.viewport.YOffset() >= bottom {
		t.Error("wheel up did not scroll transcript")
	}
	afterWheelUp := m.viewport.YOffset()
	m = updateApp(t, m, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if m.viewport.YOffset() <= afterWheelUp {
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
	if !key.Matches(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl}, keys.JumpBottom) {
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
	m = updateApp(t, m, tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	if !m.viewport.AtBottom() {
		t.Errorf("ctrl+t did not jump to bottom: offset=%d", m.viewport.YOffset())
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

// TestNoSGRMouseJunkInComposer covers #484: scroll-wheel SGR bodies must not
// type into the prompt when ESC was consumed separately.
func TestNoSGRMouseJunkInComposer(t *testing.T) {
	const junk = "[<64;56;36M"
	const full = "\x1b[<64;56;36M"
	const wheelDown = "[<65;62;26M"

	for _, wire := range []string{junk, wheelDown, junk + "ok", "pre" + full + "post"} {
		got, err := io.ReadAll(WrapInput(strings.NewReader(wire)))
		if err != nil {
			t.Fatalf("WrapInput: %v", err)
		}
		s := string(got)
		// Bodies without ESC are re-prefixed; nothing should remain as bare "[<"
		// junk ready to type. Full CSI is fine (Bubble Tea → MouseMsg).
		if strings.Contains(s, "[<") && !strings.Contains(s, "\x1b[<") {
			t.Errorf("WrapInput(%q) left bare mouse body: %q", wire, s)
		}
	}
	got, err := io.ReadAll(WrapInput(strings.NewReader(junk + "ok")))
	if err != nil {
		t.Fatalf("WrapInput junk+ok: %v", err)
	}
	if !strings.HasSuffix(string(got), "ok") {
		t.Errorf("WrapInput(junk+ok) = %q, want suffix ok", got)
	}

	// Byte-at-a-time leaked mouse reaches the composer as runes; strip there.
	mChunk, _ := newAppTestModel(nil, nil)
	mChunk = updateApp(t, mChunk, tea.WindowSizeMsg{Width: 80, Height: 24})
	for _, r := range junk {
		mChunk = updateApp(t, mChunk, tea.KeyPressMsg{Text: string([]rune{r})})
	}
	if strings.Contains(mChunk.composer.Value(), "[<") || strings.Contains(mChunk.composer.Value(), "64;56") {
		t.Errorf("composer after chunked mouse runes = %q", mChunk.composer.Value())
	}
	mChunk = updateApp(t, mChunk, tea.KeyPressMsg{Text: "ok"})
	if mChunk.composer.Value() != "ok" {
		t.Errorf("composer after chunked mouse + ok = %q, want ok", mChunk.composer.Value())
	}

	// Normal typing of "[notes" must not be eaten.
	if got := stripComposerMouseLeak("[notes]"); got != "[notes]" {
		t.Errorf("stripComposerMouseLeak(notes) = %q, want preserved", got)
	}
	got, err = io.ReadAll(WrapInput(strings.NewReader("[notes]")))
	if err != nil {
		t.Fatalf("typing WrapInput: %v", err)
	}
	if string(got) != "[notes]" {
		t.Errorf("typing WrapInput = %q, want preserved", got)
	}

	// Wheel MouseMsg still scrolls the transcript (proper action path).
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	for i := range 40 {
		m.applyEvent(protocol.UserMessage{Text: strings.Repeat("scroll-line ", 8) + string(rune('a'+i%26))})
	}
	m.refreshViewport()
	m.viewport.GotoBottom()
	bottom := m.viewport.YOffset()
	if bottom == 0 {
		t.Fatal("setup: expected scrollable transcript")
	}
	m = updateApp(t, m, tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	if m.viewport.YOffset() >= bottom {
		t.Fatalf("wheel up did not scroll: offset=%d bottom=%d", m.viewport.YOffset(), bottom)
	}
	if m.composer.Value() != "" {
		t.Errorf("composer after wheel = %q, want empty", m.composer.Value())
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
		mChunk = updateApp(t, mChunk, tea.KeyPressMsg{Text: string([]rune{r})})
	}
	if strings.Contains(mChunk.composer.Value(), "rgb:0000") || strings.Contains(mChunk.composer.Value(), "]11;") {
		t.Errorf("composer after chunked OSC runes = %q", mChunk.composer.Value())
	}
	mChunk = updateApp(t, mChunk, tea.KeyPressMsg{Text: "ok"})
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
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
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
			m = updateApp(t, m, tea.KeyPressMsg{Text: string(got)})
		}
	}

	if strings.Contains(m.composer.Value(), "rgb:0000") || strings.Contains(m.composer.Value(), "]11;") {
		t.Errorf("composer value has OSC junk: %q", m.composer.Value())
	}
	view := viewString(m)
	plain := ansi.Strip(view)
	if strings.Contains(plain, "rgb:0000") || strings.Contains(plain, "]11;rgb") {
		t.Errorf("rendered view has OSC junk: %q", plain)
	}
}

// TestMarkdownRenderDoesNotUseAutoStyle pins #52: WithAutoStyle re-queries OSC 11
// on every complete assistant cell and dumps the reply into the composer.
func TestMarkdownRenderDoesNotUseAutoStyle(t *testing.T) {
	savedMD := glamourStyleName
	savedDark := compat.HasDarkBackground
	t.Cleanup(func() {
		glamourStyleName = savedMD
		compat.HasDarkBackground = savedDark
	})

	// Pin glamour to a concrete dark/light style (never "auto") as applyAppearance does.
	setGlamourStyle(true)
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
	m = updateApp(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	_ = m.applyEvent(protocol.TextDelta{Text: "# Title\n\n- item"})
	_ = m.applyEvent(protocol.TurnCompleted{})
	_ = viewString(m) // forces markdown render on complete cell
	if strings.Contains(m.composer.Value(), "rgb:0000") || strings.Contains(m.composer.Value(), "]11;") {
		t.Errorf("composer after markdown view has OSC junk: %q", m.composer.Value())
	}
	plain := ansi.Strip(viewString(m))
	if strings.Contains(plain, "rgb:0000") || strings.Contains(plain, "]11;rgb") {
		t.Errorf("view after markdown has OSC junk: %q", plain)
	}
}
