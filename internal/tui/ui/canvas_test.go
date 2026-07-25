package ui

import (
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

func TestCanvasFitsEveryCellAtCommonScreenSizes(t *testing.T) {
	th := theme.Default()
	for _, size := range []struct{ width, height int }{{80, 24}, {120, 40}} {
		t.Run(strconv.Itoa(size.width)+"x"+strconv.Itoa(size.height), func(t *testing.T) {
			out := Canvas(th, size.width, size.height, "first\n界界\nlast")
			assertCanvasSize(t, out, size.width, size.height)
		})
	}
}

func TestCanvasPaintsContentPaddingAndBlankRows(t *testing.T) {
	setTrueColor(t)
	th := theme.Default()
	th.Background = lipgloss.Color("#112233")
	styled := lipgloss.NewStyle().Foreground(lipgloss.Color("#eeddcc")).Render("nested")
	out := Canvas(th, 12, 4, "x\n"+styled+" plain")

	assertCanvasSize(t, out, 12, 4)
	assertSolidBackground(t, out, "48;2;17;34;51")
}

func TestCanvasUsesResolvedCompleteBackgroundInSupportedProfiles(t *testing.T) {
	for _, tt := range []struct {
		name    string
		profile termenv.Profile
		wantSGR string
	}{
		{"ansi256", termenv.ANSI256, "48;5;99"},
		{"ansi zero", termenv.ANSI, "40"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			saved := lipgloss.ColorProfile()
			lipgloss.SetColorProfile(tt.profile)
			t.Cleanup(func() { lipgloss.SetColorProfile(saved) })
			th := theme.Default()
			th.Background = lipgloss.CompleteColor{ANSI256: "99", ANSI: "0"}
			if out := Canvas(th, 4, 1, "x"); !strings.Contains(out, "["+tt.wantSGR+"m") {
				t.Errorf("Canvas(%s) = %q, want background %q", tt.name, out, tt.wantSGR)
			}
		})
	}
}

func TestCanvasHandlesANSIBlackPointersWithoutLosingItsSolidBackground(t *testing.T) {
	for _, tt := range []struct {
		name       string
		background lipgloss.TerminalColor
		want       string
	}{
		{"ANSI black pointer", ansiColorPointer(0), "40"},
		{"typed nil ANSI pointer falls back", typedNilANSIColor(), "40"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			saved := lipgloss.ColorProfile()
			lipgloss.SetColorProfile(termenv.ANSI)
			t.Cleanup(func() { lipgloss.SetColorProfile(saved) })
			th := theme.Default()
			th.Background = tt.background

			out := Canvas(th, 4, 1, "x")
			assertCanvasSize(t, out, 4, 1)
			if !strings.Contains(out, "["+tt.want+"m") {
				t.Errorf("Canvas background = %q, want solid ANSI background %q", out, tt.want)
			}
		})
	}
}

func ansiColorPointer(value lipgloss.ANSIColor) lipgloss.TerminalColor { return &value }

func typedNilANSIColor() lipgloss.TerminalColor {
	var color *lipgloss.ANSIColor
	return color
}

func TestCanvasOverflowRetainsFinalRows(t *testing.T) {
	setTrueColor(t)
	th := theme.Default()
	th.Background = lipgloss.Color("#112233")
	out := Canvas(th, 20, 2, "top content\nmiddle content\nbottom danger\nbottom footer")

	assertCanvasSize(t, out, 20, 2)
	assertSolidBackground(t, out, "48;2;17;34;51")
	if got, want := ansi.Strip(out), "bottom danger       \nbottom footer       "; got != want {
		t.Errorf("Canvas overflow = %q, want final rows %q", got, want)
	}
}

func TestCanvasRestoresBackgroundAfterResets(t *testing.T) {
	setTrueColor(t)
	th := theme.Default()
	th.Background = lipgloss.Color("#112233")
	body := "before\x1b[0mafter-reset\x1b[49mafter-background-reset"
	out := Canvas(th, 48, 1, body)

	assertSolidBackground(t, out, "48;2;17;34;51")
}

