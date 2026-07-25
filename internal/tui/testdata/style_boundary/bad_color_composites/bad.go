package fixture

import "github.com/charmbracelet/lipgloss"

var (
	adaptive = &lipgloss.AdaptiveColor{Light: "#123456"}
	complete = &lipgloss.CompleteColor{TrueColor: "#123456"}
	profiles = &lipgloss.CompleteAdaptiveColor{Light: lipgloss.CompleteColor{TrueColor: "#123456"}}
	none     = &lipgloss.NoColor{}
	border   = &lipgloss.Border{Top: "-"}
)
