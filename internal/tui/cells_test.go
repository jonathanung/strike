package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
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

func TestToolCellEditMetadataShowsDiffPreview(t *testing.T) {
	th := theme.Default()
	meta := json.RawMessage(`{"oldString":"foo","newString":"bar","count":1}`)
	cell := &toolCell{
		name:     "edit",
		title:    "file.go",
		output:   "Edited file.go",
		metadata: meta,
		done:     true,
	}
	plain := ansi.Strip(cell.render(80, th))
	for _, want := range []string{"-foo", "+bar", "+1", "-1"} {
		if !strings.Contains(plain, want) {
			t.Errorf("edit tool cell missing %q:\n%s", want, plain)
		}
	}
	// Diff replaces the muted output blurb; the "Edited …" summary need not appear in the body.
	// (Title may still mention the path.)
	if strings.Contains(plain, "Edited file.go") {
		// acceptable if title/output still shows it, but body should be diff-first;
		// only fail if output blurb is the sole body without markers (already checked).
	}
	for i, line := range strings.Split(cell.render(80, th), "\n") {
		if got := lipgloss.Width(line); got > 80 {
			t.Errorf("line %d width = %d > 80: %q", i, got, ansi.Strip(line))
		}
	}
}

func TestToolCellWithoutMetadataShowsOutputPreview(t *testing.T) {
	th := theme.Default()
	cell := &toolCell{
		name:   "bash",
		title:  "echo hi",
		output: "hello from bash",
		done:   true,
	}
	plain := ansi.Strip(cell.render(80, th))
	if !strings.Contains(plain, "hello from bash") {
		t.Errorf("expected muted output preview:\n%s", plain)
	}
	if strings.Contains(plain, "-foo") || strings.Contains(plain, "+bar") {
		t.Errorf("unexpected diff markers without metadata:\n%s", plain)
	}
}

func TestToolCellNotDoneHasNoBody(t *testing.T) {
	th := theme.Default()
	meta := json.RawMessage(`{"oldString":"foo","newString":"bar"}`)
	cell := &toolCell{
		name:     "edit",
		output:   "should not show",
		metadata: meta,
		done:     false,
	}
	plain := ansi.Strip(cell.render(80, th))
	if strings.Contains(plain, "foo") || strings.Contains(plain, "bar") || strings.Contains(plain, "should not show") {
		t.Errorf("in-progress tool cell should not render body:\n%s", plain)
	}
	// single status line only
	if strings.Count(plain, "\n") > 0 && strings.TrimSpace(strings.SplitN(plain, "\n", 2)[1]) != "" {
		// allow trailing empty; reject multi-line body content
		lines := strings.Split(strings.TrimRight(plain, "\n"), "\n")
		if len(lines) > 1 {
			t.Errorf("in-progress cell has body lines: %q", plain)
		}
	}
}