func TestCanvasSolidBackgroundWinsOverNestedBackgroundsAndPayloadValues(t *testing.T) {
	setTrueColor(t)
	th := theme.Default()
	th.Background = lipgloss.Color("#112233")
	canvas := "48;2;17;34;51"
	for _, tt := range []struct {
		name string
		body string
	}{
		{"standard", "\x1b[41mred"},
		{"bright", "\x1b[101mbright"},
		{"indexed payload 49", "\x1b[48;5;49mindexed"},
		{"rgb payload 49", "\x1b[48;2;49;0;49mrgb"},
		{"colon rgb", "\x1b[48:2::49:0:49mcolon"},
		{"combined foreground background", "\x1b[38;5;49;48;5;49mboth"},
		{"resets", "before\x1b[0mafter-zero\x1b[49mafter-49"},
		{"C1 reset", "before\x9b0mafter-zero\x9b49mafter-49"},
		{"OSC8 UTF8", "\x1b]8;;https://example.test\x1b\\界\x1b]8;;\x1b\\"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			out := Canvas(th, 40, 1, tt.body)
			if got := effectiveBackgroundCells(out); len(got) == 0 {
				t.Fatal("canvas emitted no printable cells")
			} else {
				for i, background := range got {
					if background != canvas {
						t.Fatalf("printable cell %d effective background = %q, want canvas %q: %q", i, background, canvas, out)
					}
				}
			}
		})
	}

	t.Run("cropped inherited nested background", func(t *testing.T) {
		out := Canvas(th, 30, 1, "\x1b[48;5;49mdiscarded\nretained")
		for i, background := range effectiveBackgroundCells(out) {
			if background != canvas {
				t.Fatalf("cropped cell %d effective background = %q, want canvas %q: %q", i, background, canvas, out)
			}
		}
	})
}

func TestCanvasPreservesC1ControlBytesAndRestoresBackground(t *testing.T) {
	setTrueColor(t)
	th := theme.Default()
	th.Background = lipgloss.Color("#112233")
	osc8 := "\x9d8;;https://example.test\x9c"
	body := "before\x9b0mafter-reset\x9b49mafter-background-reset" + osc8 + "link\x9d8;;\x9c"
	out := Canvas(th, 80, 1, body)

	for _, control := range []string{"\x9b0m", "\x9b49m", osc8, "\x9d8;;\x9c"} {
		if !strings.Contains(out, control) {
			t.Errorf("Canvas did not preserve C1 control %q in %q", control, out)
		}
	}
	if !strings.Contains(out, "\x9b0m\x1b[48;2;17;34;51m") || !strings.Contains(out, "\x9b49m\x1b[48;2;17;34;51m") {
		t.Errorf("Canvas did not restore its background after C1 resets: %q", out)
	}
}

func TestCanvasBottomCropReplaysOnlyActiveTerminalState(t *testing.T) {
	setTrueColor(t)
	th := theme.Default()
	th.Background = lipgloss.Color("#112233")
	openOSC8 := "\x1b]8;;https://example.test\x1b\\"
	c1OpenOSC8 := "\x9d8;;https://c1.example.test\x9c"
	for _, tt := range []struct {
		name, body, want string
	}{
		{"sgr inherited", "\x1b[31mdiscarded\nretained\nnewer", "\x1b[31mretained"},
		{"osc8 inherited", openOSC8 + "discarded\nretained\nnewer", openOSC8 + "retained"},
		{"c1 sgr inherited", "\x9b31mdiscarded\nretained\nnewer", "\x9b31mretained"},
		{"c1 osc8 inherited into blank row", c1OpenOSC8 + "discarded\n\nretained", c1OpenOSC8},
	} {
		t.Run(tt.name, func(t *testing.T) {
			out := Canvas(th, 30, 2, tt.body)
			if strings.Contains(ansi.Strip(out), "discarded") {
				t.Errorf("bottom crop retained discarded text: %q", out)
			}
			if !strings.Contains(out, tt.want) {
				t.Errorf("bottom crop did not replay active state %q: %q", tt.want, out)
			}
		})
	}

	balanced := Canvas(th, 30, 1, openOSC8+"discarded\x1b]8;;\x1b\\\nretained")
	if strings.Contains(balanced, "https://example.test") {
		t.Errorf("balanced discarded OSC8 was replayed into retained output: %q", balanced)
	}
}

func TestCanvasBottomCropDoesNotTreatUTF8ContinuationBytesAsC1Controls(t *testing.T) {
	setTrueColor(t)
	th := theme.Default()
	th.Background = lipgloss.Color("#112233")

	for _, tt := range []struct {
		name, discarded, replay, retained string
	}{
		{
			name:      "C1 CSI continuation",
			discarded: "discarded \u069b31m",
			replay:    "\x9b31m",
			retained:  "CSI retained \u069b",
		},
		{
			// U+06DD is encoded as DB 9D. Its second byte must remain part of
			// the rune, not become an OSC introducer while scanning cropped rows.
			name:      "reviewer U+06DD C1 OSC continuation",
			discarded: "discarded \u06dd8;;https://forged.example.test\x9c",
			replay:    "https://forged.example.test",
			retained:  "OSC retained \u06dd",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			out := Canvas(th, 40, 1, tt.discarded+"\n"+tt.retained)
			if strings.Contains(out, tt.replay) {
				t.Errorf("bottom crop replayed a control synthesized from UTF-8 continuation bytes: %q", out)
			}
			if got := ansi.Strip(out); got != tt.retained+strings.Repeat(" ", 40-ansi.StringWidth(tt.retained)) {
				t.Errorf("bottom crop visible UTF-8 = %q, want retained rune intact", got)
			}
		})
	}
}

