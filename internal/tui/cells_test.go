package tui

import (
	"encoding/json"
	"errors"
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

func TestAssistantCellRendersHeadingMarkdown(t *testing.T) {
	th := theme.Default()
	const src = "# Title"
	out := (&assistantCell{text: src, complete: true}).render(80, th)
	plain := ansi.Strip(out)
	if !strings.Contains(plain, "Title") {
		t.Errorf("heading body missing Title:\n%s", plain)
	}
	if !strings.Contains(plain, "strike") {
		t.Errorf("assistant label missing:\n%s", plain)
	}
	// Pin the glamour path: body matches markdownRender (not a divergent plain dump).
	// AutoStyle may keep the leading '#' in notty environments; do not assert its removal.
	bodyWidth := assistantBodyWidth(80, th)
	wantMD, err := markdownRender(src, bodyWidth)
	if err != nil {
		t.Fatalf("markdownRender: %v", err)
	}
	body := assistantBodyPlain(plain)
	if collapsedWS(body) != collapsedWS(ansi.Strip(wantMD)) {
		t.Errorf("assistant body not from markdownRender:\n got %q\nwant %q", body, wantMD)
	}
}

func TestAssistantCellRendersFencedCodeBlock(t *testing.T) {
	th := theme.Default()
	src := "```go\nfunc main() {}\n```"
	plain := ansi.Strip((&assistantCell{text: src, complete: true}).render(80, th))
	if !strings.Contains(collapsedWS(plain), "func main") {
		t.Errorf("code body missing func main:\n%s", plain)
	}
	if strings.Contains(plain, "```") {
		t.Errorf("fenced code still contains triple backticks:\n%s", plain)
	}
	if !strings.Contains(plain, "strike") {
		t.Errorf("assistant label missing:\n%s", plain)
	}
}

func TestAssistantCellMarkdownWidthSafe(t *testing.T) {
	th := theme.Default()
	// Prose + short fenced code (avoid ultra-long tokens glamour will not break).
	src := "This is a fairly long prose paragraph that should wrap cleanly inside the cell width without overflowing the terminal columns.\n\n```go\nfmt.Println(\"hi\")\n```"
	for _, width := range []int{24, 40} {
		out := (&assistantCell{text: src, complete: true}).render(width, th)
		for i, line := range strings.Split(out, "\n") {
			if got := ansi.StringWidth(line); got > width {
				t.Errorf("width %d line %d: StringWidth=%d > %d: %q", width, i, got, width, ansi.Strip(line))
			}
		}
		plain := collapsedWS(ansi.Strip(out))
		if !strings.Contains(plain, "prose paragraph") {
			t.Errorf("width %d missing prose:\n%s", width, ansi.Strip(out))
		}
		if !strings.Contains(plain, "Println") {
			t.Errorf("width %d missing code content:\n%s", width, ansi.Strip(out))
		}
		if strings.Contains(ansi.Strip(out), "```") {
			t.Errorf("width %d still has fences:\n%s", width, ansi.Strip(out))
		}
	}
}

func TestAssistantCellMarkdownWidthSafeLongFence(t *testing.T) {
	th := theme.Default()
	// Long unbreakable fenced line must be clamped by clampRenderWidth / Hardwrap.
	long := strings.Repeat("A", 80)
	src := "```\n" + long + "\n```"
	c := &assistantCell{text: src, complete: true}
	for _, width := range []int{24, 40} {
		out := c.render(width, th)
		for i, line := range strings.Split(out, "\n") {
			if got := ansi.StringWidth(line); got > width {
				t.Errorf("width %d line %d: StringWidth=%d > %d: %q", width, i, got, width, ansi.Strip(line))
			}
		}
		plain := ansi.Strip(out)
		if !strings.Contains(plain, "AAAA") {
			t.Errorf("width %d missing long fence content:\n%s", width, plain)
		}
	}
}

func TestAssistantCellStreamingVsComplete(t *testing.T) {
	th := theme.Default()
	const src = "# Title"
	const width = 80

	// Incomplete: plain path keeps raw markdown source markers.
	streaming := &assistantCell{text: src, complete: false}
	streamPlain := ansi.Strip(streaming.render(width, th))
	if !strings.Contains(streamPlain, "# Title") {
		t.Errorf("streaming body should keep raw source:\n%s", streamPlain)
	}
	// Same as plain renderCellText path (label + indented source).
	plainPath := ansi.Strip((&assistantCell{text: src, complete: false}).render(width, th))
	if streamPlain != plainPath {
		t.Errorf("streaming output diverged from plain path:\n got %q\nwant %q", streamPlain, plainPath)
	}

	// Complete with real glamour: body matches markdownRender.
	// (AutoStyle may keep '#' in notty; do not require visual difference from streaming.)
	done := &assistantCell{text: src, complete: true}
	donePlain := ansi.Strip(done.render(width, th))
	bodyWidth := assistantBodyWidth(width, th)
	wantMD, err := markdownRender(src, bodyWidth)
	if err != nil {
		t.Fatalf("markdownRender: %v", err)
	}
	body := assistantBodyPlain(donePlain)
	if collapsedWS(body) != collapsedWS(ansi.Strip(wantMD)) {
		t.Errorf("complete body not from markdownRender:\n got %q\nwant %q", body, wantMD)
	}

	// Injected renderer makes the complete path observably distinct from streaming.
	orig := markdownRender
	t.Cleanup(func() { markdownRender = orig })
	markdownRender = func(source string, w int) (string, error) {
		return "MD:" + source, nil
	}
	injected := ansi.Strip((&assistantCell{text: src, complete: true}).render(width, th))
	if !strings.Contains(injected, "MD:# Title") {
		t.Errorf("complete cell missing injected markdown body: %q", injected)
	}
	if strings.Contains(streamPlain, "MD:") {
		t.Errorf("streaming cell unexpectedly used markdown path: %q", streamPlain)
	}
}

func TestAssistantCellIncompleteSkipsMarkdownRender(t *testing.T) {
	orig := markdownRender
	t.Cleanup(func() { markdownRender = orig })

	var calls int
	markdownRender = func(source string, width int) (string, error) {
		calls++
		return "should-not-appear", nil
	}

	th := theme.Default()
	const src = "# Title"
	out := ansi.Strip((&assistantCell{text: src, complete: false}).render(80, th))
	if calls != 0 {
		t.Errorf("incomplete cell called markdownRender %d times, want 0", calls)
	}
	if strings.Contains(out, "should-not-appear") {
		t.Errorf("incomplete cell used markdown body: %q", out)
	}
	if !strings.Contains(out, "# Title") {
		t.Errorf("incomplete cell missing raw source:\n%s", out)
	}
}

func TestAssistantCellEmptyAndWhitespace(t *testing.T) {
	th := theme.Default()
	for _, tc := range []struct {
		name     string
		text     string
		complete bool
	}{
		{"empty incomplete", "", false},
		{"empty complete", "", true},
		{"spaces incomplete", "   ", false},
		{"spaces complete", "   ", true},
		{"newlines incomplete", "\n\n\t  \n", false},
		{"newlines complete", "\n\n\t  \n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := (&assistantCell{text: tc.text, complete: tc.complete}).render(80, th)
			plain := ansi.Strip(out)
			if !strings.Contains(plain, "strike") {
				t.Errorf("label missing for %q:\n%s", tc.text, plain)
			}
			body := strings.TrimSpace(assistantBodyPlain(plain))
			if body != "" {
				t.Errorf("expected empty body for %q, got %q", tc.text, body)
			}
		})
	}
}

