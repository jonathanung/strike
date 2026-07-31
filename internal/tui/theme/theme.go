// Package theme is the single source of truth for the TUI's look: named
// color roles (as light+dark adaptive colors) and glyphs (theme.Icons).
// Components and views build styles from these tokens and never hardcode a
// color or a glyph, so the entire interface can be restyled — or made a data
// file later — without touching a single view. Use theme.Default() for the
// stock palette, (Theme).S() for the common precomputed styles, and
// DefaultIcons() for the glyph set.
package theme

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/compat"
)

// AdaptiveColor is a light/dark hex (or ANSI index) pair. On-disk theme JSON
// keeps string tokens unchanged (E13.2 schema decision). RGBA delegates to
// lipgloss/v2/compat so values pass straight to Style.Foreground/Background
// — conversion lives at this chokepoint rather than at every call site.
type AdaptiveColor struct {
	Light string
	Dark  string
}

// RGBA satisfies color.Color via compat.AdaptiveColor (respects
// compat.HasDarkBackground).
func (c AdaptiveColor) RGBA() (uint32, uint32, uint32, uint32) {
	return c.Compat().RGBA()
}

// Compat returns the lipgloss v2 compat form for bubbles/cursor wiring.
func (c AdaptiveColor) Compat() compat.AdaptiveColor {
	return compat.AdaptiveColor{
		Light: lipgloss.Color(c.Light),
		Dark:  lipgloss.Color(c.Dark),
	}
}

// Theme is the palette of semantic color roles plus the glyph set. Every
// adaptive field is a light+dark pair so both light and dark terminals stay
// readable. Roles carry meaning, not appearance: Accent is "the primary
// emphasis color", not "violet", so swapping the palette keeps views correct.
type Theme struct {
	Text         AdaptiveColor // primary foreground
	TextMuted    AdaptiveColor // secondary/de-emphasized foreground
	Accent       AdaptiveColor // primary emphasis (titles, assistant)
	AccentAlt    AdaptiveColor // secondary emphasis (user label, info)
	Highlight    AdaptiveColor // foreground of the selected/active item
	Success      AdaptiveColor // positive state (ok, added)
	Warning      AdaptiveColor // caution / needs-you yellow (permission, attention)
	Error        AdaptiveColor // failure state (errors, removed)
	Danger       AdaptiveColor // destructive actions
	Background   color.Color   // application background; NoColor is transparent
	Surface      AdaptiveColor // solid panel fill
	SurfaceFocus AdaptiveColor // focused/active panel fill
	SurfaceMuted AdaptiveColor // dim/inactive panel fill
	Border       AdaptiveColor // standard panel border (bordered chrome)
	BorderFocus  AdaptiveColor // border of the focused/active panel
	BorderMuted  AdaptiveColor // dim chrome (inactive tiles, gutters)
	UserLabel    AdaptiveColor // "you" transcript label
	ToolLabel    AdaptiveColor // tool-call transcript label
	DiffAdded    AdaptiveColor // added lines in diffs
	DiffRemoved  AdaptiveColor // removed lines in diffs
	OverlayScrim AdaptiveColor // de-emphasized modal background fill
	Chrome       ChromeMode    // solid surfaces vs box borders
	BorderStyle  BorderStyle   // panel border weight and glyphs
	Spacing      Spacing       // named layout spacing
	Icons        Icons         // glyph set (see DefaultIcons)
}

