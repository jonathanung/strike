package fixture

import (
	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/compat"
)

var (
	adaptive = &compat.AdaptiveColor{Light: lipgloss.Color("#123456")}
	complete = &compat.CompleteColor{TrueColor: lipgloss.Color("#123456")}
	profiles = &compat.CompleteAdaptiveColor{Light: compat.CompleteColor{TrueColor: lipgloss.Color("#123456")}}
	none     = &lipgloss.NoColor{}
	border   = &lipgloss.Border{Top: "-"}
)
