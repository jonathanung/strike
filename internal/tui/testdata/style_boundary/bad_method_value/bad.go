package fixture

import "charm.land/lipgloss/v2"

func f() {
	paint := lipgloss.NewStyle().Bold
	_ = paint(true)
}
