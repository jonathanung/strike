package theme

import (
	"image/color"
	"reflect"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/compat"
	"github.com/charmbracelet/colorprofile"
)

func TestThemeResolveBackgroundHandlesEveryColorForm(t *testing.T) {
	solid := lipgloss.Color("#112233")
	ansi := lipgloss.ANSIColor(4)
	adaptive := AdaptiveColor{Light: "#112233", Dark: "#445566"}
	complete := compat.CompleteColor{
		TrueColor: lipgloss.Color("#112233"),
		ANSI256:   lipgloss.Color("24"),
		ANSI:      lipgloss.Color("4"),
	}
	profiles := compat.CompleteAdaptiveColor{
		Light: complete,
		Dark: compat.CompleteColor{
			TrueColor: lipgloss.Color("#445566"),
			ANSI256:   lipgloss.Color("25"),
			ANSI:      lipgloss.Color("5"),
		},
	}
	var nilAdaptive *AdaptiveColor
	var nilComplete *compat.CompleteColor
	var nilProfiles *compat.CompleteAdaptiveColor
	var nilNone *lipgloss.NoColor

	for _, tt := range []struct {
		name        string
		in          color.Color
		transparent bool
		wantType    reflect.Type
	}{
		{"color", solid, false, reflect.TypeOf(solid)},
		{"ANSI color", ansi, false, reflect.TypeOf(ansi)},
		{"adaptive color", adaptive, false, reflect.TypeOf(adaptive)},
		{"adaptive color pointer", &adaptive, false, reflect.TypeOf(adaptive)},
		{"complete color", complete, false, reflect.TypeOf(complete)},
		{"complete color pointer", &complete, false, reflect.TypeOf(complete)},
		{"complete adaptive color", profiles, false, reflect.TypeOf(profiles)},
		{"complete adaptive color pointer", &profiles, false, reflect.TypeOf(profiles)},
		{"no color", lipgloss.NoColor{}, true, reflect.TypeOf(lipgloss.NoColor{})},
		{"no color pointer", &lipgloss.NoColor{}, true, reflect.TypeOf(lipgloss.NoColor{})},
		{"typed nil adaptive color", nilAdaptive, false, reflect.TypeOf(Default().Background)},
		{"typed nil complete color", nilComplete, false, reflect.TypeOf(Default().Background)},
		{"typed nil complete adaptive color", nilProfiles, false, reflect.TypeOf(Default().Background)},
		{"typed nil no color", nilNone, false, reflect.TypeOf(Default().Background)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := (Theme{Background: tt.in}).Resolve().Background
			if got == nil {
				t.Fatal("resolved background is nil")
			}
			if reflect.TypeOf(got) != tt.wantType {
				t.Errorf("resolved type = %T, want %v", got, tt.wantType)
			}
			if IsTransparentBackground(got) != tt.transparent {
				t.Errorf("transparent = %t, want %t", IsTransparentBackground(got), tt.transparent)
			}
		})
	}
}

func TestThemeResolveCopiesMutableBackgroundPointers(t *testing.T) {
	for _, tt := range []struct {
		name   string
		in     func() color.Color
		mutate func()
	}{
		func() struct {
			name   string
			in     func() color.Color
			mutate func()
		} {
			c := AdaptiveColor{Light: "#112233", Dark: "#112233"}
			return struct {
				name   string
				in     func() color.Color
				mutate func()
			}{"adaptive", func() color.Color { return &c }, func() { c.Light = "#445566" }}
		}(),
		func() struct {
			name   string
			in     func() color.Color
			mutate func()
		} {
			c := compat.CompleteColor{TrueColor: lipgloss.Color("#112233")}
			return struct {
				name   string
				in     func() color.Color
				mutate func()
			}{"complete", func() color.Color { return &c }, func() { c.TrueColor = lipgloss.Color("#445566") }}
		}(),
		func() struct {
			name   string
			in     func() color.Color
			mutate func()
		} {
			c := compat.CompleteAdaptiveColor{Light: compat.CompleteColor{TrueColor: lipgloss.Color("#112233")}}
			return struct {
				name   string
				in     func() color.Color
				mutate func()
			}{"complete adaptive", func() color.Color { return &c }, func() {
				c.Light.TrueColor = lipgloss.Color("#445566")
			}}
		}(),
	} {
		t.Run(tt.name, func(t *testing.T) {
			resolved := (Theme{Background: tt.in()}).Resolve()
			before := BackgroundPrefix(resolved.Background)
			tt.mutate()
			if after := BackgroundPrefix(resolved.Background); after != before {
				t.Errorf("resolved background changed after input mutation: before %q, after %q", before, after)
			}
		})
	}
}

