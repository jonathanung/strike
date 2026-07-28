package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestWrapTextPrefersWordBoundaries(t *testing.T) {
	// Regression for #460: hard-wrap split "GitHub" / "issue-handler" mid-token.
	const src = `Use the skill tool with name "issue-handler" (if available), or follow the issue-handler workflow from .claude/skills/issue-handler/SKILL.md if present. Own GitHub issue #460 end-to-end for strike-cli.`
	for _, width := range []int{60, 70, 80, 90} {
		out := WrapText(src, width)
		for i, line := range strings.Split(out, "\n") {
			if got := ansi.StringWidth(line); got > width {
				t.Errorf("width %d line %d: StringWidth=%d > %d: %q", width, i, got, width, line)
			}
			if strings.HasSuffix(line, " ") {
				t.Errorf("width %d line %d has trailing pad: %q", width, i, line)
			}
		}
		// Whole tokens must appear unbroken on some line (not split across wraps).
		// Hyphenated compounds may soft-break at '-' (lipgloss); that is fine.
		for _, tok := range []string{"issue-handler", "GitHub", "strike-cli"} {
			if !strings.Contains(out, tok) {
				t.Errorf("width %d broke token %q across lines:\n%s", width, tok, out)
			}
		}
		// Content preserved (spaces/newlines may move; hyphens may gain a break).
		if stripWS(out) != stripWS(src) {
			t.Errorf("width %d dropped/changed content:\n got %q\nwant %q", width, stripWS(out), stripWS(src))
		}
	}
}

func TestWrapTextHardBreaksOverlongToken(t *testing.T) {
	const width = 40
	long := strings.Repeat("A", 90)
	out := WrapText("see "+long+" end", width)
	if !strings.Contains(strings.ReplaceAll(out, "\n", ""), long) {
		t.Fatalf("lost long token:\n%s", out)
	}
	for i, line := range strings.Split(out, "\n") {
		if got := ansi.StringWidth(line); got > width {
			t.Errorf("line %d width %d > %d: %q", i, got, width, line)
		}
	}
}

func TestWrapTextNarrowAndEmpty(t *testing.T) {
	if got := WrapText("hello", 0); got != "hello" {
		t.Errorf("width 0: got %q", got)
	}
	if got := WrapText("hello", -1); got != "hello" {
		t.Errorf("width -1: got %q", got)
	}
	if got := WrapText("", 20); got != "" {
		t.Errorf("empty: got %q", got)
	}
	if got := WrapText("ab cd", 1); ansi.StringWidth(strings.Split(got, "\n")[0]) > 1 {
		t.Errorf("width 1 first line too wide: %q", got)
	}
}

func stripWS(s string) string {
	return strings.Map(func(r rune) rune {
		if r == ' ' || r == '\n' || r == '\t' || r == '\r' {
			return -1
		}
		return r
	}, s)
}
