package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestExpandMermaidFencesReplacesClosedFence(t *testing.T) {
	prev := mermaidDiagramRender
	t.Cleanup(func() { mermaidDiagramRender = prev })
	mermaidDiagramRender = func(source string, width int) (string, error) {
		if !strings.Contains(source, "A --> B") {
			t.Fatalf("render body = %q, want edge", source)
		}
		if width != 40 {
			t.Fatalf("width = %d, want 40", width)
		}
		return "A ---> B", nil
	}

	in := "# Doc\n\n```mermaid\ngraph LR\nA --> B\n```\n\nend\n"
	got := expandMermaidFences(in, 40)
	if strings.Contains(got, "mermaid") {
		t.Fatalf("expanded still has mermaid tag:\n%s", got)
	}
	if !strings.Contains(got, "A ---> B") {
		t.Fatalf("expanded missing ascii:\n%s", got)
	}
	if !strings.Contains(got, "```\nA ---> B\n```") {
		t.Fatalf("ascii not in plain fence:\n%s", got)
	}
	if !strings.Contains(got, "# Doc") || !strings.Contains(got, "end") {
		t.Fatalf("surrounding prose lost:\n%s", got)
	}
}

func TestExpandMermaidFencesTildeFenceAndIndent(t *testing.T) {
	prev := mermaidDiagramRender
	t.Cleanup(func() { mermaidDiagramRender = prev })
	mermaidDiagramRender = func(source string, _ int) (string, error) {
		return "X", nil
	}

	in := "  ~~~mermaid\n  graph TD\n  A-->B\n  ~~~\n"
	got := expandMermaidFences(in, 80)
	if !strings.Contains(got, "  ~~~\n  X\n  ~~~") {
		t.Fatalf("indent/tilde not preserved:\n%s", got)
	}
}

func TestExpandMermaidFencesLeavesOtherFences(t *testing.T) {
	prev := mermaidDiagramRender
	t.Cleanup(func() { mermaidDiagramRender = prev })
	var calls int
	mermaidDiagramRender = func(string, int) (string, error) {
		calls++
		return "nope", nil
	}

	in := "```go\nfmt.Println(\"mermaid\")\n```\n"
	got := expandMermaidFences(in, 80)
	if got != in {
		t.Fatalf("non-mermaid fence changed:\n got %q\nwant %q", got, in)
	}
	if calls != 0 {
		t.Fatalf("renderer called %d times, want 0", calls)
	}
}

func TestExpandMermaidFencesKeepsOriginalOnError(t *testing.T) {
	prev := mermaidDiagramRender
	t.Cleanup(func() { mermaidDiagramRender = prev })
	mermaidDiagramRender = func(string, int) (string, error) {
		return "", errors.New("boom")
	}

	in := "```mermaid\ngraph LR\nA --> B\n```"
	got := expandMermaidFences(in, 80)
	if got != in {
		t.Fatalf("on error got %q, want original", got)
	}
}

func TestExpandMermaidFencesLeavesUnclosedFence(t *testing.T) {
	prev := mermaidDiagramRender
	t.Cleanup(func() { mermaidDiagramRender = prev })
	var calls int
	mermaidDiagramRender = func(string, int) (string, error) {
		calls++
		return "X", nil
	}

	in := "```mermaid\ngraph LR\nA --> B\n"
	got := expandMermaidFences(in, 80)
	if got != in {
		t.Fatalf("unclosed changed:\n got %q\nwant %q", got, in)
	}
	if calls != 0 {
		t.Fatalf("renderer called on unclosed fence")
	}
}

func TestExpandMermaidFencesMultiple(t *testing.T) {
	prev := mermaidDiagramRender
	t.Cleanup(func() { mermaidDiagramRender = prev })
	var n int
	mermaidDiagramRender = func(string, int) (string, error) {
		n++
		return string(rune('0' + n)), nil
	}

	in := "```mermaid\nA\n```\nmid\n```mermaid\nB\n```"
	got := expandMermaidFences(in, 80)
	if !strings.Contains(got, "1") || !strings.Contains(got, "2") {
		t.Fatalf("missing both diagrams:\n%s", got)
	}
	if strings.Contains(strings.ToLower(got), "mermaid") {
		t.Fatalf("mermaid tags remain:\n%s", got)
	}
}

func TestGlamourRenderExpandsMermaid(t *testing.T) {
	src := "# Flow\n\n```mermaid\ngraph LR\nA --> B\n```\n"
	out, err := glamourRender(src, 72)
	if err != nil {
		t.Fatalf("glamourRender: %v", err)
	}
	plain := ansi.Strip(out)
	if strings.Contains(plain, "graph LR") {
		t.Fatalf("still shows mermaid source:\n%s", plain)
	}
	// Unicode box art from mermaid-ascii default config.
	if !strings.Contains(plain, "A") || !strings.Contains(plain, "B") {
		t.Fatalf("missing node labels:\n%s", plain)
	}
	if !strings.ContainsAny(plain, "┌└│─►→") && !strings.Contains(plain, "-->") {
		t.Fatalf("missing diagram edges/boxes:\n%s", plain)
	}
}

func TestIsMermaidInfo(t *testing.T) {
	cases := []struct {
		info string
		want bool
	}{
		{"mermaid", true},
		{"Mermaid", true},
		{"mermaid title=x", true},
		{"go", false},
		{"", false},
		{"mermaidjs", false},
	}
	for _, tc := range cases {
		if got := isMermaidInfo(tc.info); got != tc.want {
			t.Errorf("isMermaidInfo(%q)=%v, want %v", tc.info, got, tc.want)
		}
	}
}
