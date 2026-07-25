package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

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
	word := ic.Bolt + space + "S" + space + "T" + space + "R" + space + "I" + space + "K" + space + "E"
	w := lipgloss.Width(word)

	top := st.AccentAlt.Render(strings.Repeat(ic.LogoTopRule, w))
	mid := st.Warning.Render(ic.Bolt) + space + st.Title.Render("S"+space+"T"+space+"R"+space+"I"+space+"K"+space+"E")
	bot := st.Accent.Render(strings.Repeat(ic.LogoBottomRule, w))
	return top + "\n" + mid + "\n" + bot
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
