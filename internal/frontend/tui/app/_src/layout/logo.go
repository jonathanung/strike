package tui

import (
	"strings"

	"github.com/jonathanung/strike-cli/internal/frontend/tui/theme"
)

// Logo is the Strike wordmark in the web cockpit voice: letter-spaced STRIKE
// in the title/accent token. Use LogoCompact in the header strip.
func Logo(th theme.Theme) string {
	th = th.Resolve()
	st := th.S()
	space := strings.Repeat(" ", max(1, th.Spacing.XS))
	return st.Title.Render(strings.Join(strings.Split("STRIKE", ""), space))
}

// LogoCompact is the one-line header mark: accent S + STRIKE, matching the
// web .wordmark (filled S + letter-spaced name) without a Family bolt.
func LogoCompact(th theme.Theme) string {
	th = th.Resolve()
	st := th.S()
	return st.AccentStrong.Render("S") + themedSpace(th.Spacing.XS) + st.Title.Render("STRIKE")
}
