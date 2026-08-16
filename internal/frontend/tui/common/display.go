package common

import (
	"strings"

	"github.com/jonathanung/strike-cli/internal/frontend/tui/theme"
)

// DisplayJoin joins structured display fields with a themed separator.
func DisplayJoin(th theme.Theme, separator string, fields ...string) string {
	th = th.Resolve()
	gap := ThemedSpace(th.Spacing.XS)
	return strings.Join(fields, gap+separator+gap)
}

// ThemedSpace returns a horizontal gap from a resolved spacing token.
func ThemedSpace(width int) string {
	return strings.Repeat(" ", max(0, width))
}

// DotJoin joins structured display fields with the theme's inline separator.
func DotJoin(th theme.Theme, fields ...string) string {
	th = th.Resolve()
	return DisplayJoin(th, th.Icons.Dot, fields...)
}

// DetailJoin joins a label and its detail with the theme's detail separator.
func DetailJoin(th theme.Theme, label, detail string) string {
	th = th.Resolve()
	return DisplayJoin(th, th.Icons.DetailSeparator, label, detail)
}
