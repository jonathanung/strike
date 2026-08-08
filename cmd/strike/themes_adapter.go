package main

import (
	"image/color"
	"strings"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

// themesAdapter exposes the TUI theme catalog as host.Themes without leaking
// lipgloss/TUI types across the server boundary (WEBUI.11).
type themesAdapter struct{}

func (themesAdapter) List(workDir string) []host.ThemeInfo {
	entries := theme.Catalog(workDir)
	out := make([]host.ThemeInfo, 0, len(entries))
	for _, e := range entries {
		out = append(out, themeEntryToInfo(e))
	}
	return out
}

func (a themesAdapter) Get(workDir, id string) (host.ThemeInfo, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return host.ThemeInfo{}, false
	}
	for _, info := range a.List(workDir) {
		if info.ID == id {
			return info, true
		}
	}
	return host.ThemeInfo{}, false
}

func themeEntryToInfo(e theme.Entry) host.ThemeInfo {
	t := e.Theme.Resolve()
	return host.ThemeInfo{
		ID:         e.ID,
		Name:       e.Name,
		Appearance: "adaptive",
		Provenance: e.Provenance(),
		Overrode:   e.Overrode,
		Colors: host.ThemeColors{
			Text:         adaptivePair(t.Text),
			TextMuted:    adaptivePair(t.TextMuted),
			Accent:       adaptivePair(t.Accent),
			AccentAlt:    adaptivePair(t.AccentAlt),
			Highlight:    adaptivePair(t.Highlight),
			Success:      adaptivePair(t.Success),
			Warning:      adaptivePair(t.Warning),
			Error:        adaptivePair(t.Error),
			Danger:       adaptivePair(t.Danger),
			Background:   backgroundPair(t.Background),
			Surface:      adaptivePair(t.Surface),
			SurfaceFocus: adaptivePair(t.SurfaceFocus),
			SurfaceMuted: adaptivePair(t.SurfaceMuted),
			Border:       adaptivePair(t.Border),
			BorderFocus:  adaptivePair(t.BorderFocus),
			BorderMuted:  adaptivePair(t.BorderMuted),
			UserLabel:    adaptivePair(t.UserLabel),
			ToolLabel:    adaptivePair(t.ToolLabel),
			DiffAdded:    adaptivePair(t.DiffAdded),
			DiffRemoved:  adaptivePair(t.DiffRemoved),
			OverlayScrim: adaptivePair(t.OverlayScrim),
		},
	}
}

func adaptivePair(c theme.AdaptiveColor) host.ColorPair {
	return host.ColorPair{Light: strings.TrimSpace(c.Light), Dark: strings.TrimSpace(c.Dark)}
}

func backgroundPair(c color.Color) host.ColorPair {
	if c == nil || theme.IsTransparentBackground(c) {
		// Transparent canvas → fall back to stock Default background.
		d := theme.Default()
		return backgroundPair(d.Background)
	}
	switch v := c.(type) {
	case theme.AdaptiveColor:
		return adaptivePair(v)
	case *theme.AdaptiveColor:
		if v != nil {
			return adaptivePair(*v)
		}
	}
	// Non-adaptive solid: mirror into both sides when we can stringify.
	if s := colorHexString(c); s != "" {
		return host.ColorPair{Light: s, Dark: s}
	}
	d := theme.Default()
	return backgroundPair(d.Background)
}

func colorHexString(c color.Color) string {
	if c == nil {
		return ""
	}
	// lipgloss.Color is a string under the hood for hex tokens.
	type stringer interface{ String() string }
	if s, ok := c.(stringer); ok {
		v := strings.TrimSpace(s.String())
		if strings.HasPrefix(v, "#") {
			return v
		}
	}
	if s, ok := c.(interface {
		RGBA() (uint32, uint32, uint32, uint32)
	}); ok {
		r, g, b, a := s.RGBA()
		if a == 0 {
			return ""
		}
		return strings.ToLower(sprintfHex(uint8(r>>8), uint8(g>>8), uint8(b>>8)))
	}
	return ""
}

func sprintfHex(r, g, b uint8) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 7)
	out[0] = '#'
	out[1] = hexdigits[r>>4]
	out[2] = hexdigits[r&0xf]
	out[3] = hexdigits[g>>4]
	out[4] = hexdigits[g&0xf]
	out[5] = hexdigits[b>>4]
	out[6] = hexdigits[b&0xf]
	return string(out)
}
