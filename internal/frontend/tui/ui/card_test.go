package ui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/frontend/tui/theme"
)

func countTitleRows(s string, titles ...string) int {
	n := 0
	for _, line := range strings.Split(ansi.Strip(s), "\n") {
		for _, title := range titles {
			if strings.Contains(line, title) {
				n++
				break
			}
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
	plainTop := ansi.Strip(top)
	if !strings.Contains(plainTop, "a") || !strings.Contains(plainTop, "b") {
		t.Errorf("first row should carry both card titles: %q", plainTop)
	}
	if got := countTitleRows(out, "a", "b"); got != 2 {
		// one row with both titles counted once each across lines — top has both
		if strings.Count(plainTop, "a") != 1 || !strings.Contains(plainTop, "b") {
			t.Errorf("cards should share one row, titles missing: %q", plainTop)
		}
	}
}

func TestBentoWrapsToNextRowWhenOverflowing(t *testing.T) {
	cards := []Card{
		{Title: "card-a", Body: "aa", Width: 20},
		{Title: "card-b", Body: "bb", Width: 20},
		{Title: "card-c", Body: "cc", Width: 20},
	}
	out := Bento(theme.Default(), 44, cards)
	if w := lipgloss.Width(out); w > 44 {
		t.Errorf("bento width %d exceeds 44", w)
	}
	// Three cards of width 20 at outer 44: first row packs two (20+gap+20), third wraps.
	lines := strings.Split(ansi.Strip(out), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected wrapped bento rows, got:\n%s", out)
	}
	if !strings.Contains(lines[0], "card-a") || !strings.Contains(lines[0], "card-b") {
		t.Errorf("first row missing a/b titles: %q", lines[0])
	}
	// Find a later title row with card-c only.
	foundC := false
	for i := 1; i < len(lines); i++ {
		if strings.Contains(lines[i], "card-c") {
			foundC = true
			if strings.Contains(lines[i], "card-a") {
				t.Errorf("card-c did not wrap to its own row: %q", lines[i])
			}
			break
		}
	}
	if !foundC {
		t.Errorf("card-c title missing after wrap:\n%s", out)
	}
}

func TestBentoCollapsesToSingleColumnWhenNarrow(t *testing.T) {
	cards := []Card{{Title: "card-a", Body: "aa", Width: 30}, {Title: "card-b", Body: "bb", Width: 30}}
	out := Bento(theme.Default(), 20, cards)
	if w := lipgloss.Width(out); w > 20 {
		t.Errorf("narrow bento width %d exceeds 20", w)
	}
	plain := ansi.Strip(out)
	aIdx := strings.Index(plain, "card-a")
	bIdx := strings.Index(plain, "card-b")
	if aIdx < 0 || bIdx < 0 || bIdx <= aIdx {
		t.Errorf("narrow bento should stack a then b:\n%s", plain)
	}
	// Titles on separate chrome rows.
	if strings.Count(plain, "\n") < 3 {
		t.Errorf("expected multi-row single column, got:\n%s", plain)
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

	compactOut := ansi.Strip(Bento(compact, 20, cards))
	if !strings.Contains(strings.Split(compactOut, "\n")[0], "a") || !strings.Contains(strings.Split(compactOut, "\n")[0], "b") {
		t.Errorf("zero-gap bento should pack both titles on one row: %q", compactOut)
	}
	roomyOut := ansi.Strip(Bento(roomy, 20, cards))
	roomyTop := strings.Split(roomyOut, "\n")[0]
	if strings.Contains(roomyTop, "a") && strings.Contains(roomyTop, "b") {
		t.Errorf("theme-gap bento should wrap second card, top=%q", roomyTop)
	}
}