func TestCanvasBottomCropReplaysOSC8TargetsEndingInSemicolons(t *testing.T) {
	setTrueColor(t)
	th := theme.Default()
	for _, tt := range []struct {
		name, open, close string
	}{
		{"ESC OSC8", "\x1b]8;;https://example.test/path;\x1b\\", "\x1b]8;;\x1b\\"},
		{"C1 OSC8", "\x9d8;;https://example.test/path;\x9c", "\x9d8;;\x9c"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			out := Canvas(th, 30, 1, tt.open+"discarded\nretained")
			if !strings.Contains(out, tt.open+"retained") {
				t.Errorf("active OSC8 target ending in a semicolon was not replayed: %q", out)
			}

			balanced := Canvas(th, 30, 1, tt.open+"discarded"+tt.close+"\nretained")
			if strings.Contains(balanced, "https://example.test/path;") {
				t.Errorf("balanced OSC8 link was replayed: %q", balanced)
			}
		})
	}
}

func TestCanvasNoBackgroundIsTransparent(t *testing.T) {
	setTrueColor(t)
	th := theme.Default()
	th.Background = theme.NoBackground()
	out := Canvas(th, 12, 3, "body\nwith rows")

	assertCanvasSize(t, out, 12, 3)
	if hasBackgroundSGR(out) {
		t.Fatalf("transparent canvas emitted a background-setting SGR sequence: %q", out)
	}
}

func TestCanvasNoBackgroundDoesNotAddBackgroundButKeepsNestedBackground(t *testing.T) {
	setTrueColor(t)
	th := theme.Default()
	th.Background = theme.NoBackground()
	nested := "48;5;49"
	out := Canvas(th, 12, 1, "\x1b[48;5;49mnested")
	if got := effectiveBackgroundCells(out); len(got) == 0 || got[0] != nested {
		t.Fatalf("transparent canvas effective backgrounds = %q, want nested %q", got, nested)
	}
	if strings.Contains(out, "48;2;17;34;51") {
		t.Errorf("transparent canvas introduced its own background: %q", out)
	}
}

func TestCanvasHandlesWideTruncatedAndTinyDimensions(t *testing.T) {
	setTrueColor(t)
	th := theme.Default()
	th.Background = lipgloss.Color("#112233")
	for _, tt := range []struct {
		name          string
		width, height int
		body          string
	}{
		{name: "wide cells", width: 5, height: 2, body: "界界界"},
		{name: "truncated rows", width: 3, height: 1, body: "abcdef\nignored"},
		{name: "one cell", width: 1, height: 1, body: "界"},
		{name: "zero width", width: 0, height: 3, body: "body"},
		{name: "zero height", width: 3, height: 0, body: "body"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			out := Canvas(th, tt.width, tt.height, tt.body)
			if tt.width == 0 || tt.height == 0 {
				if out != "" {
					t.Errorf("Canvas(%d, %d) = %q, want empty output", tt.width, tt.height, out)
				}
				return
			}
			assertCanvasSize(t, out, tt.width, tt.height)
			assertSolidBackground(t, out, "48;2;17;34;51")
		})
	}
}

func assertCanvasSize(t *testing.T, out string, width, height int) {
	t.Helper()
	lines := strings.Split(out, "\n")
	if len(lines) != height {
		t.Fatalf("Canvas returned %d rows, want %d:\n%q", len(lines), height, out)
	}
	for i, line := range lines {
		if got := ansi.StringWidth(line); got != width {
			t.Errorf("row %d display width = %d, want exactly %d: %q", i, got, width, ansi.Strip(line))
		}
	}
}

func setTrueColor(t *testing.T) {
	t.Helper()
	saved := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(saved) })
}

func assertSolidBackground(t *testing.T, out, want string) {
	t.Helper()
	cells := backgroundCells(out)
	if len(cells) == 0 {
		t.Fatal("canvas did not render printable cells")
	}
	for i, background := range cells {
		if background != want {
			t.Fatalf("printable cell %d background = %q, want canvas background %q: %q", i, background, want, out)
		}
	}
}