func TestThemeResolveCompletesBothCompleteAdaptiveColorProfiles(t *testing.T) {
	got := (Theme{Background: compat.CompleteAdaptiveColor{
		Light: compat.CompleteColor{TrueColor: lipgloss.Color("#112233")},
		Dark:  compat.CompleteColor{ANSI256: lipgloss.Color("99")},
	}}).Resolve().Background
	want := compat.CompleteAdaptiveColor{
		Light: compat.CompleteColor{
			TrueColor: lipgloss.Color("#112233"),
			ANSI256:   lipgloss.Color("#112233"),
			ANSI:      lipgloss.Color("#112233"),
		},
		Dark: compat.CompleteColor{
			TrueColor: lipgloss.Color("99"),
			ANSI256:   lipgloss.Color("99"),
			ANSI:      lipgloss.Color("99"),
		},
	}
	gotC, ok := got.(compat.CompleteAdaptiveColor)
	if !ok {
		t.Fatalf("resolved type = %T, want CompleteAdaptiveColor", got)
	}
	if !sameColor(gotC.Light.TrueColor, want.Light.TrueColor) ||
		!sameColor(gotC.Dark.ANSI256, want.Dark.ANSI256) {
		t.Errorf("resolved CompleteAdaptiveColor = %#v, want %#v", got, want)
	}
}

func TestDefaultPaletteRolesArePopulatedAndReadable(t *testing.T) {
	th := Default()
	roles := map[string]AdaptiveColor{
		"Text": th.Text, "TextMuted": th.TextMuted, "Accent": th.Accent,
		"AccentAlt": th.AccentAlt, "Highlight": th.Highlight, "Success": th.Success,
		"Warning": th.Warning, "Error": th.Error, "Border": th.Border,
		"Danger":      th.Danger,
		"BorderFocus": th.BorderFocus, "BorderMuted": th.BorderMuted,
		"UserLabel": th.UserLabel, "ToolLabel": th.ToolLabel,
		"DiffAdded": th.DiffAdded, "DiffRemoved": th.DiffRemoved,
		"OverlayScrim": th.OverlayScrim,
		"Surface":      th.Surface, "SurfaceFocus": th.SurfaceFocus, "SurfaceMuted": th.SurfaceMuted,
	}
	for name, c := range roles {
		if c.Light == "" || c.Dark == "" {
			t.Errorf("%s missing a light/dark value: %+v", name, c)
		}
		if c.Light == c.Dark {
			t.Errorf("%s uses the same color for light and dark (%q); adaptive pairs should differ", name, c.Light)
		}
	}
	if th.Chrome != ChromeSolid {
		t.Errorf("default chrome = %v, want solid", th.Chrome)
	}
}

func TestThemeResolveCompletesBackgroundColorVariants(t *testing.T) {
	// v2 Render always emits full-fidelity ANSI; Bubble Tea downsamples later.
	// compat.CompleteColor.RGBA still selects by compat.Profile — pin TrueColor
	// so the TrueColor slot is what Render sees.
	saved := compat.Profile
	compat.Profile = colorprofile.TrueColor
	t.Cleanup(func() { compat.Profile = saved })

	for _, tt := range []struct {
		name    string
		color   color.Color
		wantSGR string
	}{
		{"complete truecolor", compat.CompleteColor{TrueColor: lipgloss.Color("#112233")}, "48;2;17;34;51"},
		{"adaptive light only", AdaptiveColor{Light: "#112233"}, "48;2;17;34;51"},
		{"adaptive dark only", AdaptiveColor{Dark: "#112233"}, "48;2;17;34;51"},
		{"plain color", lipgloss.Color("#112233"), "48;2;17;34;51"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := (Theme{Background: tt.color}).Resolve().Background
			if got == nil {
				t.Fatal("resolved background is nil")
			}
			if IsTransparentBackground(got) {
				t.Fatalf("resolved %T as transparent", tt.color)
			}
			out := lipgloss.NewStyle().Background(got).Render("x")
			if !containsSGR(out, tt.wantSGR) {
				t.Errorf("Background(%T) = %q, want background SGR %q", got, out, tt.wantSGR)
			}
		})
	}
}

