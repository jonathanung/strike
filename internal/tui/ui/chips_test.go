package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

func TestBadgeWrapsTextInBrackets(t *testing.T) {
	out := Badge(theme.Default(), ToneAccent, "anthropic/claude-sonnet-5")
	if !strings.Contains(out, "anthropic/claude-sonnet-5") {
		t.Errorf("badge missing text: %q", out)
	}
	if !strings.HasPrefix(out, "[") || !strings.HasSuffix(out, "]") {
		t.Errorf("badge is not bracketed: %q", out)
	}
}

func TestBadgeUsesCustomDelimitersAndStrongTone(t *testing.T) {
	th := theme.Default()
	th.Icons.BadgeLeft = "<"
	th.Icons.BadgeRight = ">"
	if out := Badge(th, ToneAccent, "model"); !strings.HasPrefix(out, "<") || !strings.HasSuffix(out, ">") {
		t.Errorf("badge did not use custom delimiters: %q", out)
	}
}

func TestWidgetsSafelyRenderNegativeThemeSpacing(t *testing.T) {
	for _, spacing := range []theme.Spacing{
		theme.NewSpacing(-1, -2, -3, -4),
		theme.Spacing{}.WithXS(-1).WithSM(-2).WithMD(-3).WithLG(-4),
		theme.Spacing{XS: -1, SM: -2, MD: -3, LG: -4},
	} {
		th := theme.Default()
		th.Spacing = spacing
		if out := Badge(th, ToneAccent, "badge"); !strings.Contains(out, "badge") {
			t.Errorf("Badge dropped content with negative spacing: %q", out)
		}
		if out := Dialog(th, DialogOpts{Width: 30, Hint: "hint"}, "body"); !strings.Contains(out, "body") || !strings.Contains(out, "hint") {
			t.Errorf("Dialog dropped content with negative spacing: %q", out)
		}
		if out := List(th, ListOpts{Items: []ListItem{{Label: "item"}}, Cursor: 0, Width: 20}); !strings.Contains(out, "item") {
			t.Errorf("List dropped content with negative spacing: %q", out)
		}
	}
}

func TestBadgeHonorsExplicitZeroSpacing(t *testing.T) {
	th := theme.Default()
	th.Spacing = theme.NewSpacing(0, 0, 0, 0)
	if got := Badge(th, ToneAccent, "x"); got != "[x]" {
		t.Errorf("Badge with zero XS spacing = %q, want [x]", got)
	}
}

func TestKeyHintsJoinsWithDotsWithinWidth(t *testing.T) {
	hints := []KeyHint{{"enter", "send"}, {"ctrl+p", "palette"}, {"esc", "interrupt"}}
	out := KeyHints(theme.Default(), 80, hints)
	for _, h := range hints {
		if !strings.Contains(out, h.Key) || !strings.Contains(out, h.Label) {
			t.Errorf("hint %q/%q missing from %q", h.Key, h.Label, out)
		}
	}
	if !strings.Contains(out, "·") {
		t.Errorf("hints not dot-separated: %q", out)
	}
	if w := lipgloss.Width(out); w > 80 {
		t.Errorf("width %d exceeds 80", w)
	}
}

func TestKeyHintsTruncatesByDroppingWholeHints(t *testing.T) {
	hints := []KeyHint{{"enter", "send"}, {"ctrl+p", "palette"}, {"esc", "interrupt"}}
	out := KeyHints(theme.Default(), 14, hints)
	if w := lipgloss.Width(out); w > 14 {
		t.Errorf("truncated width %d exceeds 14: %q", w, out)
	}
	if !strings.Contains(out, "enter send") {
		t.Errorf("first hint dropped: %q", out)
	}
	if strings.Contains(out, "interrupt") {
		t.Errorf("overflowing hint not dropped: %q", out)
	}
}

func TestKeyHintsNarrowFallbackUsesThemeKeyLabelGap(t *testing.T) {
	themes := []struct {
		name string
		th   theme.Theme
		gap  string
	}{
		{name: "default", th: theme.Default(), gap: " "},
		{name: "explicit zero XS", th: func() theme.Theme {
			th := theme.Default()
			th.Spacing = theme.NewSpacing(0, 2, 3, 4)
			return th
		}(), gap: ""},
		{name: "custom XS", th: func() theme.Theme {
			th := theme.Default()
			th.Spacing = theme.NewSpacing(2, 2, 3, 4)
			return th
		}(), gap: "  "},
	}
	for _, tt := range themes {
		t.Run(tt.name, func(t *testing.T) {
			full := "enter" + tt.gap + "send"
			out := ansi.Strip(KeyHints(tt.th, len(full), []KeyHint{{Key: "enter", Label: "send"}, {Key: "esc", Label: "close"}}))
			if out != full {
				t.Errorf("full hint = %q, want %q", out, full)
			}
			if w := lipgloss.Width(out); w != len(full) {
				t.Errorf("full hint width = %d, want %d", w, len(full))
			}

			narrowWidth := len(full) - 1
			out = ansi.Strip(KeyHints(tt.th, narrowWidth, []KeyHint{{Key: "enter", Label: "send"}}))
			want := strings.TrimSuffix(full, "nd") + tt.th.Resolve().Icons.Ellipsis
			if out != want {
				t.Errorf("narrow hint = %q, want %q", out, want)
			}
			if w := lipgloss.Width(out); w != narrowWidth {
				t.Errorf("narrow hint width = %d, want %d", w, narrowWidth)
			}
		})
	}
}

