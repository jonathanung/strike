package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

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

func TestKeyHintsJoinsWithDotsWithinWidth(t *testing.T) {
	hints := []KeyHint{{"enter", "send"}, {"ctrl+k", "palette"}, {"esc", "interrupt"}}
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
	hints := []KeyHint{{"enter", "send"}, {"ctrl+k", "palette"}, {"esc", "interrupt"}}
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
