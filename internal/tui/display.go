package tui

import (
	"strings"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

// displayJoin joins structured display fields with a themed separator.
func displayJoin(th theme.Theme, separator string, fields ...string) string {
	th = th.Resolve()
	gap := themedSpace(th.Spacing.XS)
	return strings.Join(fields, gap+separator+gap)
}

// themedSpace returns a horizontal gap from a resolved spacing token.
func themedSpace(width int) string {
	return strings.Repeat(" ", max(0, width))
}

// dotJoin joins structured display fields with the theme's inline separator.
func dotJoin(th theme.Theme, fields ...string) string {
	th = th.Resolve()
	return displayJoin(th, th.Icons.Dot, fields...)
}

// detailJoin joins a label and its detail with the theme's detail separator.
func detailJoin(th theme.Theme, label, detail string) string {
	th = th.Resolve()
	return displayJoin(th, th.Icons.DetailSeparator, label, detail)
}
