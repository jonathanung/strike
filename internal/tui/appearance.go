package tui

import (
	"github.com/charmbracelet/lipgloss"
)

// appearanceMode is the session-local light/dark preference.
type appearanceMode string

const (
	appearanceAuto  appearanceMode = "auto"
	appearanceDark  appearanceMode = "dark"
	appearanceLight appearanceMode = "light"
)

// appearanceAutoDark caches the terminal's detected background the first time
// applyAppearance runs, so cycling back to "auto" after a forced mode restores
// detection rather than sticking on the last forced value. lipgloss has no
// public unset for SetHasDarkBackground.
var (
	appearanceDetected     bool
	appearanceDetectedDark bool
)

// applyAppearance forces lipgloss adaptive colors for dark/light, or restores
// the initially detected background for auto. Package-level so tests can drive
// appearance without going through slash-command parsing.
func applyAppearance(mode appearanceMode) {
	if !appearanceDetected {
		appearanceDetectedDark = lipgloss.HasDarkBackground()
		appearanceDetected = true
	}
	switch mode {
	case appearanceDark:
		lipgloss.SetHasDarkBackground(true)
	case appearanceLight:
		lipgloss.SetHasDarkBackground(false)
	default:
		lipgloss.SetHasDarkBackground(appearanceDetectedDark)
	}
}

func cycleAppearance(cur appearanceMode) appearanceMode {
	switch cur {
	case appearanceAuto:
		return appearanceDark
	case appearanceDark:
		return appearanceLight
	default:
		return appearanceAuto
	}
}

func parseAppearance(s string) (appearanceMode, bool) {
	switch s {
	case "auto":
		return appearanceAuto, true
	case "dark":
		return appearanceDark, true
	case "light":
		return appearanceLight, true
	default:
		return "", false
	}
}
