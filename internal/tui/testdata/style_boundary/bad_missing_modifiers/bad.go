package fixture

import "github.com/charmbracelet/lipgloss"

func f() {
	style := lipgloss.NewStyle()
	_ = style.BorderStyle(lipgloss.Border{Top: "-"})
	_ = style.BorderForeground(lipgloss.Color("1"))
	_ = style.BorderBackground(lipgloss.Color("1"))
	_ = style.BorderTopForeground(lipgloss.Color("1"))
	_ = style.BorderTopBackground(lipgloss.Color("1"))
	_ = style.BorderBottomForeground(lipgloss.Color("1"))
	_ = style.BorderBottomBackground(lipgloss.Color("1"))
	_ = style.BorderLeftForeground(lipgloss.Color("1"))
	_ = style.BorderLeftBackground(lipgloss.Color("1"))
	_ = style.BorderRightForeground(lipgloss.Color("1"))
	_ = style.BorderRightBackground(lipgloss.Color("1"))
	_ = style.MarginBackground(lipgloss.Color("1"))
	_ = style.ColorWhitespace(true)
	_ = style.UnderlineSpaces(true)
	_ = style.StrikethroughSpaces(true)
}
