// Package theme is the single source of truth for the TUI's look: named
// color roles (as light+dark adaptive colors) and glyphs (theme.Icons).
// Components and views build styles from these tokens and never hardcode a
// color or a glyph, so the entire interface can be restyled — or made a data
// file later — without touching a single view. Use theme.Default() for the
// stock palette, (Theme).S() for the common precomputed styles, and
// DefaultIcons() for the glyph set.
package theme

import "github.com/charmbracelet/lipgloss"

// Theme is the palette of semantic color roles plus the glyph set. Every
// field is an adaptive color so both light and dark terminals stay readable.
// Roles carry meaning, not appearance: Accent is "the primary emphasis
// color", not "violet", so swapping the palette keeps views correct.
type Theme struct {
	Text        lipgloss.AdaptiveColor // primary foreground
	TextMuted   lipgloss.AdaptiveColor // secondary/de-emphasized foreground
	Accent      lipgloss.AdaptiveColor // primary emphasis (titles, assistant)
	AccentAlt   lipgloss.AdaptiveColor // secondary emphasis (user label, info)
	Highlight   lipgloss.AdaptiveColor // foreground of the selected/active item
	Success     lipgloss.AdaptiveColor // positive state (ok, added)
	Warning     lipgloss.AdaptiveColor // caution state (permission prompts)
	Error       lipgloss.AdaptiveColor // failure state (errors, removed)
	Border      lipgloss.AdaptiveColor // standard panel border
	BorderFocus lipgloss.AdaptiveColor // border of the focused/active panel
	BorderMuted lipgloss.AdaptiveColor // dim chrome (inactive tiles, gutters)
	UserLabel   lipgloss.AdaptiveColor // "you" transcript label
	ToolLabel   lipgloss.AdaptiveColor // tool-call transcript label
	DiffAdded   lipgloss.AdaptiveColor // added lines in diffs
	DiffRemoved lipgloss.AdaptiveColor // removed lines in diffs
	Icons       Icons                  // glyph set (see DefaultIcons)
}

// Default returns strike's stock theme: a Posting-inspired palette with a
// violet primary accent, cyan secondary, warm success/warning, and calm muted
// chrome. Colors are hex adaptive pairs (lipgloss degrades them for
// non-truecolor terminals); both the light and dark member of every pair is
// chosen to stay legible on its background.
func Default() Theme {
	return Theme{
		Text:        lipgloss.AdaptiveColor{Light: "#1c1b22", Dark: "#dcdae6"},
		TextMuted:   lipgloss.AdaptiveColor{Light: "#6c6a7a", Dark: "#8b899c"},
		Accent:      lipgloss.AdaptiveColor{Light: "#6d43d6", Dark: "#b39dff"},
		AccentAlt:   lipgloss.AdaptiveColor{Light: "#0b7285", Dark: "#5cd0e8"},
		Highlight:   lipgloss.AdaptiveColor{Light: "#4c1d95", Dark: "#f4f1ff"},
		Success:     lipgloss.AdaptiveColor{Light: "#1f8a4c", Dark: "#5edb92"},
		Warning:     lipgloss.AdaptiveColor{Light: "#b7791f", Dark: "#f5c451"},
		Error:       lipgloss.AdaptiveColor{Light: "#c23b3b", Dark: "#ff8087"},
		Border:      lipgloss.AdaptiveColor{Light: "#cfcdda", Dark: "#3a3846"},
		BorderFocus: lipgloss.AdaptiveColor{Light: "#6d43d6", Dark: "#a78bff"},
		BorderMuted: lipgloss.AdaptiveColor{Light: "#e4e2ec", Dark: "#2a2833"},
		UserLabel:   lipgloss.AdaptiveColor{Light: "#0b7285", Dark: "#5cd0e8"},
		ToolLabel:   lipgloss.AdaptiveColor{Light: "#3f51b5", Dark: "#9db2ff"},
		DiffAdded:   lipgloss.AdaptiveColor{Light: "#1f8a4c", Dark: "#5edb92"},
		DiffRemoved: lipgloss.AdaptiveColor{Light: "#c23b3b", Dark: "#ff8087"},
		Icons:       DefaultIcons(),
	}
}
