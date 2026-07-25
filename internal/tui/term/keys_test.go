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
		{name: "esc", msg: tea.KeyMsg{Type: tea.KeyEsc}, want: []byte{0x1b}},
		{name: "up", msg: tea.KeyMsg{Type: tea.KeyUp}, want: []byte("\x1b[A")},
		{name: "ctrl+c", msg: tea.KeyMsg{Type: tea.KeyCtrlC}, want: []byte{0x03}},
		{name: "runes", msg: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}}, want: []byte("a")},
		{name: "backspace", msg: tea.KeyMsg{Type: tea.KeyBackspace}, want: []byte{0x7f}},
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