// Default returns strike's stock theme: a Posting-inspired palette with a
// violet primary accent, cyan secondary, warm success/warning, and calm muted
// chrome. Colors are hex adaptive pairs (lipgloss degrades them for
// non-truecolor terminals); both the light and dark member of every pair is
// chosen to stay legible on its background.
func Default() Theme {
	return Theme{
		// Dark Text/Muted lean brighter for contrast on #1c1b22; borders sit
		// a step clearer against both light and dark chrome.
		Text:      AdaptiveColor{Light: "#1a1820", Dark: "#eceaf4"},
		TextMuted: AdaptiveColor{Light: "#5a5868", Dark: "#a09eb0"},
		Accent:    AdaptiveColor{Light: "#6d43d6", Dark: "#b39dff"},
		AccentAlt: AdaptiveColor{Light: "#0b7285", Dark: "#5cd0e8"},
		Highlight: AdaptiveColor{Light: "#4c1d95", Dark: "#f4f1ff"},
		Success:   AdaptiveColor{Light: "#1f8a4c", Dark: "#5edb92"},
		// Warning is clear yellow (needs-you / attention, permission, caution).
		// Prior amber (#b7791f / #f5c451) read muddy and low-contrast when the
		// terminal mis-detected light/dark and applied the light member on a
		// dark background.
		Warning:    AdaptiveColor{Light: "#a16207", Dark: "#ffd84d"},
		Error:      AdaptiveColor{Light: "#c23b3b", Dark: "#ff8087"},
		Danger:     AdaptiveColor{Light: "#c23b3b", Dark: "#ff8087"},
		Background: AdaptiveColor{Light: "#ffffff", Dark: "#1c1b22"},
		// Surfaces sit a step above Background so solid panels read as tiles.
		Surface:      AdaptiveColor{Light: "#f3f1f8", Dark: "#252430"},
		SurfaceFocus: AdaptiveColor{Light: "#ebe6f8", Dark: "#2f2c3c"},
		SurfaceMuted: AdaptiveColor{Light: "#f7f6fb", Dark: "#21202a"},
		Border:       AdaptiveColor{Light: "#b8b6c6", Dark: "#4a4858"},
		BorderFocus:  AdaptiveColor{Light: "#6d43d6", Dark: "#b39dff"},
		BorderMuted:  AdaptiveColor{Light: "#d8d6e2", Dark: "#323040"},
		UserLabel:    AdaptiveColor{Light: "#0b7285", Dark: "#5cd0e8"},
		ToolLabel:    AdaptiveColor{Light: "#3f51b5", Dark: "#9db2ff"},
		DiffAdded:    AdaptiveColor{Light: "#1f8a4c", Dark: "#5edb92"},
		DiffRemoved:  AdaptiveColor{Light: "#c23b3b", Dark: "#ff8087"},
		OverlayScrim: AdaptiveColor{Light: "#a8a6b4", Dark: "#6a6878"},
		Chrome:       ChromeSolid,
		BorderStyle:  lightBorderStyle(),
		Spacing:      NewSpacing(1, 2, 3, 4).WithLabel(1),
		Icons:        DefaultIcons(),
	}
}

// NoBackground explicitly opts a theme out of drawing a background.
func NoBackground() lipgloss.NoColor { return lipgloss.NoColor{} }

// IsTransparentBackground reports whether background is an explicit NoColor
// (transparent canvas).
func IsTransparentBackground(background color.Color) bool {
	switch background.(type) {
	case lipgloss.NoColor, *lipgloss.NoColor:
		return true
	default:
		return false
	}
}