// backgroundCells follows SGR state at every printable terminal cell, including
// spaces. Canvas must reapply its solid background after nested styles reset it.
func backgroundCells(s string) []string {
	var cells []string
	background := ""
	for i := 0; i < len(s); {
		if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '[' {
			end := i + 2
			for end < len(s) && (s[end] < '@' || s[end] > '~') {
				end++
			}
			if end < len(s) && s[end] == 'm' {
				background = nextBackgroundState(s[i+2:end], background)
			}
			i = end + 1
			continue
		}
		r, n := utf8.DecodeRuneInString(s[i:])
		for range ansi.StringWidth(string(r)) {
			cells = append(cells, background)
		}
		i += n
	}
	return cells
}

// effectiveBackgroundCells interprets both ESC and C1 SGR sequences at each
// printable cell. Extended-color payload values are consumed as payloads, not
// mistaken for standalone reset or background parameters.
func effectiveBackgroundCells(s string) []string {
	var cells []string
	background := ""
	for i := 0; i < len(s); {
		if end, params, ok := testCSI(s, i); ok {
			background = effectiveBackground(params, background)
			i = end
			continue
		}
		if end, ok := testOSC(s, i); ok {
			i = end
			continue
		}
		r, n := utf8.DecodeRuneInString(s[i:])
		for range ansi.StringWidth(string(r)) {
			cells = append(cells, background)
		}
		i += n
	}
	return cells
}

func testCSI(s string, start int) (int, string, bool) {
	params := start + 1
	if s[start] == '\x1b' {
		if start+1 >= len(s) || s[start+1] != '[' {
			return 0, "", false
		}
		params++
	} else if s[start] != '\x9b' {
		return 0, "", false
	}
	end := params
	for end < len(s) && (s[end] < '@' || s[end] > '~') {
		end++
	}
	if end == len(s) || s[end] != 'm' {
		return 0, "", false
	}
	return end + 1, s[params:end], true
}

func testOSC(s string, start int) (int, bool) {
	data := start + 1
	if s[start] == '\x1b' {
		if start+1 >= len(s) || s[start+1] != ']' {
			return 0, false
		}
		data++
	} else if s[start] != '\x9d' {
		return 0, false
	}
	for i := data; i < len(s); i++ {
		if s[i] == '\a' || s[i] == '\x9c' {
			return i + 1, true
		}
		if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '\\' {
			return i + 2, true
		}
	}
	return 0, false
}

func effectiveBackground(params, background string) string {
	if params == "" {
		return ""
	}
	parts := strings.Split(params, ";")
	for i := 0; i < len(parts); i++ {
		part := parts[i]
		if strings.Contains(part, ":") {
			colon := strings.Split(part, ":")
			if len(colon) > 0 && colon[0] == "48" {
				background = strings.Join(colon, ";")
			}
			continue
		}
		switch part {
		case "", "0", "49":
			background = ""
		case "48":
			if i+1 >= len(parts) {
				continue
			}
			switch parts[i+1] {
			case "5":
				if i+2 < len(parts) {
					background = strings.Join(parts[i:i+3], ";")
					i += 2
				}
			case "2":
				if i+4 < len(parts) {
					background = strings.Join(parts[i:i+5], ";")
					i += 4
				}
			}
		default:
			if n, err := strconv.Atoi(part); err == nil && ((n >= 40 && n <= 47) || (n >= 100 && n <= 107)) {
				background = part
			}
		}
	}
	return background
}

func hasBackgroundSGR(s string) bool {
	background := ""
	for i := 0; i < len(s); i++ {
		if s[i] != '\x1b' || i+1 >= len(s) || s[i+1] != '[' {
			continue
		}
		end := i + 2
		for end < len(s) && (s[end] < '@' || s[end] > '~') {
			end++
		}
		if end < len(s) && s[end] == 'm' {
			before := background
			background = nextBackgroundState(s[i+2:end], background)
			if before == "" && background != "" {
				return true
			}
		}
		i = end
	}
	return false
}

func nextBackgroundState(params, background string) string {
	if params == "" {
		return ""
	}
	parts := strings.Split(params, ";")
	for i := 0; i < len(parts); i++ {
		part := parts[i]
		code, err := strconv.Atoi(part)
		if err != nil {
			continue
		}
		switch {
		case code == 0 || code == 49:
			background = ""
		case code == 48:
			if i+4 < len(parts) && parts[i+1] == "2" {
				background = "48;2;" + parts[i+2] + ";" + parts[i+3] + ";" + parts[i+4]
				i += 4
			} else {
				background = "other"
			}
		case code == 38 && i+4 < len(parts) && parts[i+1] == "2":
			i += 4
		case (code >= 40 && code <= 47) || (code >= 100 && code <= 107):
			background = "other"
		}
	}
	return background
}
