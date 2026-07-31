package ui

import (
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

// Badge is a compact soft pill for a short status token: tone-colored label on
// a SurfaceMuted fill with XS horizontal pad. Optional Icons.BadgeLeft /
// BadgeRight delimiters stay available for themes that want brackets, but the
// stock set is delimiter-free so chips read as Family-style soft pills.
//
//	ui.Badge(th, ui.ToneAccent, "anthropic/claude-sonnet-5")
//	ui.Badge(th, ui.ToneSuccess, "authed")
//
// Badge does not take a width; it sizes to its text. Truncate the text before
// calling if it must fit a budget. Empty text yields an empty string.
func Badge(th theme.Theme, tone Tone, text string) string {
	th = th.Resolve()
	if text == "" {
		return ""
	}
	ic := resolveIcons(th)
	pad := strings.Repeat(" ", max(0, th.Spacing.XS))
	label := toneStrongStyle(th, tone).Render(text)
	var b strings.Builder
	if ic.BadgeLeft != "" {
		b.WriteString(th.S().Muted.Render(ic.BadgeLeft))
	}
	b.WriteString(pad)
	b.WriteString(label)
	b.WriteString(pad)
	if ic.BadgeRight != "" {
		b.WriteString(th.S().Muted.Render(ic.BadgeRight))
	}
	content := b.String()
	w := lipgloss.Width(content)
	if w < 1 {
		return content
	}
	return paintSurface(content, w, th.SurfaceMuted)
}
