package fixture

import "github.com/charmbracelet/lipgloss"

func f() {
	_ = lipgloss.NewStyle().TabWidth(4)
	width := lipgloss.NewStyle().TabWidth
	_ = width(4)
}