func TestKeyHintsTinyWidthAndEmptyInputs(t *testing.T) {
	if w := lipgloss.Width(KeyHints(theme.Default(), 4, []KeyHint{{"enter", "send"}})); w > 4 {
		t.Errorf("tiny-width hints overflowed: %d", w)
	}
	if got := KeyHints(theme.Default(), 0, []KeyHint{{"a", "b"}}); got != "" {
		t.Errorf("zero width should yield empty, got %q", got)
	}
	if got := KeyHints(theme.Default(), 40, nil); got != "" {
		t.Errorf("no hints should yield empty, got %q", got)
	}
}

func TestStatusBarAlignsLeftAndRightToExactWidth(t *testing.T) {
	out := StatusBar(theme.Default(), 20, "left", "right")
	if w := lipgloss.Width(out); w != 20 {
		t.Errorf("width = %d, want 20", w)
	}
	if !strings.HasPrefix(out, "left") {
		t.Errorf("left not flush-left: %q", out)
	}
	if !strings.HasSuffix(out, "right") {
		t.Errorf("right not flush-right: %q", out)
	}
}

func TestStatusBarTruncatesOnOverflow(t *testing.T) {
	out := StatusBar(theme.Default(), 10, "left-side", "right-side")
	if w := lipgloss.Width(out); w > 10 {
		t.Errorf("overflow width %d > 10: %q", w, out)
	}
	if !strings.Contains(out, "left") {
		t.Errorf("left content lost: %q", out)
	}

	wide := StatusBar(theme.Default(), 5, "verylongleft", "r")
	if w := lipgloss.Width(wide); w > 5 {
		t.Errorf("left-only overflow width %d > 5", w)
	}
}

func TestNoticeRendersLevelGlyphs(t *testing.T) {
	th := theme.Default()
	cases := []struct {
		level Level
		glyph string
	}{
		{LevelInfo, "◦"},
		{LevelSuccess, "✓"},
		{LevelWarning, "◦"},
		{LevelError, "✗"},
	}
	for _, c := range cases {
		out := Notice(th, c.level, "message text", 40)
		if !strings.HasPrefix(out, c.glyph) {
			t.Errorf("level %d prefix = %q, want glyph %q", c.level, out, c.glyph)
		}
		if !strings.Contains(out, "message text") {
			t.Errorf("level %d dropped text: %q", c.level, out)
		}
	}
}

func TestNoticeTruncatesAndHandlesEmpty(t *testing.T) {
	if w := lipgloss.Width(Notice(theme.Default(), LevelError, strings.Repeat("x", 100), 20)); w > 20 {
		t.Errorf("notice width %d > 20", w)
	}
	if got := Notice(theme.Default(), LevelInfo, "", 20); got != "" {
		t.Errorf("empty text should yield empty, got %q", got)
	}
	if got := Notice(theme.Default(), LevelInfo, "hi", 0); got != "" {
		t.Errorf("zero width should yield empty, got %q", got)
	}
}

func TestNoticeCollapsesNewlinesToOneRow(t *testing.T) {
	out := Notice(theme.Default(), LevelError, "line one\nline two\r\nline three", 60)
	if strings.Contains(out, "\n") || strings.Contains(out, "\r") {
		t.Fatalf("notice is not a single row: %q", out)
	}
	for _, want := range []string{"line one", "line two", "line three"} {
		if !strings.Contains(out, want) {
			t.Errorf("collapsed notice dropped %q: %q", want, out)
		}
	}
}

func TestNoticeLinesWrapsToMultipleRows(t *testing.T) {
	th := theme.Default()
	text := "commands: /provider [name] · /model <model> · /theme [name|dark|light|auto] · /layout · /help"
	out := NoticeLines(th, LevelInfo, text, 32, 5)
	if out == "" {
		t.Fatal("NoticeLines returned empty")
	}
	lines := strings.Split(out, "\n")
	if len(lines) < 2 {
		t.Fatalf("NoticeLines did not wrap: %q", out)
	}
	if len(lines) > 5 {
		t.Errorf("NoticeLines exceeded maxLines: %d lines", len(lines))
	}
	joined := strings.Join(lines, " ")
	for _, want := range []string{"/provider", "/theme", "/layout"} {
		if !strings.Contains(joined, want) {
			t.Errorf("wrapped notice missing %q: %q", want, out)
		}
	}
	for i, line := range lines {
		if w := lipgloss.Width(line); w > 32 {
			t.Errorf("line %d width = %d, want <= 32: %q", i, w, line)
		}
	}
	// maxLines=1 collapses to single-line Notice behavior.
	one := NoticeLines(th, LevelInfo, text, 32, 1)
	if strings.Contains(one, "\n") {
		t.Errorf("maxLines=1 still multi-line: %q", one)
	}
}
