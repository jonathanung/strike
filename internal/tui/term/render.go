package term

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/hinshun/vt10x"
)

// Render returns a lipgloss-styled string of the current VT screen, clipped to
// cols×rows when positive. The session must not be nil. Cursor is inverted when
// visible. Colors map ANSI 0–15 and xterm 256 when the profile supports them.
func Render(s *Session, cols, rows int) string {
	if s == nil {
		return ""
	}
	s.Lock()
	defer s.Unlock()

	term := s.Terminal()
	tCols, tRows := term.Size()
	if cols <= 0 || cols > tCols {
		cols = tCols
	}
	if rows <= 0 || rows > tRows {
		rows = tRows
	}
	cur := term.Cursor()
	curVis := term.CursorVisible()

	var b strings.Builder
	for y := 0; y < rows; y++ {
		if y > 0 {
			b.WriteByte('\n')
		}
		for x := 0; x < cols; x++ {
			g := term.Cell(x, y)
			ch := g.Char
			if ch == 0 {
				ch = ' '
			}
			style := lipgloss.NewStyle()
			if fg := colorToHex(g.FG, false); fg != "" {
				style = style.Foreground(lipgloss.Color(fg))
			}
			if bg := colorToHex(g.BG, true); bg != "" {
				style = style.Background(lipgloss.Color(bg))
			}
			if g.Mode&attrBold != 0 {
				style = style.Bold(true)
			}
			if g.Mode&attrUnderline != 0 {
				style = style.Underline(true)
			}
			if g.Mode&attrItalic != 0 {
				style = style.Italic(true)
			}
			if g.Mode&attrReverse != 0 || (curVis && cur.X == x && cur.Y == y) {
				style = style.Reverse(true)
			}
			b.WriteString(style.Render(string(ch)))
		}
	}
	return b.String()
}

// attr bits mirrored from vt10x (unexported there).
const (
	attrReverse = 1 << iota
	attrUnderline
	attrBold
	attrGfx
	attrItalic
	attrBlink
	attrWrap
)

func colorToHex(c vt10x.Color, bg bool) string {
	switch c {
	case vt10x.DefaultFG:
		if bg {
			return ""
		}
		return ""
	case vt10x.DefaultBG, vt10x.DefaultCursor:
		return ""
	}
	if c < 16 {
		return ansi16Hex[c]
	}
	if c < 256 {
		r, g, b := xterm256RGB(uint32(c))
		return fmt.Sprintf("#%02x%02x%02x", r, g, b)
	}
	// Truecolor packed as 0xRRGGBB in some emulators; vt10x uses Color as index.
	return ""
}

var ansi16Hex = [16]string{
	"#000000", "#cd0000", "#00cd00", "#cdcd00",
	"#0000ee", "#cd00cd", "#00cdcd", "#e5e5e5",
	"#7f7f7f", "#ff0000", "#00ff00", "#ffff00",
	"#5c5cff", "#ff00ff", "#00ffff", "#ffffff",
}

func xterm256RGB(c uint32) (r, g, b uint8) {
	if c < 16 {
		hex := ansi16Hex[c]
		var rr, gg, bb int
		_, _ = fmt.Sscanf(hex, "#%02x%02x%02x", &rr, &gg, &bb)
		return uint8(rr), uint8(gg), uint8(bb)
	}
	if c >= 16 && c <= 231 {
		c -= 16
		r = cube(int(c / 36))
		g = cube(int((c / 6) % 6))
		b = cube(int(c % 6))
		return
	}
	if c >= 232 && c <= 255 {
		v := uint8(8 + (c-232)*10)
		return v, v, v
	}
	return 0, 0, 0
}

func cube(i int) uint8 {
	if i == 0 {
		return 0
	}
	return uint8(55 + i*40)
}
