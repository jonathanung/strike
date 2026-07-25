package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

func countTopBorderRows(s string) int {
	n := 0
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, "╭") {
			n++
		}
	}
	return n
}

func TestBentoPacksCardsIntoOneRowThatFitsWidth(t *testing.T) {
	cards := []Card{{Title: "a", Body: "aa", Width: 20}, {Title: "b", Body: "bb", Width: 20}}
	out := Bento(theme.Default(), 42, cards) // 20 + default theme gap + 20
	top := firstLine(out)
	if w := lipgloss.Width(top); w != 42 {
		t.Errorf("row width = %d, want 42", w)
	}
	if got := strings.Count(top, "╭"); got != 2 {
		t.Errorf("first row should carry 2 card tops, got %d: %q", got, top)
	}
	if got := countTopBorderRows(out); got != 1 {
		t.Errorf("cards should share one row, got %d rows", got)
	}
}

func TestBentoWrapsToNextRowWhenOverflowing(t *testing.T) {
	cards := []Card{
		{Title: "a", Body: "aa", Width: 20},
		{Title: "b", Body: "bb", Width: 20},
		{Title: "c", Body: "cc", Width: 20},
	}
	out := Bento(theme.Default(), 44, cards)
	if w := lipgloss.Width(out); w > 44 {
		t.Errorf("bento width %d exceeds 44", w)
	}
	if got := countTopBorderRows(out); got != 2 {
		t.Errorf("three cards at width 44 should wrap to 2 rows, got %d\n%s", got, out)
	}
}

func TestBentoCollapsesToSingleColumnWhenNarrow(t *testing.T) {
	cards := []Card{{Title: "a", Body: "aa", Width: 30}, {Title: "b", Body: "bb", Width: 30}}
	out := Bento(theme.Default(), 20, cards)
	if w := lipgloss.Width(out); w > 20 {
		t.Errorf("narrow bento width %d exceeds 20", w)
	}
	if got := countTopBorderRows(out); got != 2 {
		t.Errorf("narrow bento should be single-column (2 rows), got %d\n%s", got, out)
	}
}

func TestBentoRowCardsShareHeight(t *testing.T) {
	cards := []Card{
		{Title: "short", Body: "one line", Width: 20},
		{Title: "tall", Body: "one\ntwo\nthree\nfour", Width: 20},
	}
	out := Bento(theme.Default(), 42, cards)
	for i, line := range strings.Split(out, "\n") {
		if w := lipgloss.Width(line); w != 42 {
			t.Errorf("line %d width = %d, want 42 (cards not height-aligned)", i, w)
		}
	}
}

func TestBentoTinyWidthDoesNotPanic(t *testing.T) {
	out := Bento(theme.Default(), 5, []Card{{Title: "a", Body: "x", Width: 30}})
	if w := lipgloss.Width(out); w > 5 {
		t.Errorf("width %d exceeds 5", w)
	}
}

func TestCardWrapsToThemePanelInnerWidth(t *testing.T) {
	th := theme.Default()
	th.Spacing = theme.NewSpacing(0, 0, 0, 0)
	width := 12
	body := "one two three four five"
	out := Bento(th, width, []Card{{Title: "card", Body: body, Width: width}})
	if !strings.Contains(out, "one two") || !strings.Contains(out, "three") {
		t.Errorf("card did not wrap body to its panel budget:\n%s", out)
	}
	for i, line := range strings.Split(out, "\n") {
		if got := lipgloss.Width(line); got != width {
			t.Errorf("line %d width = %d, want %d", i, got, width)
		}
	}
}

func TestBentoUsesThemeGapForRowPacking(t *testing.T) {
	cards := []Card{{Title: "a", Body: "a", Width: 10}, {Title: "b", Body: "b", Width: 10}}
	compact := theme.Default()
	compact.Spacing = theme.NewSpacing(0, 0, 0, 0)
	roomy := theme.Default()
	roomy.Spacing = theme.NewSpacing(0, 4, 0, 0)

	if rows := countTopBorderRows(Bento(compact, 20, cards)); rows != 1 {
		t.Errorf("zero-gap bento rows = %d, want 1", rows)
	}
	if rows := countTopBorderRows(Bento(roomy, 20, cards)); rows != 2 {
		t.Errorf("theme-gap bento rows = %d, want 2", rows)
	}
}
