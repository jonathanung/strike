package theme

import (
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

func TestThemeResolveBackgroundHandlesEveryTerminalColorForm(t *testing.T) {
	color := lipgloss.Color("#112233")
	ansi := lipgloss.ANSIColor(4)
	adaptive := lipgloss.AdaptiveColor{Light: "#112233", Dark: "#445566"}
	complete := lipgloss.CompleteColor{TrueColor: "#112233", ANSI256: "24", ANSI: "4"}
	profiles := lipgloss.CompleteAdaptiveColor{Light: complete, Dark: lipgloss.CompleteColor{TrueColor: "#445566", ANSI256: "25", ANSI: "5"}}
	var nilColor *lipgloss.Color
	var nilANSI *lipgloss.ANSIColor
	var nilAdaptive *lipgloss.AdaptiveColor
	var nilComplete *lipgloss.CompleteColor
	var nilProfiles *lipgloss.CompleteAdaptiveColor
	var nilNone *lipgloss.NoColor

	for _, tt := range []struct {
		name        string
		in          lipgloss.TerminalColor
		transparent bool
		wantType    reflect.Type
	}{
		{"color", color, false, reflect.TypeOf(color)},
		{"color pointer", &color, false, reflect.TypeOf(color)},
		{"ANSI color", ansi, false, reflect.TypeOf(ansi)},
		{"ANSI color pointer", &ansi, false, reflect.TypeOf(ansi)},
		{"adaptive color", adaptive, false, reflect.TypeOf(adaptive)},
		{"adaptive color pointer", &adaptive, false, reflect.TypeOf(adaptive)},
		{"complete color", complete, false, reflect.TypeOf(complete)},
		{"complete color pointer", &complete, false, reflect.TypeOf(complete)},
		{"complete adaptive color", profiles, false, reflect.TypeOf(profiles)},
		{"complete adaptive color pointer", &profiles, false, reflect.TypeOf(profiles)},
		{"no color", lipgloss.NoColor{}, true, reflect.TypeOf(lipgloss.NoColor{})},
		{"no color pointer", &lipgloss.NoColor{}, true, reflect.TypeOf(lipgloss.NoColor{})},
		{"typed nil color", nilColor, false, reflect.TypeOf(Default().Background)},
		{"typed nil ANSI color", nilANSI, false, reflect.TypeOf(Default().Background)},
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
			_, transparent := got.(lipgloss.NoColor)
			if transparent != tt.transparent {
				t.Errorf("transparent = %t, want %t", transparent, tt.transparent)
			}
		})
	}
}

