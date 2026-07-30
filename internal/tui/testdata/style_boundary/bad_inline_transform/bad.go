package fixture

import "charm.land/lipgloss/v2"

func f() {
	_ = lipgloss.NewStyle().Inline(true)
	_ = lipgloss.NewStyle().Transform(func(s string) string { return s })
	inline := lipgloss.NewStyle().Inline
	transform := lipgloss.NewStyle().Transform
	_ = inline
	_ = transform
}