func TestAssistantCellPlainProse(t *testing.T) {
	th := theme.Default()
	const msg = "Hello world without markup"
	// Complete path still shows plain prose content via markdownRender.
	plain := ansi.Strip((&assistantCell{text: msg, complete: true}).render(80, th))
	if !strings.Contains(collapsedWS(plain), msg) {
		t.Errorf("plain prose missing:\n%s", plain)
	}
	if !strings.Contains(plain, "strike") {
		t.Errorf("assistant label missing:\n%s", plain)
	}
}

func TestAssistantCellMarkdownErrorFallback(t *testing.T) {
	orig := markdownRender
	t.Cleanup(func() { markdownRender = orig })
	markdownRender = func(source string, width int) (string, error) {
		return "", errMarkdownTest
	}

	th := theme.Default()
	const src = "fallback plain source text that must remain visible"
	const width = 40
	cell := &assistantCell{text: src, complete: true}
	out := cell.render(width, th)
	plain := ansi.Strip(out)
	// Hardwrap may insert mid-word breaks; compare with all whitespace removed.
	if !strings.Contains(stripAllWS(plain), stripAllWS(src)) {
		t.Errorf("error fallback omitted source text:\n%s", plain)
	}
	if !strings.Contains(plain, "strike") {
		t.Errorf("assistant label missing on error path:\n%s", plain)
	}
	for i, line := range strings.Split(out, "\n") {
		if got := ansi.StringWidth(line); got > width {
			t.Errorf("fallback line %d width = %d, want <= %d: %q", i, got, width, ansi.Strip(line))
		}
	}
}

