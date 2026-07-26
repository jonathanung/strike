package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

func firstLine(s string) string { return strings.SplitN(s, "\n", 2)[0] }

func lastLine(s string) string {
	lines := strings.Split(s, "\n")
	return lines[len(lines)-1]
}

func borderedTheme() theme.Theme {
	th := theme.Default()
	th.Chrome = theme.ChromeBordered
	return th
}

func TestPanelSolidEmbedsTitleWithoutBoxDrawing(t *testing.T) {
	out := Panel(theme.Default(), PanelOpts{Title: "session", Width: 40}, "body")
	top := ansi.Strip(firstLine(out))
	if !strings.Contains(top, "session") {
		t.Errorf("top chrome missing title: %q", top)
	}
	if strings.ContainsAny(top, "╭╮╰╯│") {
		t.Errorf("solid default used box-drawing chrome: %q", top)
	}
	if w := lipgloss.Width(firstLine(out)); w != 40 {
		t.Errorf("top width = %d, want 40", w)
	}
	if !strings.Contains(ansi.Strip(out), "body") {
		t.Errorf("body missing: %q", out)
	}
}

func TestPanelSolidEmbedsFooter(t *testing.T) {
	out := Panel(theme.Default(), PanelOpts{Title: "keys", Footer: "esc close", Width: 40}, "body")
	bottom := ansi.Strip(lastLine(out))
	if !strings.Contains(bottom, "esc close") {
		t.Errorf("bottom chrome missing footer: %q", bottom)
	}
	if strings.ContainsAny(bottom, "╭╮╰╯│") {
		t.Errorf("solid footer used box-drawing: %q", bottom)
	}
	if w := lipgloss.Width(lastLine(out)); w != 40 {
		t.Errorf("bottom width = %d, want 40", w)
	}
}

func TestPanelBorderedEmbedsTitleInTopBorder(t *testing.T) {
	out := Panel(borderedTheme(), PanelOpts{Title: "session", Width: 40}, "body")
	top := firstLine(out)
	if !strings.HasPrefix(top, "╭─ session ") {
		t.Errorf("top border does not embed the title: %q", top)
	}
	if !strings.HasSuffix(top, "╮") {
		t.Errorf("top border does not close with ╮: %q", top)
	}
	if w := lipgloss.Width(top); w != 40 {
		t.Errorf("top border width = %d, want 40", w)
	}
}

func TestPanelBorderedEmbedsFooterInBottomBorder(t *testing.T) {
	out := Panel(borderedTheme(), PanelOpts{Title: "keys", Footer: "esc close", Width: 40}, "body")
	bottom := lastLine(out)
	if !strings.HasPrefix(bottom, "╰─ esc close ") {
		t.Errorf("bottom border does not embed the footer: %q", bottom)
	}
	if !strings.HasSuffix(bottom, "╯") {
		t.Errorf("bottom border does not close with ╯: %q", bottom)
	}
}

func TestPanelRendersExactlyRequestedWidth(t *testing.T) {
	body := "the quick brown fox jumps over the lazy dog, then keeps on going for a while"
	for _, th := range []theme.Theme{theme.Default(), borderedTheme()} {
		for _, width := range []int{3, 4, 5, 6, 8, 12, 24, 40, 80, 120} {
			out := Panel(th, PanelOpts{Title: "session", Footer: "esc", Width: width}, body)
			for i, line := range strings.Split(out, "\n") {
				if w := lipgloss.Width(line); w != width {
					t.Errorf("chrome=%v width %d: line %d = %d cells, want exactly %d\n%q", th.Chrome, width, i, w, width, line)
				}
			}
		}
	}
}

func TestPanelNeverExceedsWidthWithWideRunesAndLongTitle(t *testing.T) {
	body := strings.Repeat("界", 60)
	title := "an exceedingly long panel title that must be truncated to fit"
	for _, width := range []int{6, 10, 20, 30} {
		out := Panel(theme.Default(), PanelOpts{Title: title, Width: width}, body)
		if w := lipgloss.Width(out); w != width {
			t.Errorf("width %d: rendered width = %d, want %d", width, w, width)
		}
	}
}

func TestPanelDegradesAtTinyWidthsWithoutPanic(t *testing.T) {
	for _, width := range []int{0, 1, 2} {
		out := Panel(theme.Default(), PanelOpts{Title: "x", Width: width}, "content")
		if w := lipgloss.Width(out); w > max(width, 0) {
			t.Errorf("width %d: rendered width = %d, want <= %d", width, w, max(width, 0))
		}
		if strings.Contains(ansi.Strip(out), "x") && width == 0 {
			t.Errorf("width 0 should be empty, got %q", out)
		}
	}
}

