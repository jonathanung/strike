package term

import (
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
)

// EncodeKey maps a Bubble Tea key message to bytes for a PTY running under
// xterm-style key reporting. Unknown keys yield nil.
func EncodeKey(msg tea.KeyPressMsg) []byte {
	// Printable text first (including space when Text is set).
	textMod := msg.Mod &^ (tea.ModCapsLock | tea.ModNumLock | tea.ModScrollLock)
	if len(msg.Text) > 0 && (textMod == 0 || textMod == tea.ModShift) {
		out := make([]byte, 0, len(msg.Text)*utf8.UTFMax)
		for _, r := range msg.Text {
			buf := make([]byte, utf8.UTFMax)
			n := utf8.EncodeRune(buf, r)
			out = append(out, buf[:n]...)
		}
		return out
	}
	if len(msg.Text) > 0 && msg.Mod.Contains(tea.ModAlt) && !msg.Mod.Contains(tea.ModCtrl) {
		out := make([]byte, 0, 1+len(msg.Text)*utf8.UTFMax)
		for _, r := range msg.Text {
			out = append(out, 0x1b)
			buf := make([]byte, utf8.UTFMax)
			n := utf8.EncodeRune(buf, r)
			out = append(out, buf[:n]...)
		}
		return out
	}

	// Ctrl+letter (and a few specials) via Code+Mod.
	if msg.Mod.Contains(tea.ModCtrl) && !msg.Mod.Contains(tea.ModAlt) {
		switch msg.Code {
		case tea.KeyUp:
			return []byte("\x1b[1;5A")
		case tea.KeyDown:
			return []byte("\x1b[1;5B")
		case tea.KeyLeft:
			return []byte("\x1b[1;5D")
		case tea.KeyRight:
			return []byte("\x1b[1;5C")
		case 'a', 'A':
			return []byte{0x01}
		case 'b', 'B':
			return []byte{0x02}
		case 'c', 'C':
			return []byte{0x03}
		case 'd', 'D':
			return []byte{0x04}
		case 'e', 'E':
			return []byte{0x05}
		case 'f', 'F':
			return []byte{0x06}
		case 'g', 'G':
			return []byte{0x07}
		case 'h', 'H':
			return []byte{0x08}
		case 'j', 'J':
			return []byte{0x0a}
		case 'k', 'K':
			return []byte{0x0b}
		case 'l', 'L':
			return []byte{0x0c}
		case 'n', 'N':
			return []byte{0x0e}
		case 'o', 'O':
			return []byte{0x0f}
		case 'p', 'P':
			return []byte{0x10}
		case 'q', 'Q':
			return []byte{0x11}
		case 'r', 'R':
			return []byte{0x12}
		case 's', 'S':
			return []byte{0x13}
		case 't', 'T':
			return []byte{0x14}
		case 'u', 'U':
			return []byte{0x15}
		case 'v', 'V':
			return []byte{0x16}
		case 'w', 'W':
			return []byte{0x17}
		case 'x', 'X':
			return []byte{0x18}
		case 'y', 'Y':
			return []byte{0x19}
		case 'z', 'Z':
			return []byte{0x1a}
		case '\\':
			return []byte{0x1c}
		}
	}

	// Shift+Tab
	if msg.Code == tea.KeyTab && msg.Mod.Contains(tea.ModShift) {
		return []byte("\x1b[Z")
	}

	switch msg.Code {
	case tea.KeyEnter:
		return []byte{'\r'}
	case tea.KeyTab:
		return []byte{'\t'}
	case tea.KeySpace:
		return []byte{' '}
	case tea.KeyBackspace:
		return []byte{0x7f}
	case tea.KeyDelete:
		return []byte("\x1b[3~")
	case tea.KeyEsc:
		return []byte{0x1b}
	case tea.KeyUp:
		return []byte("\x1b[A")
	case tea.KeyDown:
		return []byte("\x1b[B")
	case tea.KeyRight:
		return []byte("\x1b[C")
	case tea.KeyLeft:
		return []byte("\x1b[D")
	case tea.KeyHome:
		return []byte("\x1b[H")
	case tea.KeyEnd:
		return []byte("\x1b[F")
	case tea.KeyPgUp:
		return []byte("\x1b[5~")
	case tea.KeyPgDown:
		return []byte("\x1b[6~")
	case tea.KeyF1:
		return []byte("\x1bOP")
	case tea.KeyF2:
		return []byte("\x1bOQ")
	case tea.KeyF3:
		return []byte("\x1bOR")
	case tea.KeyF4:
		return []byte("\x1bOS")
	case tea.KeyF5:
		return []byte("\x1b[15~")
	case tea.KeyF6:
		return []byte("\x1b[17~")
	case tea.KeyF7:
		return []byte("\x1b[18~")
	case tea.KeyF8:
		return []byte("\x1b[19~")
	case tea.KeyF9:
		return []byte("\x1b[20~")
	case tea.KeyF10:
		return []byte("\x1b[21~")
	case tea.KeyF11:
		return []byte("\x1b[23~")
	case tea.KeyF12:
		return []byte("\x1b[24~")
	default:
		return nil
	}
}