func TestThemeResolveBackgroundPointersAndTypedNil(t *testing.T) {
	complete := &compat.CompleteColor{TrueColor: lipgloss.Color("#112233")}
	adaptive := &AdaptiveColor{Light: "#112233"}
	var nilComplete *compat.CompleteColor
	var nilAdaptive *AdaptiveColor
	var nilNoColor *lipgloss.NoColor
	for _, tt := range []struct {
		name string
		in   color.Color
	}{
		{"complete pointer", complete},
		{"adaptive pointer", adaptive},
		{"typed nil complete pointer", nilComplete},
		{"typed nil adaptive pointer", nilAdaptive},
		{"typed nil no-color pointer", nilNoColor},
		{"ANSI black", lipgloss.ANSIColor(0)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := (Theme{Background: tt.in}).Resolve().Background
			if got == nil {
				t.Fatal("resolved background is nil")
			}
			if IsTransparentBackground(got) {
				t.Fatalf("resolved %T as transparent", tt.in)
			}
			if out := lipgloss.NewStyle().Background(got).Render("x"); out == "x" {
				t.Errorf("Background(%T) rendered without a solid background", got)
			}
		})
	}
}

func TestThemeResolveZeroCompleteColorIsSolid(t *testing.T) {
	got := (Theme{Background: compat.CompleteColor{}}).Resolve().Background
	if got == nil {
		t.Fatal("zero CompleteColor resolved to nil")
	}
	if IsTransparentBackground(got) {
		t.Fatal("zero CompleteColor resolved to transparent")
	}
	// Empty CompleteColor falls back to Default background via resolveCompleteColor.
	if out := lipgloss.NewStyle().Background(got).Render("x"); out == "x" {
		t.Error("zero CompleteColor rendered without a solid background")
	}
}

func TestThemeResolveClampsNegativeSpacing(t *testing.T) {
	for _, tt := range []struct {
		name string
		in   Spacing
	}{
		{"constructor", NewSpacing(-1, -2, -3, -4)},
		{"with methods", Spacing{}.WithXS(-1).WithSM(-2).WithMD(-3).WithLG(-4)},
		{"direct fields", Spacing{XS: -1, SM: -2, MD: -3, LG: -4}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := (Theme{Spacing: tt.in}).Resolve().Spacing
			if got.XS != 0 || got.SM != 0 || got.MD != 0 || got.LG != 0 {
				t.Errorf("negative spacing resolved as %+v, want zero values", got)
			}
		})
	}
}

func containsSGR(s, want string) bool {
	return strings.Contains(s, "["+want+"m")
}

func TestThemeResolveCompletesZeroAndPartialThemes(t *testing.T) {
	defaults := Default()
	resolved := (Theme{}).Resolve()

	roles := map[string]AdaptiveColor{
		"Text": resolved.Text, "TextMuted": resolved.TextMuted,
		"Accent": resolved.Accent, "AccentAlt": resolved.AccentAlt,
		"Highlight": resolved.Highlight, "Success": resolved.Success,
		"Warning": resolved.Warning, "Error": resolved.Error,
		"Danger": resolved.Danger, "Border": resolved.Border,
		"BorderFocus": resolved.BorderFocus, "BorderMuted": resolved.BorderMuted,
		"UserLabel": resolved.UserLabel, "ToolLabel": resolved.ToolLabel,
		"DiffAdded": resolved.DiffAdded, "DiffRemoved": resolved.DiffRemoved,
		"OverlayScrim": resolved.OverlayScrim,
		"Surface":      resolved.Surface, "SurfaceFocus": resolved.SurfaceFocus, "SurfaceMuted": resolved.SurfaceMuted,
	}
	for name, role := range roles {
		if role.Light == "" || role.Dark == "" {
			t.Errorf("zero Theme Resolve left %s incomplete: %+v", name, role)
		}
	}
	if resolved.Chrome != ChromeSolid {
		t.Errorf("zero Theme Resolve chrome = %v, want solid", resolved.Chrome)
	}
	if resolved.Background == nil {
		t.Fatal("zero Theme Resolve left Background unset")
	}
	if IsTransparentBackground(resolved.Background) {
		t.Error("zero Theme Resolve made Background transparent; it must inherit the solid default")
	}

	for _, tt := range []struct {
		name string
		in   AdaptiveColor
		want AdaptiveColor
	}{
		{"light only", AdaptiveColor{Light: "#123456"}, AdaptiveColor{Light: "#123456", Dark: defaults.Accent.Dark}},
		{"dark only", AdaptiveColor{Dark: "#654321"}, AdaptiveColor{Light: defaults.Accent.Light, Dark: "#654321"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := (Theme{Accent: tt.in}).Resolve().Accent
			if got != tt.want {
				t.Errorf("resolved Accent = %+v, want %+v", got, tt.want)
			}
		})
	}

	icons := (Theme{Icons: Icons{Cursor: ">"}}).Resolve().Icons
	if icons.Cursor != ">" {
		t.Errorf("custom Cursor = %q, want >", icons.Cursor)
	}
	if icons.Bolt != defaults.Icons.Bolt || icons.Dot != defaults.Icons.Dot {
		t.Errorf("partial Icons did not inherit missing fields: %+v", icons)
	}
}

