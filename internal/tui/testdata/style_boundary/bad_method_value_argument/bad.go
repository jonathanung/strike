package fixture

import "github.com/charmbracelet/lipgloss"

func helper(paint func(bool) lipgloss.Style) { _ = paint(true) }

func f() { helper(lipgloss.NewStyle().Bold) }
