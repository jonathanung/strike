package fixture

import "github.com/charmbracelet/lipgloss"

func f() {
	style := lipgloss.NewStyle()
	_ = style.UnsetForeground()
	_ = style.UnsetPadding()
	clearForeground := style.UnsetForeground
	clearBorder := style.UnsetBorderStyle
	_ = clearForeground
	_ = clearBorder
}
