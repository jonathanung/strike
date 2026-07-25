// Package theme is the single source of truth for the TUI's look: named
// color roles (as light+dark adaptive colors) and glyphs (theme.Icons).
// Components and views build styles from these tokens and never hardcode a
// color or a glyph, so the entire interface can be restyled — or made a data
// file later — without touching a single view. Use theme.Default() for the
// stock palette, (Theme).S() for the common precomputed styles, and
// DefaultIcons() for the glyph set.
package theme

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

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
	Danger      lipgloss.AdaptiveColor // destructive actions
	Background  lipgloss.TerminalColor // application background; NoColor is transparent
	Border      lipgloss.AdaptiveColor // standard panel border
	BorderFocus lipgloss.AdaptiveColor // border of the focused/active panel
	BorderMuted lipgloss.AdaptiveColor // dim chrome (inactive tiles, gutters)
	UserLabel   lipgloss.AdaptiveColor // "you" transcript label
	ToolLabel   lipgloss.AdaptiveColor // tool-call transcript label
	DiffAdded   lipgloss.AdaptiveColor // added lines in diffs
	DiffRemoved lipgloss.AdaptiveColor // removed lines in diffs
	BorderStyle BorderStyle            // panel border weight and glyphs
	Spacing     Spacing                // named layout spacing
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
		Danger:      lipgloss.AdaptiveColor{Light: "#c23b3b", Dark: "#ff8087"},
		Background:  lipgloss.AdaptiveColor{Light: "#ffffff", Dark: "#1c1b22"},
		Border:      lipgloss.AdaptiveColor{Light: "#cfcdda", Dark: "#3a3846"},
		BorderFocus: lipgloss.AdaptiveColor{Light: "#6d43d6", Dark: "#a78bff"},
		BorderMuted: lipgloss.AdaptiveColor{Light: "#e4e2ec", Dark: "#2a2833"},
		UserLabel:   lipgloss.AdaptiveColor{Light: "#0b7285", Dark: "#5cd0e8"},
		ToolLabel:   lipgloss.AdaptiveColor{Light: "#3f51b5", Dark: "#9db2ff"},
		DiffAdded:   lipgloss.AdaptiveColor{Light: "#1f8a4c", Dark: "#5edb92"},
		DiffRemoved: lipgloss.AdaptiveColor{Light: "#c23b3b", Dark: "#ff8087"},
		BorderStyle: lightBorderStyle(),
		Spacing:     NewSpacing(1, 2, 3, 4).WithLabel(1),
		Icons:       DefaultIcons(),
	}
}

// NoBackground explicitly opts a theme out of drawing a background.
func NoBackground() lipgloss.NoColor { return lipgloss.NoColor{} }

// BackgroundPrefix returns the terminal sequence that paints the configured
// background, or an empty string when it is explicitly transparent.
func BackgroundPrefix(background lipgloss.TerminalColor) string {
	switch background := background.(type) {
	case lipgloss.NoColor:
		return ""
	case *lipgloss.NoColor:
		if background != nil {
			return ""
		}
	}
	rendered := lipgloss.NewStyle().Background(background).Render(" ")
	if at := strings.IndexByte(rendered, ' '); at >= 0 {
		return rendered[:at]
	}
	return ""
}

// Resolve fills unset portions of a theme from Default. Explicit NoColor is
// preserved for Background; every other unset background is solid.
func (t Theme) Resolve() Theme {
	d := Default()
	t.Text = resolveAdaptive(t.Text, d.Text)
	t.TextMuted = resolveAdaptive(t.TextMuted, d.TextMuted)
	t.Accent = resolveAdaptive(t.Accent, d.Accent)
	t.AccentAlt = resolveAdaptive(t.AccentAlt, d.AccentAlt)
	t.Highlight = resolveAdaptive(t.Highlight, d.Highlight)
	t.Success = resolveAdaptive(t.Success, d.Success)
	t.Warning = resolveAdaptive(t.Warning, d.Warning)
	t.Error = resolveAdaptive(t.Error, d.Error)
	t.Danger = resolveAdaptive(t.Danger, d.Danger)
	t.Border = resolveAdaptive(t.Border, d.Border)
	t.BorderFocus = resolveAdaptive(t.BorderFocus, d.BorderFocus)
	t.BorderMuted = resolveAdaptive(t.BorderMuted, d.BorderMuted)
	t.UserLabel = resolveAdaptive(t.UserLabel, d.UserLabel)
	t.ToolLabel = resolveAdaptive(t.ToolLabel, d.ToolLabel)
	t.DiffAdded = resolveAdaptive(t.DiffAdded, d.DiffAdded)
	t.DiffRemoved = resolveAdaptive(t.DiffRemoved, d.DiffRemoved)
	t.Background = resolveBackground(t.Background, d.Background)
	t.BorderStyle = resolveBorderStyle(t.BorderStyle)
	t.Spacing = resolveSpacing(t.Spacing, d.Spacing)
	t.Icons = resolveIcons(t.Icons, d.Icons)
	return t
}

func resolveAdaptive(c, fallback lipgloss.AdaptiveColor) lipgloss.AdaptiveColor {
	if c.Light == "" {
		c.Light = fallback.Light
	}
	if c.Dark == "" {
		c.Dark = fallback.Dark
	}
	return c
}