func TestPanelFixedHeightClampsAndPads(t *testing.T) {
	tall := strings.Join([]string{"1", "2", "3", "4", "5", "6"}, "\n")
	clamped := Panel(theme.Default(), PanelOpts{Title: "t", Width: 20, Height: 5}, tall)
	if got := lineCount(clamped); got != 5 {
		t.Errorf("clamped height = %d lines, want 5", got)
	}
	padded := Panel(theme.Default(), PanelOpts{Title: "t", Width: 20, Height: 8}, "just one line")
	if got := lineCount(padded); got != 8 {
		t.Errorf("padded height = %d lines, want 8", got)
	}
	for i, line := range strings.Split(padded, "\n") {
		if w := lipgloss.Width(line); w != 20 {
			t.Errorf("padded line %d width = %d, want 20", i, w)
		}
	}
}

func TestPanelTinyFixedHeightsDegradeWithoutPhantomBorderRows(t *testing.T) {
	th := theme.Default()
	for _, tt := range []struct {
		name          string
		width, height int
		body          string
		wantRows      int
		wantChrome    bool
	}{
		{"height one is a one-row borderless canvas", 12, 1, "界x", 1, false},
		{"height two keeps only exact chrome bars", 12, 2, "body", 2, true},
		{"height zero retains natural panel height", 12, 0, "one\ntwo", 4, true},
		{"narrow height one stays chrome-less", 2, 1, "界x", 1, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			out := Panel(th, PanelOpts{Title: "title", Footer: "footer", Width: tt.width, Height: tt.height}, tt.body)
			lines := strings.Split(out, "\n")
			if len(lines) != tt.wantRows {
				t.Fatalf("rows = %d, want %d: %q", len(lines), tt.wantRows, out)
			}
			for i, line := range lines {
				if got := lipgloss.Width(line); got != tt.width {
					t.Errorf("row %d width = %d, want exact %d: %q", i, got, tt.width, line)
				}
			}
			plain := ansi.Strip(out)
			hasTitle := strings.Contains(plain, "title")
			if hasTitle != tt.wantChrome {
				t.Errorf("chrome title present = %v, want %v: %q", hasTitle, tt.wantChrome, plain)
			}
			if tt.height == 2 && strings.Contains(plain, "body") {
				t.Errorf("height-two panel retained a body row: %q", plain)
			}
			if strings.ContainsAny(plain, "╭╮╰╯│") {
				t.Errorf("solid panel used box-drawing: %q", plain)
			}
		})
	}
}