func TestThemeResolveBackgroundPreservesOnlyExplicitTransparency(t *testing.T) {
	defaultBackground := Default().Background
	cases := []struct {
		name        string
		background  color.Color
		transparent bool
	}{
		{"unset", nil, false},
		{"solid", lipgloss.Color("#123456"), false},
		{"transparent", NoBackground(), true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := (Theme{Background: tt.background}).Resolve().Background
			if got == nil {
				t.Fatal("resolved Background is nil")
			}
			if IsTransparentBackground(got) != tt.transparent {
				t.Errorf("transparent = %t, want %t (%T)", IsTransparentBackground(got), tt.transparent, got)
			}
			if tt.name == "unset" && !sameColor(got, defaultBackground) {
				t.Errorf("%s Background = %v, want default %v", tt.name, got, defaultBackground)
			}
			if tt.name == "solid" && !sameColor(got, tt.background) {
				t.Errorf("solid Background = %v, want preserved %v", got, tt.background)
			}
		})
	}
}

func TestThemeResolveChromeModes(t *testing.T) {
	if got := (Theme{}).Resolve().Chrome; got != ChromeSolid {
		t.Errorf("unset chrome = %v, want solid", got)
	}
	if got := (Theme{Chrome: ChromeSolid}).Resolve().Chrome; got != ChromeSolid {
		t.Errorf("solid chrome = %v", got)
	}
	if got := (Theme{Chrome: ChromeBordered}).Resolve().Chrome; got != ChromeBordered {
		t.Errorf("bordered chrome = %v", got)
	}
}

func TestThemeResolveBorderStyleAndSpacingFieldByField(t *testing.T) {
	defaults := Default()
	if defaults.BorderStyle.Weight != BorderWeightLight {
		t.Errorf("default border weight = %v, want light", defaults.BorderStyle.Weight)
	}
	if defaults.BorderStyle.TopLeft == "" || defaults.BorderStyle.Horizontal == "" || defaults.BorderStyle.Vertical == "" {
		t.Errorf("default border style is incomplete: %+v", defaults.BorderStyle)
	}

	heavy := (Theme{BorderStyle: BorderStyle{Weight: BorderWeightHeavy}}).Resolve().BorderStyle
	if heavy.Weight != BorderWeightHeavy || heavy.TopLeft != "┏" || heavy.Horizontal != "━" || heavy.Vertical != "┃" {
		t.Errorf("heavy border = %+v, want heavy preset", heavy)
	}

	custom := (Theme{BorderStyle: BorderStyle{
		TopLeft: "+", TopRight: "+", BottomLeft: "+", BottomRight: "+", Horizontal: "=", Vertical: "!",
	}}).Resolve().BorderStyle
	if custom.TopLeft != "+" || custom.Horizontal != "=" || custom.Vertical != "!" {
		t.Errorf("custom border glyphs were not preserved: %+v", custom)
	}
	partialBorder := (Theme{BorderStyle: BorderStyle{TopLeft: "+"}}).Resolve().BorderStyle
	if partialBorder.TopLeft != "+" || partialBorder.TopRight != defaults.BorderStyle.TopRight || partialBorder.Vertical != defaults.BorderStyle.Vertical {
		t.Errorf("partial border did not inherit missing glyphs: %+v", partialBorder)
	}

	invalid := (Theme{BorderStyle: BorderStyle{TopLeft: "", TopRight: "界", Horizontal: "--", Vertical: ""}}).Resolve().BorderStyle
	if invalid.TopLeft != defaults.BorderStyle.TopLeft || invalid.TopRight != defaults.BorderStyle.TopRight || invalid.Horizontal != defaults.BorderStyle.Horizontal || invalid.Vertical != defaults.BorderStyle.Vertical {
		t.Errorf("invalid border glyphs did not fall back: %+v", invalid)
	}

	unset := (Theme{}).Resolve().Spacing
	if unset.XS != defaults.Spacing.XS || unset.SM != defaults.Spacing.SM || unset.MD != defaults.Spacing.MD || unset.LG != defaults.Spacing.LG {
		t.Errorf("unset spacing = %+v, want defaults %+v", unset, defaults.Spacing)
	}
	zero := (Theme{Spacing: NewSpacing(0, 0, 0, 0)}).Resolve().Spacing
	if zero.XS != 0 || zero.SM != 0 || zero.MD != 0 || zero.LG != 0 {
		t.Errorf("explicit zero spacing was overwritten: %+v", zero)
	}
	partial := (Theme{Spacing: Spacing{}.WithSM(9)}).Resolve().Spacing
	if partial.SM != 9 || partial.XS != defaults.Spacing.XS || partial.MD != defaults.Spacing.MD || partial.LG != defaults.Spacing.LG {
		t.Errorf("partial spacing did not inherit field-by-field: %+v", partial)
	}
}

