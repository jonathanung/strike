package fixture

import "github.com/charmbracelet/lipgloss"

func f() { _ = lipgloss.NewStyle().Border(lipgloss.Border{Top: "*"}) }