func resolveBackground(c, fallback lipgloss.TerminalColor) lipgloss.TerminalColor {
	if c == nil {
		return fallback
	}
	switch c := c.(type) {
	case lipgloss.NoColor:
		return c
	case *lipgloss.NoColor:
		if c == nil {
			return fallback
		}
		return *c
	case lipgloss.Color:
		if c == "" {
			return fallback
		}
	case *lipgloss.Color:
		if c == nil || *c == "" {
			return fallback
		}
		return *c
	case lipgloss.ANSIColor:
		return c
	case *lipgloss.ANSIColor:
		if c == nil {
			return fallback
		}
		return *c
	case lipgloss.AdaptiveColor:
		return resolveBackgroundAdaptive(c, fallback)
	case *lipgloss.AdaptiveColor:
		if c == nil {
			return fallback
		}
		return resolveBackgroundAdaptive(*c, fallback)
	case lipgloss.CompleteColor:
		return resolveCompleteColor(c, fallbackColor(fallback))
	case *lipgloss.CompleteColor:
		if c == nil {
			return fallback
		}
		return resolveCompleteColor(*c, fallbackColor(fallback))
	case lipgloss.CompleteAdaptiveColor:
		light, dark := fallbackAdaptiveColors(fallback)
		return lipgloss.CompleteAdaptiveColor{
			Light: resolveCompleteColor(c.Light, light),
			Dark:  resolveCompleteColor(c.Dark, dark),
		}
	case *lipgloss.CompleteAdaptiveColor:
		if c == nil {
			return fallback
		}
		light, dark := fallbackAdaptiveColors(fallback)
		return lipgloss.CompleteAdaptiveColor{
			Light: resolveCompleteColor(c.Light, light),
			Dark:  resolveCompleteColor(c.Dark, dark),
		}
	}
	return c
}

func resolveBackgroundAdaptive(c lipgloss.AdaptiveColor, fallback lipgloss.TerminalColor) lipgloss.TerminalColor {
	light, dark := fallbackAdaptiveColors(fallback)
	if c.Light == "" {
		c.Light = c.Dark
		if c.Light == "" {
			c.Light = light.TrueColor
		}
	}
	if c.Dark == "" {
		c.Dark = c.Light
		if c.Dark == "" {
			c.Dark = dark.TrueColor
		}
	}
	if c.Light == "" && c.Dark == "" {
		return fallback
	}
	return c
}

func resolveCompleteColor(c, fallback lipgloss.CompleteColor) lipgloss.CompleteColor {
	selected := c.TrueColor
	if selected == "" {
		selected = c.ANSI256
	}
	if selected == "" {
		selected = c.ANSI
	}
	if c.TrueColor == "" {
		c.TrueColor = selected
		if c.TrueColor == "" {
			c.TrueColor = fallback.TrueColor
		}
	}
	if c.ANSI256 == "" {
		c.ANSI256 = selected
		if c.ANSI256 == "" {
			c.ANSI256 = fallback.ANSI256
		}
	}
	if c.ANSI == "" {
		c.ANSI = selected
		if c.ANSI == "" {
			c.ANSI = fallback.ANSI
		}
	}
	return c
}

func fallbackAdaptiveColors(c lipgloss.TerminalColor) (lipgloss.CompleteColor, lipgloss.CompleteColor) {
	switch c := c.(type) {
	case lipgloss.AdaptiveColor:
		return completeColor(c.Light), completeColor(c.Dark)
	case *lipgloss.AdaptiveColor:
		if c != nil {
			return completeColor(c.Light), completeColor(c.Dark)
		}
	case lipgloss.CompleteAdaptiveColor:
		return c.Light, c.Dark
	case *lipgloss.CompleteAdaptiveColor:
		if c != nil {
			return c.Light, c.Dark
		}
	}
	fallback := fallbackColor(c)
	return fallback, fallback
}

func fallbackColor(c lipgloss.TerminalColor) lipgloss.CompleteColor {
	switch c := c.(type) {
	case lipgloss.Color:
		return completeColor(string(c))
	case *lipgloss.Color:
		if c != nil {
			return completeColor(string(*c))
		}
	case lipgloss.ANSIColor:
		return completeColor(strconv.FormatUint(uint64(c), 10))
	case *lipgloss.ANSIColor:
		if c != nil {
			return completeColor(strconv.FormatUint(uint64(*c), 10))
		}
	case lipgloss.AdaptiveColor:
		if lipgloss.HasDarkBackground() {
			return completeColor(c.Dark)
		}
		return completeColor(c.Light)
	case *lipgloss.AdaptiveColor:
		if c != nil {
			return fallbackColor(*c)
		}
	case lipgloss.CompleteColor:
		return c
	case *lipgloss.CompleteColor:
		if c != nil {
			return *c
		}
	case lipgloss.CompleteAdaptiveColor:
		if lipgloss.HasDarkBackground() {
			return c.Dark
		}
		return c.Light
	case *lipgloss.CompleteAdaptiveColor:
		if c != nil {
			return fallbackColor(*c)
		}
	}
	return lipgloss.CompleteColor{}
}

func completeColor(value string) lipgloss.CompleteColor {
	return lipgloss.CompleteColor{TrueColor: value, ANSI256: value, ANSI: value}
}
