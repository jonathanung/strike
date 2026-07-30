package ui

import (
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

// Logo is the "strike" wordmark: the bolt motif and spaced letters hugged by
// two hairline rules, colored as an accent gradient (accent-alt rule, accent
// letters, accent rule) with a warm bolt. Three lines, at most ~16 columns.
// Use LogoCompact when the space is narrower than the wordmark.
//
//	card := ui.Card{Title: "strike", Body: ui.Logo(th), Width: 30}
func Logo(th theme.Theme) string {
	th = th.Resolve()
	ic := resolveIcons(th)
	st := th.S()
	space := strings.Repeat(" ", th.Spacing.XS)
	// Build mid first so rule width matches the styled wordmark (bold/title
	// glyphs can differ from the plain-string width used previously).
	mid := st.Warning.Render(ic.Bolt) + space + st.Title.Render("S"+space+"T"+space+"R"+space+"I"+space+"K"+space+"E")
	w := max(1, lipgloss.Width(mid))
	top := st.AccentAlt.Render(strings.Repeat(ic.LogoTopRule, w))
	bot := st.Accent.Render(strings.Repeat(ic.LogoBottomRule, w))
	// Pad any line that still disagrees so JoinHorizontal gutters stay blank.
	pad := func(line string) string {
		if d := w - lipgloss.Width(line); d > 0 {
			return line + strings.Repeat(" ", d)
		}
		return line
	}
	return pad(top) + "\n" + pad(mid) + "\n" + pad(bot)
}

// LogoCompact is the one-line fallback wordmark: "⚡ strike", bolt warm and
// word accented. Use it when the full Logo will not fit.
//
//	if width < 18 { header = ui.LogoCompact(th) } else { header = ui.Logo(th) }
func LogoCompact(th theme.Theme) string {
	ic := resolveIcons(th)
	st := th.S()
	return st.Warning.Render(ic.Bolt) + strings.Repeat(" ", th.Resolve().Spacing.XS) + st.Title.Render("strike")
}