// BackgroundPrefix returns the terminal sequence that paints the configured
// background, or an empty string when it is explicitly transparent.
// Lip Gloss v2 Render emits full-fidelity ANSI; Bubble Tea downsamples.
func BackgroundPrefix(background color.Color) string {
	if background == nil || IsTransparentBackground(background) {
		return ""
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
	t.Surface = resolveAdaptive(t.Surface, d.Surface)
	t.SurfaceFocus = resolveAdaptive(t.SurfaceFocus, d.SurfaceFocus)
	t.SurfaceMuted = resolveAdaptive(t.SurfaceMuted, d.SurfaceMuted)
	t.Border = resolveAdaptive(t.Border, d.Border)
	t.BorderFocus = resolveAdaptive(t.BorderFocus, d.BorderFocus)
	t.BorderMuted = resolveAdaptive(t.BorderMuted, d.BorderMuted)
	t.UserLabel = resolveAdaptive(t.UserLabel, d.UserLabel)
	t.ToolLabel = resolveAdaptive(t.ToolLabel, d.ToolLabel)
	t.DiffAdded = resolveAdaptive(t.DiffAdded, d.DiffAdded)
	t.DiffRemoved = resolveAdaptive(t.DiffRemoved, d.DiffRemoved)
	t.OverlayScrim = resolveAdaptive(t.OverlayScrim, d.OverlayScrim)
	t.Background = resolveBackground(t.Background, d.Background)
	t.Chrome = resolveChrome(t.Chrome)
	t.BorderStyle = resolveBorderStyle(t.BorderStyle)
	t.Spacing = resolveSpacing(t.Spacing, d.Spacing)
	t.Icons = resolveIcons(t.Icons, d.Icons)
	return t
}

func resolveAdaptive(c, fallback AdaptiveColor) AdaptiveColor {
	if c.Light == "" {
		c.Light = fallback.Light
	}
	if c.Dark == "" {
		c.Dark = fallback.Dark
	}
	return c
}

func resolveBackground(c, fallback color.Color) color.Color {
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
	case AdaptiveColor:
		return resolveBackgroundAdaptive(c, fallback)
	case *AdaptiveColor:
		if c == nil {
			return fallback
		}
		return resolveBackgroundAdaptive(*c, fallback)
	case compat.AdaptiveColor:
		return resolveBackgroundCompatAdaptive(c, fallback)
	case *compat.AdaptiveColor:
		if c == nil {
			return fallback
		}
		return resolveBackgroundCompatAdaptive(*c, fallback)
	case compat.CompleteColor:
		return resolveCompleteColor(c, fallbackColor(fallback))
	case *compat.CompleteColor:
		if c == nil {
			return fallback
		}
		return resolveCompleteColor(*c, fallbackColor(fallback))
	case compat.CompleteAdaptiveColor:
		light, dark := fallbackAdaptiveColors(fallback)
		return compat.CompleteAdaptiveColor{
			Light: resolveCompleteColor(c.Light, light),
			Dark:  resolveCompleteColor(c.Dark, dark),
		}
	case *compat.CompleteAdaptiveColor:
		if c == nil {
			return fallback
		}
		light, dark := fallbackAdaptiveColors(fallback)
		return compat.CompleteAdaptiveColor{
			Light: resolveCompleteColor(c.Light, light),
			Dark:  resolveCompleteColor(c.Dark, dark),
		}
	}
	return c
}

func resolveBackgroundAdaptive(c AdaptiveColor, fallback color.Color) color.Color {
	light, dark := fallbackAdaptiveColors(fallback)
	if c.Light == "" {
		c.Light = c.Dark
		if c.Light == "" {
			c.Light = colorToken(light.TrueColor)
		}
	}
	if c.Dark == "" {
		c.Dark = c.Light
		if c.Dark == "" {
			c.Dark = colorToken(dark.TrueColor)
		}
	}
	if c.Light == "" && c.Dark == "" {
		return fallback
	}
	return c
}

func resolveBackgroundCompatAdaptive(c compat.AdaptiveColor, fallback color.Color) color.Color {
	light, dark := fallbackAdaptiveColors(fallback)
	if c.Light == nil {
		c.Light = c.Dark
		if c.Light == nil {
			c.Light = light.TrueColor
		}
	}
	if c.Dark == nil {
		c.Dark = c.Light
		if c.Dark == nil {
			c.Dark = dark.TrueColor
		}
	}
	if c.Light == nil && c.Dark == nil {
		return fallback
	}
	return c
}

