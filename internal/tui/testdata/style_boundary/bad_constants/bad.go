package fixture

import "github.com/charmbracelet/lipgloss"

const color = "red"
const indirect = color
const pad = 2

func f() {
	const local = indirect
	_ = lipgloss.NewStyle().Foreground(local).Padding(pad)
}