func TestPanelBorderColorSelectionPrecedence(t *testing.T) {
	th := borderedTheme()
	tests := []struct {
		name string
		opts PanelOpts
		want lipgloss.AdaptiveColor
	}{
		{"default", PanelOpts{}, th.Border},
		{"focused", PanelOpts{Focused: true}, th.BorderFocus},
		{"dim", PanelOpts{Dim: true}, th.BorderMuted},
		{"tone override", PanelOpts{Tone: ToneWarning}, th.Warning},
		{"tone beats focus", PanelOpts{Focused: true, Tone: ToneError}, th.Error},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := panelBorderColor(th, tt.opts); got != tt.want {
				t.Errorf("panelBorderColor = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPanelSurfaceColorSelectionPrecedence(t *testing.T) {
	th := theme.Default()
	tests := []struct {
		name string
		opts PanelOpts
		want lipgloss.AdaptiveColor
	}{
		{"default", PanelOpts{}, th.Surface},
		{"focused", PanelOpts{Focused: true}, th.SurfaceFocus},
		{"dim", PanelOpts{Dim: true}, th.SurfaceMuted},
		{"tone uses focus surface", PanelOpts{Tone: ToneWarning}, th.SurfaceFocus},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := panelSurfaceColor(th, tt.opts)
			ac, ok := got.(lipgloss.AdaptiveColor)
			if !ok || ac != tt.want {
				t.Errorf("panelSurfaceColor = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPanelFocusStateChangesRenderedChrome(t *testing.T) {
	saved := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(saved) })

	th := theme.Default()
	focused := Panel(th, PanelOpts{Title: "x", Width: 24, Focused: true}, "body")
	unfocused := Panel(th, PanelOpts{Title: "x", Width: 24}, "body")
	dim := Panel(th, PanelOpts{Title: "x", Width: 24, Dim: true}, "body")

	if focused == unfocused {
		t.Error("focused and unfocused panels render identically; surface not applied")
	}
	if unfocused == dim {
		t.Error("dim and normal panels render identically; SurfaceMuted not applied")
	}
	for name, out := range map[string]string{"focused": focused, "unfocused": unfocused, "dim": dim} {
		if w := lipgloss.Width(out); w != 24 {
			t.Errorf("%s panel width = %d with color enabled, want 24", name, w)
		}
	}
}

func TestInnerWidthAccountsForSolidChromeAndPadding(t *testing.T) {
	// Default solid: no vertical frame columns.
	tests := map[int]int{0: 0, 2: 2, 4: 2, 5: 3, 6: 4, 40: 38, 80: 78}
	for width, want := range tests {
		if got := InnerWidth(width); got != want {
			t.Errorf("InnerWidth(%d) = %d, want %d", width, got, want)
		}
	}
	// Bordered retains the classic budget.
	bt := borderedTheme()
	bordered := map[int]int{0: 0, 2: 2, 4: 2, 5: 3, 6: 2, 40: 36, 80: 76}
	for width, want := range bordered {
		if got := PanelInnerWidth(bt, width); got != want {
			t.Errorf("bordered PanelInnerWidth(%d) = %d, want %d", width, got, want)
		}
	}
}

func TestPanelInnerHeightAccountsForBorderOnlyWhenItFits(t *testing.T) {
	for _, tt := range []struct {
		name          string
		width, height int
		want          int
	}{
		{"zero width", 0, 40, 0},
		{"negative width", -1, 40, 0},
		{"one column unbordered", 1, 40, 40},
		{"two columns unbordered", 2, 40, 40},
		{"three columns chrome", 3, 40, 38},
		{"normal chrome", 80, 40, 38},
		{"zero height", 80, 0, 0},
		{"negative height", 80, -1, 0},
		{"height one degrades to a borderless body row", 80, 1, 1},
		{"chrome height two has no body", 80, 2, 0},
		{"narrow height one is its own body row", 2, 1, 1},
		{"narrow height two is two body rows", 2, 2, 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := PanelInnerHeight(tt.width, tt.height); got != tt.want {
				t.Errorf("PanelInnerHeight(%d, %d) = %d, want %d", tt.width, tt.height, got, tt.want)
			}
		})
	}
}

func TestPanelInnerWidthMatchesRenderedBodyBudgetAndLegacyInnerWidth(t *testing.T) {
	th := theme.Default()
	for _, width := range []int{0, 1, 2, 3, 5, 6, 20, 40} {
		t.Run("width", func(t *testing.T) {
			budget := PanelInnerWidth(th, width)
			out := Panel(th, PanelOpts{Width: width}, strings.Repeat("x", budget+1))
			for _, line := range strings.Split(out, "\n") {
				plain := ansi.Strip(line)
				if strings.Contains(plain, "x") && strings.Count(plain, "x") > budget {
					t.Errorf("width %d: body exceeds PanelInnerWidth %d: %q", width, budget, plain)
				}
			}
		})
	}

	for width, want := range map[int]int{0: 0, 2: 2, 4: 2, 5: 3, 6: 4, 40: 38, 80: 78} {
		if got := InnerWidth(width); got != want {
			t.Errorf("legacy InnerWidth(%d) = %d, want %d", width, got, want)
		}
	}
}

func TestPanelSpacingSupportsExplicitZeroAndCustomValues(t *testing.T) {
	zero := theme.Default()
	zero.Spacing = theme.NewSpacing(0, 0, 0, 0)
	custom := theme.Default()
	custom.Spacing = theme.NewSpacing(1, 3, 5, 7)

	for name, th := range map[string]theme.Theme{"zero": zero, "custom": custom} {
		t.Run(name, func(t *testing.T) {
			const width, height = 30, 7
			out := Panel(th, PanelOpts{Width: width, Height: height}, "body")
			if got := lineCount(out); got != height {
				t.Errorf("height = %d, want %d", got, height)
			}
			for i, line := range strings.Split(out, "\n") {
				if got := lipgloss.Width(line); got != width {
					t.Errorf("line %d width = %d, want %d", i, got, width)
				}
			}
		})
	}
	if PanelInnerWidth(zero, 30) <= PanelInnerWidth(custom, 30) {
		t.Errorf("explicit-zero spacing body budget = %d, want larger than custom spacing budget %d", PanelInnerWidth(zero, 30), PanelInnerWidth(custom, 30))
	}
}

func TestPanelUsesValidCustomBordersAndFallsBackFromInvalidGlyphs(t *testing.T) {
	custom := borderedTheme()
	custom.BorderStyle = theme.BorderStyle{
		TopLeft: "+", TopRight: "+", BottomLeft: "+", BottomRight: "+", Horizontal: "=", Vertical: "!",
	}
	out := Panel(custom, PanelOpts{Title: "x", Width: 20}, "body")
	if top := firstLine(out); !strings.HasPrefix(top, "+") || !strings.HasSuffix(top, "+") || !strings.Contains(top, "=") {
		t.Errorf("custom top border = %q", top)
	}
	if body := strings.Split(out, "\n")[1]; !strings.HasPrefix(body, "!") || !strings.HasSuffix(body, "!") {
		t.Errorf("custom vertical border = %q", body)
	}

	invalid := borderedTheme()
	invalid.BorderStyle = theme.BorderStyle{TopLeft: "界", Horizontal: "--"}
	out = Panel(invalid, PanelOpts{Width: 20}, "body")
	if top := firstLine(out); strings.Contains(top, "界") || strings.Contains(top, "--") || !strings.HasPrefix(top, "╭") {
		t.Errorf("invalid custom border did not fall back: %q", top)
	}
	for i, line := range strings.Split(out, "\n") {
		if got := lipgloss.Width(line); got != 20 {
			t.Errorf("fallback line %d width = %d, want 20", i, got)
		}
	}
}

func TestPanelRejectsControlCharacterBorderGlyphs(t *testing.T) {
	th := borderedTheme()
	th.BorderStyle = theme.BorderStyle{
		TopLeft: "\n", TopRight: "\r", BottomLeft: "x\n", BottomRight: "x\r", Horizontal: "\n", Vertical: "\r",
	}
	out := Panel(th, PanelOpts{Width: 20, Height: 5}, "body")
	lines := strings.Split(out, "\n")
	if len(lines) != 5 {
		t.Fatalf("Panel with control-character border glyphs returned %d rows, want 5: %q", len(lines), out)
	}
	for i, line := range lines {
		if got := lipgloss.Width(line); got != 20 {
			t.Errorf("row %d width = %d, want 20: %q", i, got, line)
		}
		if strings.ContainsRune(line, '\r') {
			t.Errorf("row %d retained a carriage return border glyph: %q", i, line)
		}
	}
	if !strings.HasPrefix(lines[0], "╭") || !strings.HasSuffix(lines[0], "╮") {
		t.Errorf("invalid border glyphs did not fall back to the light preset: %q", lines[0])
	}
}

func TestBorderlessPanelUsesExactCanvasWithoutChromeOrPadding(t *testing.T) {
	for _, tt := range []struct {
		name          string
		width, height int
		body          string
	}{
		{"fixed unicode", 8, 3, "界x\nsecond\nextra"},
		{"tiny", 1, 2, "界\nxyz"},
		{"zero width", 0, 2, "body"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			out := Panel(theme.Default(), PanelOpts{Title: "title", Footer: "footer", Width: tt.width, Height: tt.height, Borderless: true}, tt.body)
			if tt.width == 0 {
				if out != "" {
					t.Errorf("zero-width borderless panel = %q, want empty", out)
				}
				return
			}
			lines := strings.Split(out, "\n")
			if len(lines) != tt.height {
				t.Fatalf("borderless rows = %d, want %d: %q", len(lines), tt.height, out)
			}
			for i, line := range lines {
				if got := lipgloss.Width(line); got != tt.width {
					t.Errorf("row %d width = %d, want %d: %q", i, got, tt.width, line)
				}
			}
			if strings.ContainsAny(out, "╭╮╰╯│─") || strings.Contains(out, "title") || strings.Contains(out, "footer") {
				t.Errorf("borderless panel retained chrome: %q", out)
			}
		})
	}
	defaultPanel := Panel(theme.Default(), PanelOpts{Title: "title", Footer: "footer", Width: 20, Height: 4}, "body")
	plain := ansi.Strip(defaultPanel)
	if !strings.Contains(plain, "title") || !strings.Contains(plain, "footer") || !strings.Contains(plain, "body") {
		t.Errorf("default solid Panel missing chrome labels: %q", plain)
	}
	if strings.ContainsAny(plain, "╭╮╰╯│") {
		t.Errorf("default solid Panel used box-drawing: %q", plain)
	}
}

func TestDefaultThemeChromeIsSolid(t *testing.T) {
	th := theme.Default().Resolve()
	if th.Chrome != theme.ChromeSolid {
		t.Errorf("default chrome = %v, want solid", th.Chrome)
	}
	if (theme.Theme{}).Resolve().Chrome != theme.ChromeSolid {
		t.Error("zero theme did not resolve chrome to solid")
	}
}
