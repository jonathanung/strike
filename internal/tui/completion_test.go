package tui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

// Keep completion rendering's allocation explicit: callers must provide the
// popup rectangle rather than relying on state-derived height.
var _ func(*completionState, int, int, theme.Theme) string = (*completionState).view

func TestCompletionViewUsesExactlyAllocatedRows(t *testing.T) {
	completion := leadingSlashCompletion("/", 0, 1, commandCatalog(nil))
	if completion == nil {
		t.Fatal("completion did not open")
	}
	for _, height := range []int{0, 1, 2} {
		out := completion.view(40, height, theme.Default())
		if height == 0 {
			if out != "" {
				t.Errorf("zero-row completion = %q, want empty", out)
			}
			continue
		}
		if rows := len(strings.Split(out, "\n")); rows != height {
			t.Errorf("height %d completion rows = %d, want exact allocation", height, rows)
		}
	}
}

func TestCommandMatchesRanksCaseInsensitivelyAndStably(t *testing.T) {
	catalog := []commandSpec{
		{Name: "/praline", Source: commandSourceSkill},
		{Name: "/provider", Source: commandSourceBuiltin},
		{Name: "/project", Source: commandSourceSkill},
		{Name: "/paper", Source: commandSourceSkill},
		{Name: "/PR", Source: commandSourceSkill},
	}

	matches := commandMatches(catalog, "/Pr")
	var got []string
	for _, match := range matches {
		got = append(got, match.Spec.Name)
	}
	want := []string{"/PR", "/praline", "/provider", "/project", "/paper"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ranked matches = %q, want exact, then stable prefixes, then subsequences %q", got, want)
	}

	if got := commandMatches(catalog, "/zzz"); len(got) != 0 {
		t.Fatalf("no-match query returned %d candidates", len(got))
	}
}

func TestProviderIsFirstMatchForPr(t *testing.T) {
	matches := commandMatches(commandCatalog(nil), "/pr")
	if len(matches) == 0 || matches[0].Spec.Name != "/provider" {
		t.Fatalf("first /pr match = %#v, want /provider", matches)
	}
}

func TestLeadingSlashCompletionActivationIsLimitedToFirstLineTokenAtCursor(t *testing.T) {
	catalog := commandCatalog(nil)
	tests := []struct {
		name      string
		value     string
		row, col  int
		wantOpen  bool
		wantRange runeRange
	}{
		{name: "token middle", value: "/provider argument\nlater", col: 3, wantOpen: true, wantRange: runeRange{Start: 0, End: 9}},
		{name: "token end", value: "/pr argument", col: 3, wantOpen: true, wantRange: runeRange{Start: 0, End: 3}},
		{name: "cursor after token", value: "/pr argument", col: 4},
		{name: "cursor at initial slash", value: "/pr", col: 0},
		{name: "not leading", value: "x/pr", col: 4},
		{name: "later line", value: "first\n/pr", row: 1, col: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			completion := leadingSlashCompletion(tt.value, tt.row, tt.col, catalog)
			if (completion != nil) != tt.wantOpen {
				t.Fatalf("completion open = %v, want %v", completion != nil, tt.wantOpen)
			}
			if completion != nil && completion.Replace != tt.wantRange {
				t.Errorf("replacement range = %#v, want %#v", completion.Replace, tt.wantRange)
			}
		})
	}
}

func TestCompletionMoveWrapsSelection(t *testing.T) {
	c := leadingSlashCompletion("/", 0, 1, commandCatalog(nil))
	if c == nil {
		t.Fatal("completion did not open")
	}
	c.move(-1)
	if c.Selected != len(c.Candidates)-1 {
		t.Errorf("moving up from first selected %d, want last %d", c.Selected, len(c.Candidates)-1)
	}
	c.move(1)
	if c.Selected != 0 {
		t.Errorf("moving down from last selected %d, want first", c.Selected)
	}
}
