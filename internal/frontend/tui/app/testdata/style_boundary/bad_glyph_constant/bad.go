package fixture

import "charm.land/lipgloss/v2"

const tick = "✓"
const aliasedTick = tick

func icon() string { return "✓" }

func f() {
	assigned := "✓"
	_ = assigned
	_ = lipgloss.NewStyle().Render("✓")
}