func resolveCompleteColor(c, fallback compat.CompleteColor) compat.CompleteColor {
	selected := firstColor(c.TrueColor, c.ANSI256, c.ANSI)
	if c.TrueColor == nil {
		c.TrueColor = selected
		if c.TrueColor == nil {
			c.TrueColor = fallback.TrueColor
		}
	}
	if c.ANSI256 == nil {
		c.ANSI256 = selected
		if c.ANSI256 == nil {
			c.ANSI256 = fallback.ANSI256
		}
	}
	if c.ANSI == nil {
		c.ANSI = selected
		if c.ANSI == nil {
			c.ANSI = fallback.ANSI
		}
	}
	return c
}

func firstColor(cs ...color.Color) color.Color {
	for _, c := range cs {
		if c != nil {
			return c
		}
	}
	return nil
}

func fallbackAdaptiveColors(c color.Color) (compat.CompleteColor, compat.CompleteColor) {
	switch c := c.(type) {
	case AdaptiveColor:
		return completeColor(c.Light), completeColor(c.Dark)
	case *AdaptiveColor:
		if c != nil {
			return completeColor(c.Light), completeColor(c.Dark)
		}
	case compat.AdaptiveColor:
		return completeFromColor(c.Light), completeFromColor(c.Dark)
	case *compat.AdaptiveColor:
		if c != nil {
			return completeFromColor(c.Light), completeFromColor(c.Dark)
		}
	case compat.CompleteAdaptiveColor:
		return c.Light, c.Dark
	case *compat.CompleteAdaptiveColor:
		if c != nil {
			return c.Light, c.Dark
		}
	}
	fallback := fallbackColor(c)
	return fallback, fallback
}

func fallbackColor(c color.Color) compat.CompleteColor {
	switch c := c.(type) {
	case AdaptiveColor:
		if compat.HasDarkBackground {
			return completeColor(c.Dark)
		}
		return completeColor(c.Light)
	case *AdaptiveColor:
		if c != nil {
			return fallbackColor(*c)
		}
	case compat.AdaptiveColor:
		if compat.HasDarkBackground {
			return completeFromColor(c.Dark)
		}
		return completeFromColor(c.Light)
	case *compat.AdaptiveColor:
		if c != nil {
			return fallbackColor(*c)
		}
	case compat.CompleteColor:
		return c
	case *compat.CompleteColor:
		if c != nil {
			return *c
		}
	case compat.CompleteAdaptiveColor:
		if compat.HasDarkBackground {
			return c.Dark
		}
		return c.Light
	case *compat.CompleteAdaptiveColor:
		if c != nil {
			return fallbackColor(*c)
		}
	case lipgloss.NoColor, *lipgloss.NoColor:
		return compat.CompleteColor{}
	default:
		if c != nil {
			return completeFromColor(c)
		}
	}
	return compat.CompleteColor{}
}

func completeColor(value string) compat.CompleteColor {
	if value == "" {
		return compat.CompleteColor{}
	}
	c := lipgloss.Color(value)
	return compat.CompleteColor{TrueColor: c, ANSI256: c, ANSI: c}
}

func completeFromColor(c color.Color) compat.CompleteColor {
	if c == nil {
		return compat.CompleteColor{}
	}
	return compat.CompleteColor{TrueColor: c, ANSI256: c, ANSI: c}
}

// colorToken best-effort string form for AdaptiveColor fill-from-fallback.
// Prefer hex when the color came from lipgloss.Color("#…"); else empty keeps
// resolve from falling through to Default.
func colorToken(c color.Color) string {
	if c == nil {
		return ""
	}
	if s, ok := colorHex(c); ok {
		return s
	}
	return ""
}

func colorHex(c color.Color) (string, bool) {
	if c == nil {
		return "", false
	}
	r, g, b, a := c.RGBA()
	if a == 0 {
		return "", false
	}
	// colorful / truecolor path: 16-bit components → 8-bit hex.
	return strings.ToLower(
		"#" +
			hex2(uint8(r>>8)) +
			hex2(uint8(g>>8)) +
			hex2(uint8(b>>8)),
	), true
}

func hex2(v uint8) string {
	const digits = "0123456789abcdef"
	return string([]byte{digits[v>>4], digits[v&0xf]})
}
