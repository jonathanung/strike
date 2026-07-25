package term

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/hinshun/vt10x"
	"github.com/muesli/termenv"
)

func TestRenderNilSession(t *testing.T) {
	if got := Render(nil, 10, 5); got != "" {
		t.Fatalf("Render(nil) = %q, want empty", got)
	}
}

func TestRenderClipsAndShowsText(t *testing.T) {
	script := `#!/bin/sh
printf 'MARKER-LINE\n'
sleep 30
`
	dir := t.TempDir()
	path := dir + "/paint.sh"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(path)
	s, err := Start(cmd, 40, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	waitScreenContains(t, s, "MARKER-LINE", 3*time.Second)

	full := Render(s, 0, 0)
	plain := ansi.Strip(full)
	if !strings.Contains(plain, "MARKER-LINE") {
		t.Fatalf("full render missing marker: %q", plain)
	}
	lines := strings.Split(plain, "\n")
	if len(lines) != 10 {
		t.Fatalf("full rows = %d, want 10", len(lines))
	}
	if len([]rune(lines[0])) != 40 {
		t.Fatalf("full cols = %d, want 40 (line=%q)", len([]rune(lines[0])), lines[0])
	}

	clipped := Render(s, 8, 2)
	clipPlain := ansi.Strip(clipped)
	clipLines := strings.Split(clipPlain, "\n")
	if len(clipLines) != 2 {
		t.Fatalf("clipped rows = %d, want 2", len(clipLines))
	}
	if len([]rune(clipLines[0])) != 8 {
		t.Fatalf("clipped cols = %d, want 8", len([]rune(clipLines[0])))
	}
	if !strings.HasPrefix(strings.TrimRight(clipLines[0], " "), "MARKER-") {
		t.Fatalf("clipped first line = %q", clipLines[0])
	}
}

func TestRenderStylesAndColors(t *testing.T) {
	// Non-TTY test runners often pick Ascii; force truecolor so styles stick.
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })

	// Bold red, underline, italic, reverse, ansi16, 256-color, greyscale.
	script := "#!/bin/sh\n" +
		"printf '\\033[1;31mB\\033[0m'\n" +
		"printf '\\033[4mU\\033[0m'\n" +
		"printf '\\033[3mI\\033[0m'\n" +
		"printf '\\033[7mR\\033[0m'\n" +
		"printf '\\033[38;5;196mX\\033[0m'\n" +
		"printf '\\033[38;5;232mG\\033[0m'\n" +
		"printf '\\033[38;5;15mW\\033[0m'\n" +
		"printf '\\n'\n" +
		"sleep 30\n"
	dir := t.TempDir()
	path := dir + "/style.sh"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(path)
	s, err := Start(cmd, 20, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	waitScreenContains(t, s, "BUIRXGW", 3*time.Second)

	out := Render(s, 20, 4)
	plain := ansi.Strip(out)
	if !strings.Contains(plain, "BUIRXGW") {
		t.Fatalf("styled render plain text = %q", plain)
	}
	// Styled output should retain SGR / lipgloss sequences beyond plain text.
	if out == plain {
		t.Fatal("expected ANSI styling in Render output")
	}
}

func TestColorToHex(t *testing.T) {
	tests := []struct {
		name string
		c    vt10x.Color
		bg   bool
		want string
	}{
		{name: "default fg", c: vt10x.DefaultFG, want: ""},
		{name: "default fg as bg", c: vt10x.DefaultFG, bg: true, want: ""},
		{name: "default bg", c: vt10x.DefaultBG, want: ""},
		{name: "default cursor", c: vt10x.DefaultCursor, want: ""},
		{name: "ansi black", c: 0, want: "#000000"},
		{name: "ansi bright white", c: 15, want: "#ffffff"},
		{name: "ansi red", c: 1, want: "#cd0000"},
		{name: "cube red-ish", c: 196, want: "#ff0000"},
		{name: "greyscale first", c: 232, want: "#080808"},
		{name: "greyscale last", c: 255, want: "#eeeeee"},
		{name: "out of range", c: 300, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := colorToHex(tt.c, tt.bg)
			if got != tt.want {
				t.Fatalf("colorToHex(%v, %v) = %q, want %q", tt.c, tt.bg, got, tt.want)
			}
		})
	}
}

func TestXterm256RGB(t *testing.T) {
	tests := []struct {
		c       uint32
		r, g, b uint8
	}{
		{c: 0, r: 0x00, g: 0x00, b: 0x00},
		{c: 1, r: 0xcd, g: 0x00, b: 0x00},
		{c: 15, r: 0xff, g: 0xff, b: 0xff},
		{c: 16, r: 0, g: 0, b: 0},           // cube origin
		{c: 196, r: 0xff, g: 0, b: 0},       // bright red in cube
		{c: 231, r: 0xff, g: 0xff, b: 0xff}, // cube white
		{c: 232, r: 8, g: 8, b: 8},          // greyscale start
		{c: 255, r: 0xee, g: 0xee, b: 0xee}, // greyscale end
		{c: 300, r: 0, g: 0, b: 0},          // out of range
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("c%d", tt.c), func(t *testing.T) {
			r, g, b := xterm256RGB(tt.c)
			if r != tt.r || g != tt.g || b != tt.b {
				t.Fatalf("xterm256RGB(%d) = %d,%d,%d want %d,%d,%d", tt.c, r, g, b, tt.r, tt.g, tt.b)
			}
		})
	}
}

func TestCube(t *testing.T) {
	if got := cube(0); got != 0 {
		t.Fatalf("cube(0) = %d, want 0", got)
	}
	if got := cube(1); got != 95 {
		t.Fatalf("cube(1) = %d, want 95", got)
	}
	if got := cube(5); got != 255 {
		t.Fatalf("cube(5) = %d, want 255", got)
	}
}

func waitScreenContains(t *testing.T, s *Session, needle string, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %q; screen=%q", needle, s.Terminal().String())
		case <-s.Notify():
			if strings.Contains(s.Terminal().String(), needle) {
				return
			}
		case <-s.Done():
			text := s.Terminal().String()
			if strings.Contains(text, needle) {
				return
			}
			t.Fatalf("exited before %q: %v screen=%q", needle, s.WaitErr(), text)
		}
	}
}
