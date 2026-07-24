package ui

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

// Tone names a semantic color role for components that accept a color choice
// (Badge chips, Panel/Dialog border overrides, Card accents). It maps to a
// theme role so callers pick meaning, not a specific color. The zero value,
// ToneDefault, means "no special color": plain text for a Badge, and "let
// focus decide" for a Panel border.
type Tone int

const (
	ToneDefault   Tone = iota // theme.Text; for panels, defer to focus/dim
	ToneAccent                // primary emphasis
	ToneAccentAlt             // secondary emphasis
	ToneSuccess               // positive
	ToneWarning               // caution
	ToneError                 // failure
	ToneMuted                 // de-emphasized
)

// toneColor resolves a Tone to its adaptive color for the given theme.
func toneColor(th theme.Theme, tone Tone) lipgloss.AdaptiveColor {
	switch tone {
	case ToneAccent:
		return th.Accent
	case ToneAccentAlt:
		return th.AccentAlt
	case ToneSuccess:
		return th.Success
	case ToneWarning:
		return th.Warning
	case ToneError:
		return th.Error
	case ToneMuted:
		return th.TextMuted
	default:
		return th.Text
	}
}

// toneStyle is a foreground style in the tone's color.
func toneStyle(th theme.Theme, tone Tone) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(toneColor(th, tone))
}
