package fixture

import "github.com/charmbracelet/lipgloss"

func f() {
	paint := lipgloss.NewStyle().Bold
	_ = paint(true)
}
