package fixture

import (
	"charm.land/lipgloss/v2"
	"github.com/jonathanung/strike-cli/internal/frontend/tui/theme"
)

func f(th theme.Theme) string {
	return lipgloss.NewStyle().Foreground(th.Accent).Padding(th.Spacing.SM).Render("not resolved")
}