func TestThemeResolveCopiesMutableBackgroundPointers(t *testing.T) {
	for _, tt := range []struct {
		name   string
		in     func() lipgloss.TerminalColor
		mutate func()
	}{
		func() struct {
			name   string
			in     func() lipgloss.TerminalColor
			mutate func()
		} {
			c := lipgloss.Color("#112233")
			return struct {
				name   string
				in     func() lipgloss.TerminalColor
				mutate func()
			}{"color", func() lipgloss.TerminalColor { return &c }, func() { c = "#445566" }}
		}(),
		func() struct {
			name   string
			in     func() lipgloss.TerminalColor
			mutate func()
		} {
			c := lipgloss.ANSIColor(1)
			return struct {
				name   string
				in     func() lipgloss.TerminalColor
				mutate func()
			}{"ANSI", func() lipgloss.TerminalColor { return &c }, func() { c = 2 }}
		}(),
		func() struct {
			name   string
			in     func() lipgloss.TerminalColor
			mutate func()
		} {
			c := lipgloss.AdaptiveColor{Light: "#112233", Dark: "#112233"}
			return struct {
				name   string
				in     func() lipgloss.TerminalColor
				mutate func()
			}{"adaptive", func() lipgloss.TerminalColor { return &c }, func() { c.Light = "#445566" }}
		}(),
		func() struct {
			name   string
			in     func() lipgloss.TerminalColor
			mutate func()
		} {
			c := lipgloss.CompleteColor{TrueColor: "#112233"}
			return struct {
				name   string
				in     func() lipgloss.TerminalColor
				mutate func()
			}{"complete", func() lipgloss.TerminalColor { return &c }, func() { c.TrueColor = "#445566" }}
		}(),
		func() struct {
			name   string
			in     func() lipgloss.TerminalColor
			mutate func()
		} {
			c := lipgloss.CompleteAdaptiveColor{Light: lipgloss.CompleteColor{TrueColor: "#112233"}}
			return struct {
				name   string
				in     func() lipgloss.TerminalColor
				mutate func()
			}{"complete adaptive", func() lipgloss.TerminalColor { return &c }, func() { c.Light.TrueColor = "#445566" }}
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
	got := (Theme{Background: lipgloss.CompleteAdaptiveColor{
		Light: lipgloss.CompleteColor{TrueColor: "#112233"},
		Dark:  lipgloss.CompleteColor{ANSI256: "99"},
	}}).Resolve().Background
	want := lipgloss.CompleteAdaptiveColor{
		Light: lipgloss.CompleteColor{TrueColor: "#112233", ANSI256: "#112233", ANSI: "#112233"},
		Dark:  lipgloss.CompleteColor{TrueColor: "99", ANSI256: "99", ANSI: "99"},
	}
	if got != want {
		t.Errorf("resolved CompleteAdaptiveColor = %#v, want %#v", got, want)
	}
}

func TestDefaultPaletteRolesArePopulatedAndReadable(t *testing.T) {
	th := Default()
	roles := map[string]lipgloss.AdaptiveColor{
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

func TestThemeResolveCompletesBackgroundTerminalColorVariants(t *testing.T) {
	for _, tt := range []struct {
		name    string
		profile termenv.Profile
		color   lipgloss.TerminalColor
		wantSGR string
	}{
		{"complete truecolor", termenv.TrueColor, lipgloss.CompleteColor{TrueColor: "#112233"}, "48;2;17;34;51"},
		{"complete ansi256", termenv.ANSI256, lipgloss.CompleteColor{ANSI256: "99"}, "48;5;99"},
		{"complete ansi zero", termenv.ANSI, lipgloss.CompleteColor{ANSI: "0"}, "40"},
		{"adaptive light only", termenv.TrueColor, lipgloss.AdaptiveColor{Light: "#112233"}, "48;2;17;34;51"},
		{"adaptive dark only", termenv.TrueColor, lipgloss.AdaptiveColor{Dark: "#112233"}, "48;2;17;34;51"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			saved := lipgloss.ColorProfile()
			lipgloss.SetColorProfile(tt.profile)
			t.Cleanup(func() { lipgloss.SetColorProfile(saved) })

			got := (Theme{Background: tt.color}).Resolve().Background
			if got == nil {
				t.Fatal("resolved background is nil")
			}
			if _, transparent := got.(lipgloss.NoColor); transparent {
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
	saved := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(saved) })

	complete := &lipgloss.CompleteColor{TrueColor: "#112233"}
	adaptive := &lipgloss.AdaptiveColor{Light: "#112233"}
	var nilComplete *lipgloss.CompleteColor
	var nilAdaptive *lipgloss.AdaptiveColor
	var nilNoColor *lipgloss.NoColor
	ansiBlack := lipgloss.ANSIColor(0)
	var nilANSI *lipgloss.ANSIColor
	for _, tt := range []struct {
		name string
		in   lipgloss.TerminalColor
	}{
		{"complete pointer", complete},
		{"adaptive pointer", adaptive},
		{"typed nil complete pointer", nilComplete},
		{"typed nil adaptive pointer", nilAdaptive},
		{"typed nil no-color pointer", nilNoColor},
		{"ANSI black pointer", &ansiBlack},
		{"typed nil ANSI pointer", nilANSI},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := (Theme{Background: tt.in}).Resolve().Background
			if got == nil {
				t.Fatal("resolved background is nil")
			}
			if _, transparent := got.(lipgloss.NoColor); transparent {
				t.Fatalf("resolved %T as transparent", tt.in)
			}
			if out := lipgloss.NewStyle().Background(got).Render("x"); out == "x" {
				t.Errorf("Background(%T) rendered without a solid background", got)
			}
		})
	}
}

func TestThemeResolvePreservesANSIBlackPointer(t *testing.T) {
	saved := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI)
	t.Cleanup(func() { lipgloss.SetColorProfile(saved) })

	black := lipgloss.ANSIColor(0)
	got := (Theme{Background: &black}).Resolve().Background
	if out := lipgloss.NewStyle().Background(got).Render("x"); !containsSGR(out, "40") {
		t.Errorf("ANSI black pointer rendered as %q, want ANSI black background", out)
	}
}

func TestThemeResolveZeroCompleteColorIsSolidInEveryProfile(t *testing.T) {
	for _, profile := range []termenv.Profile{termenv.TrueColor, termenv.ANSI256, termenv.ANSI} {
		t.Run(profile.Name(), func(t *testing.T) {
			saved := lipgloss.ColorProfile()
			lipgloss.SetColorProfile(profile)
			t.Cleanup(func() { lipgloss.SetColorProfile(saved) })

			got := (Theme{Background: lipgloss.CompleteColor{}}).Resolve().Background
			if got == nil {
				t.Fatal("zero CompleteColor resolved to nil")
			}
			if _, transparent := got.(lipgloss.NoColor); transparent {
				t.Fatal("zero CompleteColor resolved to transparent")
			}
			if out := lipgloss.NewStyle().Background(got).Render("x"); out == "x" {
				t.Errorf("zero CompleteColor rendered without a solid background under %s", profile.Name())
			}
		})
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

	roles := map[string]lipgloss.AdaptiveColor{
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
	if _, transparent := resolved.Background.(lipgloss.NoColor); transparent {
		t.Error("zero Theme Resolve made Background transparent; it must inherit the solid default")
	}

	for _, tt := range []struct {
		name string
		in   lipgloss.AdaptiveColor
		want lipgloss.AdaptiveColor
	}{
		{"light only", lipgloss.AdaptiveColor{Light: "#123456"}, lipgloss.AdaptiveColor{Light: "#123456", Dark: defaults.Accent.Dark}},
		{"dark only", lipgloss.AdaptiveColor{Dark: "#654321"}, lipgloss.AdaptiveColor{Light: defaults.Accent.Light, Dark: "#654321"}},
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
		background  lipgloss.TerminalColor
		transparent bool
	}{
		{"unset", nil, false},
		{"empty", lipgloss.Color(""), false},
		{"solid", lipgloss.Color("#123456"), false},
		{"transparent", NoBackground(), true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := (Theme{Background: tt.background}).Resolve().Background
			if got == nil {
				t.Fatal("resolved Background is nil")
			}
			_, transparent := got.(lipgloss.NoColor)
			if transparent != tt.transparent {
				t.Errorf("transparent = %t, want %t (%T)", transparent, tt.transparent, got)
			}
			if (tt.name == "unset" || tt.name == "empty") && !sameColor(got, defaultBackground) {
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

func sameColor(a, b lipgloss.TerminalColor) bool {
	ar, ag, ab, aa := a.RGBA()
	br, bg, bb, ba := b.RGBA()
	return ar == br && ag == bg && ab == bb && aa == ba
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
	if got := s.Danger.GetForeground(); got != th.Danger {
		t.Errorf("Danger style foreground = %v, want Danger %v", got, th.Danger)
	}
	if !s.DangerStrong.GetBold() || s.DangerStrong.GetForeground() != th.Danger {
		t.Errorf("DangerStrong = %+v, want bold Danger", s.DangerStrong)
	}
	if !s.AccentStrong.GetBold() || s.AccentStrong.GetForeground() != th.Accent {
		t.Errorf("AccentStrong = %+v, want bold Accent", s.AccentStrong)
	}
	if !s.Selected.GetBold() || s.Selected.GetForeground() != th.Highlight {
		t.Errorf("Selected = %+v, want bold Highlight", s.Selected)
	}
	if !s.UserLabel.GetBold() || s.UserLabel.GetForeground() != th.UserLabel {
		t.Errorf("UserLabel = %+v, want bold UserLabel", s.UserLabel)
	}
	if s.Input.GetForeground() != th.Text || s.InputPlaceholder.GetForeground() != th.TextMuted || s.InputPrompt.GetForeground() != th.Accent {
		t.Errorf("input widget styles do not use theme roles: %+v", s)
	}
	if got := s.DiffAdded.GetForeground(); got != th.DiffAdded {
		t.Errorf("DiffAdded style foreground = %v, want %v", got, th.DiffAdded)
	}
	if got := s.DiffRemoved.GetForeground(); got != th.DiffRemoved {
		t.Errorf("DiffRemoved style foreground = %v, want %v", got, th.DiffRemoved)
	}
}
