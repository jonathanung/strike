package tui

import (
	"charm.land/lipgloss/v2/compat"
)

// appearanceMode is the session-local light/dark preference.
type appearanceMode string

const (
	appearanceAuto  appearanceMode = "auto"
	appearanceDark  appearanceMode = "dark"
	appearanceLight appearanceMode = "light"
)

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

// effectiveDark reports whether adaptive colors should use the dark member for
// the current session appearance and terminal background detection.
func (m Model) effectiveDark() bool {
	switch m.appearance {
	case appearanceDark:
		return true
	case appearanceLight:
		return false
	default:
		return m.detectedDark
	}
}

// applyAppearance resolves session appearance into compat adaptive colors and
// the pinned glamour style. Session state (appearance + detectedDark) is the
// source of truth; globals are updated only as the resolution side effect.
func (m *Model) applyAppearance() {
	dark := m.effectiveDark()
	compat.HasDarkBackground = dark
	setGlamourStyle(dark)
}
