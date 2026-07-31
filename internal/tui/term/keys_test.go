package term

import (
	"bytes"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestEncodeKey(t *testing.T) {
	tests := []struct {
		name string
		msg  tea.KeyPressMsg
		want []byte
	}{
		{name: "enter", msg: tea.KeyPressMsg{Code: tea.KeyEnter}, want: []byte{'\r'}},
		{name: "tab", msg: tea.KeyPressMsg{Code: tea.KeyTab}, want: []byte{'\t'}},
		{name: "shift tab", msg: tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}, want: []byte("\x1b[Z")},
		{name: "space", msg: tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}, want: []byte{' '}},
		{name: "backspace", msg: tea.KeyPressMsg{Code: tea.KeyBackspace}, want: []byte{0x7f}},
		{name: "delete", msg: tea.KeyPressMsg{Code: tea.KeyDelete}, want: []byte("\x1b[3~")},
		{name: "esc", msg: tea.KeyPressMsg{Code: tea.KeyEsc}, want: []byte{0x1b}},
		{name: "up", msg: tea.KeyPressMsg{Code: tea.KeyUp}, want: []byte("\x1b[A")},
		{name: "down", msg: tea.KeyPressMsg{Code: tea.KeyDown}, want: []byte("\x1b[B")},
		{name: "ctrl up", msg: tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModCtrl}, want: []byte("\x1b[1;5A")},
		{name: "ctrl down", msg: tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModCtrl}, want: []byte("\x1b[1;5B")},
		{name: "right", msg: tea.KeyPressMsg{Code: tea.KeyRight}, want: []byte("\x1b[C")},
		{name: "left", msg: tea.KeyPressMsg{Code: tea.KeyLeft}, want: []byte("\x1b[D")},
		{name: "home", msg: tea.KeyPressMsg{Code: tea.KeyHome}, want: []byte("\x1b[H")},
		{name: "end", msg: tea.KeyPressMsg{Code: tea.KeyEnd}, want: []byte("\x1b[F")},
		{name: "pgup", msg: tea.KeyPressMsg{Code: tea.KeyPgUp}, want: []byte("\x1b[5~")},
		{name: "pgdown", msg: tea.KeyPressMsg{Code: tea.KeyPgDown}, want: []byte("\x1b[6~")},
		{name: "ctrl+a", msg: tea.KeyPressMsg{Code: 'a', Mod: tea.ModCtrl}, want: []byte{0x01}},
		{name: "ctrl+b", msg: tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl}, want: []byte{0x02}},
		{name: "ctrl+c", msg: tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}, want: []byte{0x03}},
		{name: "ctrl+d", msg: tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl}, want: []byte{0x04}},
		{name: "ctrl+e", msg: tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl}, want: []byte{0x05}},
		{name: "ctrl+f", msg: tea.KeyPressMsg{Code: 'f', Mod: tea.ModCtrl}, want: []byte{0x06}},
		{name: "ctrl+g", msg: tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl}, want: []byte{0x07}},
		{name: "ctrl+h", msg: tea.KeyPressMsg{Code: 'h', Mod: tea.ModCtrl}, want: []byte{0x08}},
		{name: "ctrl+j", msg: tea.KeyPressMsg{Code: 'j', Mod: tea.ModCtrl}, want: []byte{0x0a}},
		{name: "ctrl+k", msg: tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl}, want: []byte{0x0b}},
		{name: "ctrl+l", msg: tea.KeyPressMsg{Code: 'l', Mod: tea.ModCtrl}, want: []byte{0x0c}},
		{name: "ctrl+n", msg: tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl}, want: []byte{0x0e}},
		{name: "ctrl+o", msg: tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl}, want: []byte{0x0f}},
		{name: "ctrl+p", msg: tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl}, want: []byte{0x10}},
		{name: "ctrl+q", msg: tea.KeyPressMsg{Code: 'q', Mod: tea.ModCtrl}, want: []byte{0x11}},
		{name: "ctrl+r", msg: tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl}, want: []byte{0x12}},
		{name: "ctrl+s", msg: tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl}, want: []byte{0x13}},
		{name: "ctrl+t", msg: tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl}, want: []byte{0x14}},
		{name: "ctrl+u", msg: tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl}, want: []byte{0x15}},
		{name: "ctrl+v", msg: tea.KeyPressMsg{Code: 'v', Mod: tea.ModCtrl}, want: []byte{0x16}},
		{name: "ctrl+w", msg: tea.KeyPressMsg{Code: 'w', Mod: tea.ModCtrl}, want: []byte{0x17}},
		{name: "ctrl+x", msg: tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl}, want: []byte{0x18}},
		{name: "ctrl+y", msg: tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl}, want: []byte{0x19}},
		{name: "ctrl+z", msg: tea.KeyPressMsg{Code: 'z', Mod: tea.ModCtrl}, want: []byte{0x1a}},
		{name: "ctrl+backslash", msg: tea.KeyPressMsg{Code: '\\', Mod: tea.ModCtrl}, want: []byte{0x1c}},
		{name: "f1", msg: tea.KeyPressMsg{Code: tea.KeyF1}, want: []byte("\x1bOP")},
		{name: "f2", msg: tea.KeyPressMsg{Code: tea.KeyF2}, want: []byte("\x1bOQ")},
		{name: "f3", msg: tea.KeyPressMsg{Code: tea.KeyF3}, want: []byte("\x1bOR")},
		{name: "f4", msg: tea.KeyPressMsg{Code: tea.KeyF4}, want: []byte("\x1bOS")},
		{name: "f5", msg: tea.KeyPressMsg{Code: tea.KeyF5}, want: []byte("\x1b[15~")},
		{name: "f6", msg: tea.KeyPressMsg{Code: tea.KeyF6}, want: []byte("\x1b[17~")},
		{name: "f7", msg: tea.KeyPressMsg{Code: tea.KeyF7}, want: []byte("\x1b[18~")},
		{name: "f8", msg: tea.KeyPressMsg{Code: tea.KeyF8}, want: []byte("\x1b[19~")},
		{name: "f9", msg: tea.KeyPressMsg{Code: tea.KeyF9}, want: []byte("\x1b[20~")},
		{name: "f10", msg: tea.KeyPressMsg{Code: tea.KeyF10}, want: []byte("\x1b[21~")},
		{name: "f11", msg: tea.KeyPressMsg{Code: tea.KeyF11}, want: []byte("\x1b[23~")},
		{name: "f12", msg: tea.KeyPressMsg{Code: tea.KeyF12}, want: []byte("\x1b[24~")},
		{name: "runes", msg: tea.KeyPressMsg{Code: 'a', Text: "a"}, want: []byte("a")},
		{name: "runes multi", msg: tea.KeyPressMsg{Text: "hi"}, want: []byte("hi")},
		{name: "runes utf8", msg: tea.KeyPressMsg{Text: "界"}, want: []byte("界")},
		{name: "shift letter", msg: tea.KeyPressMsg{Code: 'A', Text: "A", Mod: tea.ModShift}, want: []byte("A")},
		{name: "shift punctuation", msg: tea.KeyPressMsg{Code: '!', Text: "!", Mod: tea.ModShift}, want: []byte("!")},
		{name: "shift utf8", msg: tea.KeyPressMsg{Text: "界", Mod: tea.ModShift}, want: []byte("界")},
		{name: "alt rune", msg: tea.KeyPressMsg{Code: 'x', Text: "x", Mod: tea.ModAlt}, want: []byte{0x1b, 'x'}},
		{name: "empty runes", msg: tea.KeyPressMsg{}, want: nil},
		{name: "unknown", msg: tea.KeyPressMsg{Code: '@', Mod: tea.ModCtrl}, want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EncodeKey(tt.msg)
			if !bytes.Equal(got, tt.want) {
				t.Fatalf("EncodeKey(%#v) = %q, want %q", tt.msg, got, tt.want)
			}
		})
	}
}
