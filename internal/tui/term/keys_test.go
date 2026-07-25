package term

import (
	"bytes"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestEncodeKeyBasics(t *testing.T) {
	tests := []struct {
		name string
		msg  tea.KeyMsg
		want []byte
	}{
		{name: "enter", msg: tea.KeyMsg{Type: tea.KeyEnter}, want: []byte{'\r'}},
		{name: "tab", msg: tea.KeyMsg{Type: tea.KeyTab}, want: []byte{'\t'}},
		{name: "space", msg: tea.KeyMsg{Type: tea.KeySpace}, want: []byte{' '}},
		{name: "backspace", msg: tea.KeyMsg{Type: tea.KeyBackspace}, want: []byte{0x7f}},
		{name: "delete", msg: tea.KeyMsg{Type: tea.KeyDelete}, want: []byte("\x1b[3~")},
		{name: "esc", msg: tea.KeyMsg{Type: tea.KeyEsc}, want: []byte{0x1b}},
		{name: "up", msg: tea.KeyMsg{Type: tea.KeyUp}, want: []byte("\x1b[A")},
		{name: "down", msg: tea.KeyMsg{Type: tea.KeyDown}, want: []byte("\x1b[B")},
		{name: "right", msg: tea.KeyMsg{Type: tea.KeyRight}, want: []byte("\x1b[C")},
		{name: "left", msg: tea.KeyMsg{Type: tea.KeyLeft}, want: []byte("\x1b[D")},
		{name: "home", msg: tea.KeyMsg{Type: tea.KeyHome}, want: []byte("\x1b[H")},
		{name: "end", msg: tea.KeyMsg{Type: tea.KeyEnd}, want: []byte("\x1b[F")},
		{name: "pgup", msg: tea.KeyMsg{Type: tea.KeyPgUp}, want: []byte("\x1b[5~")},
		{name: "pgdown", msg: tea.KeyMsg{Type: tea.KeyPgDown}, want: []byte("\x1b[6~")},
		{name: "ctrl+a", msg: tea.KeyMsg{Type: tea.KeyCtrlA}, want: []byte{0x01}},
		{name: "ctrl+b", msg: tea.KeyMsg{Type: tea.KeyCtrlB}, want: []byte{0x02}},
		{name: "ctrl+c", msg: tea.KeyMsg{Type: tea.KeyCtrlC}, want: []byte{0x03}},
		{name: "ctrl+d", msg: tea.KeyMsg{Type: tea.KeyCtrlD}, want: []byte{0x04}},
		{name: "ctrl+e", msg: tea.KeyMsg{Type: tea.KeyCtrlE}, want: []byte{0x05}},
		{name: "ctrl+f", msg: tea.KeyMsg{Type: tea.KeyCtrlF}, want: []byte{0x06}},
		{name: "ctrl+g", msg: tea.KeyMsg{Type: tea.KeyCtrlG}, want: []byte{0x07}},
		{name: "ctrl+h", msg: tea.KeyMsg{Type: tea.KeyCtrlH}, want: []byte{0x08}},
		{name: "ctrl+j", msg: tea.KeyMsg{Type: tea.KeyCtrlJ}, want: []byte{0x0a}},
		{name: "ctrl+k", msg: tea.KeyMsg{Type: tea.KeyCtrlK}, want: []byte{0x0b}},
		{name: "ctrl+l", msg: tea.KeyMsg{Type: tea.KeyCtrlL}, want: []byte{0x0c}},
		{name: "ctrl+n", msg: tea.KeyMsg{Type: tea.KeyCtrlN}, want: []byte{0x0e}},
		{name: "ctrl+o", msg: tea.KeyMsg{Type: tea.KeyCtrlO}, want: []byte{0x0f}},
		{name: "ctrl+p", msg: tea.KeyMsg{Type: tea.KeyCtrlP}, want: []byte{0x10}},
		{name: "ctrl+q", msg: tea.KeyMsg{Type: tea.KeyCtrlQ}, want: []byte{0x11}},
		{name: "ctrl+r", msg: tea.KeyMsg{Type: tea.KeyCtrlR}, want: []byte{0x12}},
		{name: "ctrl+s", msg: tea.KeyMsg{Type: tea.KeyCtrlS}, want: []byte{0x13}},
		{name: "ctrl+t", msg: tea.KeyMsg{Type: tea.KeyCtrlT}, want: []byte{0x14}},
		{name: "ctrl+u", msg: tea.KeyMsg{Type: tea.KeyCtrlU}, want: []byte{0x15}},
		{name: "ctrl+v", msg: tea.KeyMsg{Type: tea.KeyCtrlV}, want: []byte{0x16}},
		{name: "ctrl+w", msg: tea.KeyMsg{Type: tea.KeyCtrlW}, want: []byte{0x17}},
		{name: "ctrl+x", msg: tea.KeyMsg{Type: tea.KeyCtrlX}, want: []byte{0x18}},
		{name: "ctrl+y", msg: tea.KeyMsg{Type: tea.KeyCtrlY}, want: []byte{0x19}},
		{name: "ctrl+z", msg: tea.KeyMsg{Type: tea.KeyCtrlZ}, want: []byte{0x1a}},
		{name: "ctrl+backslash", msg: tea.KeyMsg{Type: tea.KeyCtrlBackslash}, want: []byte{0x1c}},
		{name: "f1", msg: tea.KeyMsg{Type: tea.KeyF1}, want: []byte("\x1bOP")},
		{name: "f2", msg: tea.KeyMsg{Type: tea.KeyF2}, want: []byte("\x1bOQ")},
		{name: "f3", msg: tea.KeyMsg{Type: tea.KeyF3}, want: []byte("\x1bOR")},
		{name: "f4", msg: tea.KeyMsg{Type: tea.KeyF4}, want: []byte("\x1bOS")},
		{name: "f5", msg: tea.KeyMsg{Type: tea.KeyF5}, want: []byte("\x1b[15~")},
		{name: "f6", msg: tea.KeyMsg{Type: tea.KeyF6}, want: []byte("\x1b[17~")},
		{name: "f7", msg: tea.KeyMsg{Type: tea.KeyF7}, want: []byte("\x1b[18~")},
		{name: "f8", msg: tea.KeyMsg{Type: tea.KeyF8}, want: []byte("\x1b[19~")},
		{name: "f9", msg: tea.KeyMsg{Type: tea.KeyF9}, want: []byte("\x1b[20~")},
		{name: "f10", msg: tea.KeyMsg{Type: tea.KeyF10}, want: []byte("\x1b[21~")},
		{name: "f11", msg: tea.KeyMsg{Type: tea.KeyF11}, want: []byte("\x1b[23~")},
		{name: "f12", msg: tea.KeyMsg{Type: tea.KeyF12}, want: []byte("\x1b[24~")},
		{name: "runes", msg: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}}, want: []byte("a")},
		{name: "runes multi", msg: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h', 'i'}}, want: []byte("hi")},
		{name: "runes utf8", msg: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'界'}}, want: []byte("界")},
		{name: "alt rune", msg: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}, Alt: true}, want: []byte{0x1b, 'x'}},
		{name: "empty runes", msg: tea.KeyMsg{Type: tea.KeyRunes}, want: nil},
		{name: "unknown", msg: tea.KeyMsg{Type: tea.KeyCtrlAt}, want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EncodeKey(tt.msg)
			if !bytes.Equal(got, tt.want) {
				t.Fatalf("EncodeKey = %v, want %v", got, tt.want)
			}
		})
	}
}
