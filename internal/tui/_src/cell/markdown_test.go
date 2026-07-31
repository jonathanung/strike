package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

// sampleMarkdown is a fixed fixture for purity / golden tests. No mermaid —
// that path is non-deterministic across mermaid-ascii versions.
const sampleMarkdown = `# Hello

This is **bold** and *italic* with a [link](https://example.com).

- item one
- item two

` + "```go\n" + `func main() {
	fmt.Println("hi")
}
` + "```\n" + `
> quote line

| A | B |
| - | - |
| 1 | 2 |
`

func TestGlamourRenderPurity(t *testing.T) {
	saved := glamourStyleName
	t.Cleanup(func() { glamourStyleName = saved })
	setGlamourStyle(true)

	const width = 72
	a, err := glamourRender(sampleMarkdown, width)
	if err != nil {
		t.Fatalf("first render: %v", err)
	}
	b, err := glamourRender(sampleMarkdown, width)
	if err != nil {
		t.Fatalf("second render: %v", err)
	}
	if a != b {
		t.Fatalf("glamour v2 purity broken: same input produced different output\n--- a ---\n%s\n--- b ---\n%s", a, b)
	}
	if a == "" {
		t.Fatal("render returned empty")
	}
	plain := ansi.Strip(a)
	if !strings.Contains(plain, "Hello") {
		t.Fatalf("missing heading text:\n%s", plain)
	}
	if !strings.Contains(plain, "fmt.Println") {
		t.Fatalf("missing code body:\n%s", plain)
	}
}

func TestGlamourRenderGolden(t *testing.T) {
	saved := glamourStyleName
	t.Cleanup(func() { glamourStyleName = saved })

	cases := []struct {
		name  string
		dark  bool
		file  string
		width int
	}{
		{name: "dark", dark: true, file: "sample-dark.golden", width: 72},
		{name: "light", dark: false, file: "sample-light.golden", width: 72},
	}

	dir := filepath.Join(moduleRoot(t), "internal", "tui", "testdata", "glamour")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setGlamourStyle(tc.dark)
			if got := glamourStyle(); (tc.dark && got != "dark") || (!tc.dark && got != "light") {
				t.Fatalf("glamourStyle = %q after setGlamourStyle(%v)", got, tc.dark)
			}
			got, err := glamourRender(sampleMarkdown, tc.width)
			if err != nil {
				t.Fatalf("glamourRender: %v", err)
			}
			path := filepath.Join(dir, tc.file)
			if os.Getenv("UPDATE_GOLDEN") == "1" {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatalf("write golden: %v", err)
				}
				t.Logf("updated %s", path)
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden %s: %v (run UPDATE_GOLDEN=1 go test)", path, err)
			}
			if got != string(want) {
				t.Fatalf("golden mismatch for %s\n--- got ---\n%s\n--- want ---\n%s", tc.file, got, want)
			}
		})
	}
}

func TestGlamourStylePathNeverAuto(t *testing.T) {
	saved := glamourStyleName
	t.Cleanup(func() { glamourStyleName = saved })

	for _, dark := range []bool{true, false} {
		setGlamourStyle(dark)
		style := glamourStyle()
		if style != "dark" && style != "light" {
			t.Fatalf("glamourStyle=%q want dark|light", style)
		}
		if style == "auto" {
			t.Fatal("glamourStyle must never be auto")
		}
		out, err := glamourRender("**x**", 40)
		if err != nil {
			t.Fatalf("render: %v", err)
		}
		if out == "" {
			t.Fatal("empty render")
		}
		if glamourStyle() != style {
			t.Errorf("style mutated during render: before %q after %q", style, glamourStyle())
		}
	}
}

func TestGlamourRenderCacheKeyIncludesWidthAndStyle(t *testing.T) {
	// Re-verify mdCache: width and glamour style are part of the cache key.
	saved := glamourStyleName
	t.Cleanup(func() { glamourStyleName = saved })
	setGlamourStyle(true)

	const src = "A long enough line of prose that will wrap differently at narrow widths versus wide ones for the purity check."
	narrow, err := glamourRender(src, 20)
	if err != nil {
		t.Fatalf("narrow: %v", err)
	}
	wide, err := glamourRender(src, 80)
	if err != nil {
		t.Fatalf("wide: %v", err)
	}
	if narrow == wide {
		t.Fatal("expected different wrap at width 20 vs 80")
	}
	// Cache hit path on assistantCell.
	c := &assistantCell{text: src, complete: true}
	th := theme.Default()
	_ = c.render(40, th)
	if !c.mdCacheOK || c.mdMisses != 1 {
		t.Fatalf("after first render: ok=%v misses=%d", c.mdCacheOK, c.mdMisses)
	}
	if c.mdCacheStyle != "dark" {
		t.Fatalf("mdCacheStyle=%q want dark", c.mdCacheStyle)
	}
	_ = c.render(40, th)
	if c.mdMisses != 1 {
		t.Fatalf("cache miss on same width/style: misses=%d want 1", c.mdMisses)
	}
	_ = c.render(60, th)
	if c.mdMisses != 2 {
		t.Fatalf("width change should miss cache: misses=%d want 2", c.mdMisses)
	}
	setGlamourStyle(false)
	_ = c.render(60, th)
	if c.mdMisses != 3 {
		t.Fatalf("style change should miss cache: misses=%d want 3", c.mdMisses)
	}
	if c.mdCacheStyle != "light" {
		t.Fatalf("mdCacheStyle=%q want light after appearance flip", c.mdCacheStyle)
	}
	_ = c.render(60, th)
	if c.mdMisses != 3 {
		t.Fatalf("same width+style should hit: misses=%d want 3", c.mdMisses)
	}
}