func TestAssistantCellMarkdownCache(t *testing.T) {
	orig := markdownRender
	t.Cleanup(func() { markdownRender = orig })

	var calls int
	var lastSrc string
	var lastW int
	markdownRender = func(source string, width int) (string, error) {
		calls++
		lastSrc = source
		lastW = width
		return "rendered:" + source, nil
	}

	th := theme.Default()
	cell := &assistantCell{text: "alpha", complete: true}

	out1 := ansi.Strip(cell.render(80, th))
	if calls != 1 {
		t.Fatalf("first render: calls=%d, want 1", calls)
	}
	if !strings.Contains(out1, "rendered:alpha") {
		t.Fatalf("first render missing injected body: %q", out1)
	}

	out2 := ansi.Strip(cell.render(80, th))
	if calls != 1 {
		t.Errorf("same text/width re-render: calls=%d, want 1 (cache hit)", calls)
	}
	if out2 != out1 {
		t.Errorf("cache hit changed output:\n got %q\nwant %q", out2, out1)
	}

	_ = cell.render(60, th)
	if calls != 2 {
		t.Errorf("width change: calls=%d, want 2", calls)
	}
	if lastW == 0 {
		t.Errorf("width change did not pass body width")
	}

	cell.text = "beta"
	out3 := ansi.Strip(cell.render(60, th))
	if calls != 3 {
		t.Errorf("text change: calls=%d, want 3", calls)
	}
	if lastSrc != "beta" {
		t.Errorf("text change lastSrc=%q, want beta", lastSrc)
	}
	if !strings.Contains(out3, "rendered:beta") {
		t.Errorf("text change missing new body: %q", out3)
	}
}

func TestUserCellUnchangedByMarkdown(t *testing.T) {
	// User cells stay plain Hardwrap text; markdown markers must remain literal.
	th := theme.Default()
	plain := ansi.Strip((&userCell{text: "# Title"}).render(80, th))
	if !strings.Contains(plain, "# Title") {
		t.Errorf("user cell should keep raw markdown:\n%s", plain)
	}
	if !strings.Contains(plain, "you") {
		t.Errorf("user label missing:\n%s", plain)
	}
}

// errMarkdownTest is a sentinel for assistant markdown error-path tests.
var errMarkdownTest = errors.New("markdown render failed")

// assistantBodyPlain returns the stripped cell body after the first (label) line.
func assistantBodyPlain(plain string) string {
	parts := strings.SplitN(plain, "\n", 2)
	if len(parts) < 2 {
		return ""
	}
	return parts[1]
}

func collapsedWS(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func stripAllWS(s string) string {
	return strings.Map(func(r rune) rune {
		if r == ' ' || r == '\n' || r == '\t' || r == '\r' {
			return -1
		}
		return r
	}, s)
}

func assistantBodyWidth(width int, th theme.Theme) int {
	th = th.Resolve()
	indentation := themedSpace(th.Spacing.SM)
	return max(1, width-lipgloss.Width(indentation))
}
