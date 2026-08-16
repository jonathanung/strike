package common

import (
	"strings"
	"unicode/utf8"
)

// PadWideGlyphs inserts a trailing ASCII space after runes that common terminal
// fonts paint double-wide despite Unicode East Asian Width Neutral (measured
// width 1 by go-runewidth / ansi / lipgloss). The reserved cell absorbs glyph
// overflow so multi-column TUI layouts stay aligned (#689).
//
// Idempotent when a space already follows the rune. Safe on ANSI-styled strings:
// escape sequences are ASCII and are left untouched. Copy/export paths should
// keep the original text; call this only at render time.
func PadWideGlyphs(s string) string {
	if s == "" || !containsOverflowingNeutral(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		b.WriteString(s[i : i+size])
		i += size
		if !isOverflowingNeutral(r) {
			continue
		}
		if i < len(s) && s[i] == ' ' {
			continue
		}
		b.WriteByte(' ')
	}
	return b.String()
}

// containsOverflowingNeutral reports whether s has any rune in a historic
// script block that terminals often render wider than its measured width.
func containsOverflowingNeutral(s string) bool {
	for i := 0; i < len(s); {
		// ASCII (including ANSI CSI/OSC) cannot be in the target blocks.
		if s[i] < 0x80 {
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if size <= 0 {
			return false
		}
		if isOverflowingNeutral(r) {
			return true
		}
		i += size
	}
	return false
}

// isOverflowingNeutral reports runes whose EAW is Neutral (libraries count 1)
// but that terminal fonts commonly draw as double-cell pictographs.
func isOverflowingNeutral(r rune) bool {
	switch {
	case r >= 0x12000 && r <= 0x123FF: // Cuneiform
		return true
	case r >= 0x12400 && r <= 0x1247F: // Cuneiform Numbers and Punctuation
		return true
	case r >= 0x13000 && r <= 0x1342F: // Egyptian Hieroglyphs
		return true
	case r >= 0x14400 && r <= 0x1467F: // Anatolian Hieroglyphs
		return true
	default:
		return false
	}
}
