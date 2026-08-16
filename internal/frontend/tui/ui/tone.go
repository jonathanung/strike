package ui

import (
	"charm.land/lipgloss/v2"

	"github.com/jonathanung/strike-cli/internal/frontend/tui/theme"
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
	ToneDanger                // destructive (distinct from Error)
	ToneMuted                 // de-emphasized
)

// toneColor resolves a Tone to its adaptive color for the given theme.
func toneColor(th theme.Theme, tone Tone) theme.AdaptiveColor {
	th = th.Resolve()
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
	case ToneDanger:
		return th.Danger
	case ToneMuted:
		return th.TextMuted
	default:
		return th.Text
	}
}

// toneStyle is a foreground style in the tone's color.
func toneStyle(th theme.Theme, tone Tone) lipgloss.Style {
	st := th.S()
	switch tone {
	case ToneAccent:
		return st.Accent
	case ToneAccentAlt:
		return st.AccentAlt
	case ToneSuccess:
		return st.Success
	case ToneWarning:
		return st.Warning
	case ToneError:
		return st.Error
	case ToneDanger:
		return st.Danger
	case ToneMuted:
		return st.Muted
	default:
		return st.Text
	}
}

func toneStrongStyle(th theme.Theme, tone Tone) lipgloss.Style {
	st := th.S()
	switch tone {
	case ToneAccent:
		return st.AccentStrong
	case ToneAccentAlt:
		return st.AccentAltStrong
	case ToneSuccess:
		return st.SuccessStrong
	case ToneWarning:
		return st.WarningStrong
	case ToneError:
		return st.ErrorStrong
	case ToneDanger:
		return st.DangerStrong
	case ToneMuted:
		return st.MutedStrong
	default:
		return st.TextStrong
	}
}
