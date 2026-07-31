package fixture

import "charm.land/lipgloss/v2"

func helper(paint func(bool) lipgloss.Style) { _ = paint(true) }

func f() { helper(lipgloss.NewStyle().Bold) }