func sameColor(a, b color.Color) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	ar, ag, ab, aa := a.RGBA()
	br, bg, bb, ba := b.RGBA()
	return ar == br && ag == bg && ab == bb && aa == ba
}

func TestDefaultPaletteDistinguishesKeyRoles(t *testing.T) {
	th := Default()
	pairs := []struct {
		name string
		a, b AdaptiveColor
	}{
		{"Accent vs AccentAlt", th.Accent, th.AccentAlt},
		{"Border vs BorderFocus", th.Border, th.BorderFocus},
		{"Border vs BorderMuted", th.Border, th.BorderMuted},
		{"Success vs Error", th.Success, th.Error},
		{"Error vs Danger", th.Error, th.Danger},
	}
	for _, p := range pairs {
		if p.a == p.b {
			t.Errorf("%s should be visually distinct roles but are identical: %+v", p.name, p.a)
		}
	}
}

// TestDefaultPaletteE138Map locks the soft-bento multi-accent Default() map
// from issue #628 / docs/theme.md (21 color roles).
func TestDefaultPaletteE138Map(t *testing.T) {
	th := Default()
	want := map[string]AdaptiveColor{
		"Text":         {Light: "#1a1528", Dark: "#f3f1fa"},
		"TextMuted":    {Light: "#5c586e", Dark: "#9b99b0"},
		"Accent":       {Light: "#6d28d9", Dark: "#c4b5fd"},
		"AccentAlt":    {Light: "#0e7490", Dark: "#22d3ee"},
		"Highlight":    {Light: "#5b21b6", Dark: "#f5f3ff"},
		"Success":      {Light: "#15803d", Dark: "#4ade80"},
		"Warning":      {Light: "#b45309", Dark: "#fbbf24"},
		"Error":        {Light: "#e11d48", Dark: "#fb7185"},
		"Danger":       {Light: "#ea580c", Dark: "#fb923c"},
		"Surface":      {Light: "#f3eef9", Dark: "#232230"},
		"SurfaceFocus": {Light: "#e9e0f7", Dark: "#2e2c3e"},
		"SurfaceMuted": {Light: "#f8f5fc", Dark: "#1a1924"},
		"Border":       {Light: "#c4bfd4", Dark: "#4f4d63"},
		"BorderFocus":  {Light: "#6d28d9", Dark: "#c4b5fd"},
		"BorderMuted":  {Light: "#ddd8ea", Dark: "#2c2a3a"},
		"UserLabel":    {Light: "#0e7490", Dark: "#22d3ee"},
		"ToolLabel":    {Light: "#2563eb", Dark: "#7dd3fc"},
		"DiffAdded":    {Light: "#15803d", Dark: "#4ade80"},
		"DiffRemoved":  {Light: "#e11d48", Dark: "#fb7185"},
		"OverlayScrim": {Light: "#a8a3b8", Dark: "#7c7a90"},
	}
	got := map[string]AdaptiveColor{
		"Text": th.Text, "TextMuted": th.TextMuted, "Accent": th.Accent,
		"AccentAlt": th.AccentAlt, "Highlight": th.Highlight, "Success": th.Success,
		"Warning": th.Warning, "Error": th.Error, "Danger": th.Danger,
		"Surface": th.Surface, "SurfaceFocus": th.SurfaceFocus, "SurfaceMuted": th.SurfaceMuted,
		"Border": th.Border, "BorderFocus": th.BorderFocus, "BorderMuted": th.BorderMuted,
		"UserLabel": th.UserLabel, "ToolLabel": th.ToolLabel,
		"DiffAdded": th.DiffAdded, "DiffRemoved": th.DiffRemoved, "OverlayScrim": th.OverlayScrim,
	}
	for name, w := range want {
		if g := got[name]; g != w {
			t.Errorf("Default().%s = %#v, want %#v", name, g, w)
		}
	}
	bg, ok := th.Background.(AdaptiveColor)
	if !ok {
		t.Fatalf("Default().Background type = %T, want AdaptiveColor", th.Background)
	}
	if bg != (AdaptiveColor{Light: "#ffffff", Dark: "#14131c"}) {
		t.Errorf("Default().Background = %#v, want soft-bento ground", bg)
	}
	if th.Chrome != ChromeSolid {
		t.Errorf("Default().Chrome = %v, want ChromeSolid", th.Chrome)
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
		"FocusBar": ic.FocusBar, "FilterCursor": ic.FilterCursor, "ToolGuide": ic.ToolGuide,
	} {
		if glyph == "" {
			t.Errorf("DefaultIcons().%s is empty", name)
		}
	}
	if lipgloss.Width(ic.FocusBar) != 1 {
		t.Errorf("DefaultIcons().FocusBar width = %d, want 1 cell", lipgloss.Width(ic.FocusBar))
	}
}

