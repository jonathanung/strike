package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

// KeyHint is one keybinding hint: the key (accented) and what it does (muted).
type KeyHint struct {
	Key   string // e.g. "enter", "ctrl+p"
	Label string // e.g. "send", "palette"
}

// KeyHints renders a single footer row of keybinding hints joined by dots:
// "enter send · ctrl+p palette · tab agent". Keys are accented, labels muted.
// It truncates cleanly to width by dropping whole hints that do not fit
// rather than cutting mid-hint (falling back to truncating the first hint
// only when even one will not fit).
//
//	ui.KeyHints(th, width, []ui.KeyHint{
//	    {"enter", "send"}, {"ctrl+p", "palette"}, {"esc", "interrupt"},
//	})
func KeyHints(th theme.Theme, width int, hints []KeyHint) string {
	if width <= 0 || len(hints) == 0 {
		return ""
	}
	th = th.Resolve()
	ic := resolveIcons(th)
	st := th.S()
	space := strings.Repeat(" ", th.Spacing.XS)
	sep := space + st.Muted.Render(ic.Dot) + space
	sepWidth := lipgloss.Width(space + ic.Dot + space)

	var b strings.Builder
	used := 0
	for i, h := range hints {
		seg := st.Accent.Render(h.Key)
		plain := h.Key
		if h.Label != "" {
			seg += space + st.Muted.Render(h.Label)
			plain += space + h.Label
		}
		need := lipgloss.Width(plain)
		if i > 0 {
			need += sepWidth
		}
		if used+need > width {
			break
		}
		if i > 0 {
			b.WriteString(sep)
		}
		b.WriteString(seg)
		used += need
	}
	if b.Len() == 0 {
		// Not even the first hint fits: truncate it to the width.
		first := hints[0].Key
		if hints[0].Label != "" {
			first += space + hints[0].Label
		}
		return st.Muted.Render(truncate(th, first, width))
	}
	return b.String()
}
