package fixture

import "charm.land/lipgloss/v2"

func f() {
	_ = lipgloss.NewStyle().TabWidth(4)
	width := lipgloss.NewStyle().TabWidth
	_ = width(4)
}
