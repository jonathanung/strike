package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

func TestToolCellUsesToolGuideIconRatherThanBorderVertical(t *testing.T) {
	th := theme.Default()
	th.Icons.ToolGuide = ">"
	th.BorderStyle.Vertical = "|"

	out := (&toolCell{name: "x", output: "result", done: true}).render(8, th)
	plain := ansi.Strip(out)
	if !strings.Contains(plain, "  > ") {
		t.Errorf("tool output guide = %q, want custom ToolGuide", plain)
	}
	if strings.Contains(plain, "|") {
		t.Errorf("tool output used BorderStyle.Vertical instead of ToolGuide: %q", plain)
	}
	for i, line := range strings.Split(out, "\n") {
		if got := ansi.StringWidth(line); got > 8 {
			t.Errorf("tool cell line %d width = %d, want <= 8: %q", i, got, ansi.Strip(line))
		}
	}
}

func TestToolCellUsesCustomDotForStructuredTitleSeparator(t *testing.T) {
	th := theme.Default()
	th.Icons.Dot = "|"

	plain := ansi.Strip((&toolCell{name: "bash", title: "run tests", done: true}).render(80, th))
	if !strings.Contains(plain, "bash | run tests") {
		t.Errorf("tool title omitted custom structured separator: %q", plain)
	}
	if strings.Contains(plain, "bash · run tests") {
		t.Errorf("tool title retained default dot separator: %q", plain)
	}
}
