package theme

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestDefaultPaletteRolesArePopulatedAndReadable(t *testing.T) {
	th := Default()
	roles := map[string]lipgloss.AdaptiveColor{
		"Text": th.Text, "TextMuted": th.TextMuted, "Accent": th.Accent,
		"AccentAlt": th.AccentAlt, "Highlight": th.Highlight, "Success": th.Success,
		"Warning": th.Warning, "Error": th.Error, "Border": th.Border,
		"BorderFocus": th.BorderFocus, "BorderMuted": th.BorderMuted,
		"UserLabel": th.UserLabel, "ToolLabel": th.ToolLabel,
		"DiffAdded": th.DiffAdded, "DiffRemoved": th.DiffRemoved,
	}
	for name, c := range roles {
		if c.Light == "" || c.Dark == "" {
			t.Errorf("%s missing a light/dark value: %+v", name, c)
		}
		if c.Light == c.Dark {
			t.Errorf("%s uses the same color for light and dark (%q); adaptive pairs should differ", name, c.Light)
		}
	}
}

func TestDefaultPaletteDistinguishesKeyRoles(t *testing.T) {
	th := Default()
	pairs := []struct {
		name string
		a, b lipgloss.AdaptiveColor
	}{
		{"Accent vs AccentAlt", th.Accent, th.AccentAlt},
		{"Border vs BorderFocus", th.Border, th.BorderFocus},
		{"Border vs BorderMuted", th.Border, th.BorderMuted},
		{"Success vs Error", th.Success, th.Error},
	}
	for _, p := range pairs {
		if p.a == p.b {
			t.Errorf("%s should be visually distinct roles but are identical: %+v", p.name, p.a)
		}
	}
}

func TestDefaultThemeCarriesDefaultIcons(t *testing.T) {
	if Default().Icons != DefaultIcons() {
		t.Error("Default() must embed DefaultIcons() so views get glyphs")
	}
	ic := DefaultIcons()
	for name, glyph := range map[string]string{
		"Prompt": ic.Prompt, "Assistant": ic.Assistant, "Tool": ic.Tool,
		"OK": ic.OK, "Err": ic.Err, "Info": ic.Info, "Agent": ic.Agent,
		"Bolt": ic.Bolt, "Dot": ic.Dot, "Cursor": ic.Cursor,
	} {
		if glyph == "" {
			t.Errorf("DefaultIcons().%s is empty", name)
		}
	}
}

func TestStylesDrawFromThemeRoles(t *testing.T) {
	th := Default()
	s := th.S()
	if got := s.Accent.GetForeground(); got != th.Accent {
		t.Errorf("Accent style foreground = %v, want %v", got, th.Accent)
	}
	if got := s.Muted.GetForeground(); got != th.TextMuted {
		t.Errorf("Muted style foreground = %v, want %v", got, th.TextMuted)
	}
	if !s.Title.GetBold() {
		t.Error("Title style should be bold")
	}
	if got := s.Title.GetForeground(); got != th.Accent {
		t.Errorf("Title style foreground = %v, want Accent %v", got, th.Accent)
	}
}
