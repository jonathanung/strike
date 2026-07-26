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

// PinAppearance detects the terminal background once and freezes lipgloss plus
// glamour style selection. Call before tea.NewProgram so OSC 11 replies cannot
// race the program's stdin reader into the composer (#52).
func PinAppearance() {
	applyAppearance(appearanceAuto)
}

// applyAppearance forces lipgloss adaptive colors for dark/light, or restores
// the initially detected background for auto. Package-level so tests can drive
// appearance without going through slash-command parsing.
func applyAppearance(mode appearanceMode) {
	if !appearanceDetected {
		appearanceDetectedDark = lipgloss.HasDarkBackground()
		appearanceDetected = true
	}
	var dark bool
	switch mode {
	case appearanceDark:
		dark = true
	case appearanceLight:
		dark = false
	default:
		dark = appearanceDetectedDark
	}
	lipgloss.SetHasDarkBackground(dark)
	setGlamourStyle(dark)
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