func TestStylesDrawFromThemeRoles(t *testing.T) {
	th := Default()
	s := th.S()
	if got := s.Accent.GetForeground(); !sameColor(got, th.Accent) {
		t.Errorf("Accent style foreground = %v, want %v", got, th.Accent)
	}
	if got := s.Muted.GetForeground(); !sameColor(got, th.TextMuted) {
		t.Errorf("Muted style foreground = %v, want %v", got, th.TextMuted)
	}
	if !s.Title.GetBold() {
		t.Error("Title style should be bold")
	}
	if got := s.Title.GetForeground(); !sameColor(got, th.Accent) {
		t.Errorf("Title style foreground = %v, want Accent %v", got, th.Accent)
	}
	if got := s.Danger.GetForeground(); !sameColor(got, th.Danger) {
		t.Errorf("Danger style foreground = %v, want Danger %v", got, th.Danger)
	}
	if !s.DangerStrong.GetBold() || !sameColor(s.DangerStrong.GetForeground(), th.Danger) {
		t.Errorf("DangerStrong = %+v, want bold Danger", s.DangerStrong)
	}
	if !s.AccentStrong.GetBold() || !sameColor(s.AccentStrong.GetForeground(), th.Accent) {
		t.Errorf("AccentStrong = %+v, want bold Accent", s.AccentStrong)
	}
	if !s.Selected.GetBold() || !sameColor(s.Selected.GetForeground(), th.Highlight) {
		t.Errorf("Selected = %+v, want bold Highlight", s.Selected)
	}
	if !s.UserLabel.GetBold() || !sameColor(s.UserLabel.GetForeground(), th.UserLabel) {
		t.Errorf("UserLabel = %+v, want bold UserLabel", s.UserLabel)
	}
	if !sameColor(s.Input.GetForeground(), th.Text) || !sameColor(s.InputPlaceholder.GetForeground(), th.TextMuted) || !sameColor(s.InputPrompt.GetForeground(), th.Accent) {
		t.Errorf("input widget styles do not use theme roles: %+v", s)
	}
	if got := s.DiffAdded.GetForeground(); !sameColor(got, th.DiffAdded) {
		t.Errorf("DiffAdded style foreground = %v, want %v", got, th.DiffAdded)
	}
	if got := s.DiffRemoved.GetForeground(); !sameColor(got, th.DiffRemoved) {
		t.Errorf("DiffRemoved style foreground = %v, want %v", got, th.DiffRemoved)
	}
}

func TestAdaptiveColorRGBARespectsCompatHasDarkBackground(t *testing.T) {
	c := AdaptiveColor{Light: "#112233", Dark: "#445566"}
	saved := compat.HasDarkBackground
	t.Cleanup(func() { compat.HasDarkBackground = saved })

	compat.HasDarkBackground = false
	lr, lg, lb, _ := c.RGBA()
	compat.HasDarkBackground = true
	dr, dg, db, _ := c.RGBA()
	if lr == dr && lg == dg && lb == db {
		t.Fatalf("light and dark RGBA identical: light=%d,%d,%d dark=%d,%d,%d", lr, lg, lb, dr, dg, db)
	}
}
