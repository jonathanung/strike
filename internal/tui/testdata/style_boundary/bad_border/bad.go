package fixture

import "charm.land/lipgloss/v2"

func f() { _ = lipgloss.NewStyle().Border(lipgloss.Border{Top: "*"}) }
