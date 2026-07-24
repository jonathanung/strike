// Package theme defines the semantic color contract for the TUI. Views
// build lipgloss styles from these named roles and never hardcode colors,
// so themes can become data files later without touching views.
package theme

import "github.com/charmbracelet/lipgloss"

type Theme struct {
	Text        lipgloss.AdaptiveColor
	TextMuted   lipgloss.AdaptiveColor
	Accent      lipgloss.AdaptiveColor
	Success     lipgloss.AdaptiveColor
	Warning     lipgloss.AdaptiveColor
	Error       lipgloss.AdaptiveColor
	Border      lipgloss.AdaptiveColor
	BorderFocus lipgloss.AdaptiveColor
	UserLabel   lipgloss.AdaptiveColor
	ToolLabel   lipgloss.AdaptiveColor
	DiffAdded   lipgloss.AdaptiveColor
	DiffRemoved lipgloss.AdaptiveColor
}

func Default() Theme {
	return Theme{
		Text:        lipgloss.AdaptiveColor{Light: "235", Dark: "252"},
		TextMuted:   lipgloss.AdaptiveColor{Light: "245", Dark: "243"},
		Accent:      lipgloss.AdaptiveColor{Light: "63", Dark: "111"},
		Success:     lipgloss.AdaptiveColor{Light: "29", Dark: "78"},
		Warning:     lipgloss.AdaptiveColor{Light: "130", Dark: "214"},
		Error:       lipgloss.AdaptiveColor{Light: "124", Dark: "203"},
		Border:      lipgloss.AdaptiveColor{Light: "250", Dark: "238"},
		BorderFocus: lipgloss.AdaptiveColor{Light: "63", Dark: "111"},
		UserLabel:   lipgloss.AdaptiveColor{Light: "29", Dark: "78"},
		ToolLabel:   lipgloss.AdaptiveColor{Light: "93", Dark: "141"},
		DiffAdded:   lipgloss.AdaptiveColor{Light: "29", Dark: "78"},
		DiffRemoved: lipgloss.AdaptiveColor{Light: "124", Dark: "203"},
	}
}
