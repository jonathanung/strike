package fixture

import "github.com/charmbracelet/lipgloss"

const tick = "✓"
const aliasedTick = tick

func icon() string { return "✓" }

func f() {
	assigned := "✓"
	_ = assigned
	_ = lipgloss.NewStyle().Render("✓")
}
