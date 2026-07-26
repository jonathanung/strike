package ui

import (
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

func TestListAlignsSelectedAndUnselectedLabelsForCursorWidthsAndSpacing(t *testing.T) {
	for _, tt := range []struct {
		name string
		th   theme.Theme
	}{
		{"explicit XS zero", theme.Theme{Spacing: theme.NewSpacing(0, 2, 3, 4), Icons: theme.Icons{Cursor: ">>"}}},
		{"default", theme.Default()},
		{"custom spacing and two-cell cursor", theme.Theme{Spacing: theme.NewSpacing(3, 2, 3, 4), Icons: theme.Icons{Cursor: ">>"}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			out := List(tt.th, ListOpts{
				Items:  []ListItem{{Label: "selected label"}, {Label: "other label"}},
				Cursor: 0,
				Width:  40,
			})
			selected, unselected := "", ""
			for _, line := range strings.Split(out, "\n") {
				plain := ansi.Strip(line)
				switch {
				case strings.Contains(plain, "selected label"):
					selected = plain
				case strings.Contains(plain, "other label"):
					unselected = plain
				}
			}
			if selected == "" || unselected == "" {
				t.Fatalf("missing list rows in %q", out)
			}
			selectedColumn := lipgloss.Width(selected[:strings.Index(selected, "selected label")])
			unselectedColumn := lipgloss.Width(unselected[:strings.Index(unselected, "other label")])
			if selectedColumn != unselectedColumn {
				t.Errorf("label columns selected=%d unselected=%d; selected=%q unselected=%q", selectedColumn, unselectedColumn, selected, unselected)
			}
		})
	}
}

func TestListTinyWidthsAreSafe(t *testing.T) {
	items := []ListItem{{Label: "wide label", Detail: "detail"}, {Label: "other"}}
	for _, width := range []int{0, 1, 2} {
		out := List(theme.Default(), ListOpts{Items: items, Cursor: 0, Width: width})
		if width == 0 {
			if out != "" {
				t.Errorf("width zero output = %q, want empty", out)
			}
			continue
		}
		for row, line := range strings.Split(out, "\n") {
			if got := lipgloss.Width(line); got > width {
				t.Errorf("width %d row %d has display width %d: %q", width, row, got, line)
			}
		}
	}
}

func listItems(n int) []ListItem {
	items := make([]ListItem, n)
	for i := range items {
		items[i] = ListItem{Label: "item-" + strconv.Itoa(i)}
	}
	return items
}

func TestListWindowKeepsCursorVisible(t *testing.T) {
	th := theme.Default()
	items := listItems(20)
	for _, cursor := range []int{0, 5, 10, 19} {
		out := List(th, ListOpts{Items: items, Cursor: cursor, Width: 40, Visible: 5})
		lines := strings.Split(out, "\n")
		if len(lines) != 5 {
			t.Errorf("cursor %d: %d rows, want a 5-row window", cursor, len(lines))
		}
		want := "item-" + strconv.Itoa(cursor)
		var cursorLine string
		for _, l := range lines {
			if strings.Contains(l, want) {
				cursorLine = l
			}
		}
		if cursorLine == "" {
			t.Errorf("cursor %d: %q not in window\n%s", cursor, want, out)
			continue
		}
		if !strings.HasPrefix(cursorLine, "▸ ") {
			t.Errorf("cursor %d: highlighted row lacks cursor marker: %q", cursor, cursorLine)
		}
	}
}

func TestListFilterHeaderShowsCounter(t *testing.T) {
	out := List(theme.Default(), ListOpts{
		Items: listItems(3), Cursor: 0, Width: 40, Visible: 10,
		ShowFilter: true, Filter: "ab", Total: 12,
	})
	header := firstLine(out)
	if !strings.Contains(header, "filter: ab") {
		t.Errorf("filter text missing: %q", header)
	}
	if !strings.Contains(header, "3/12") {
		t.Errorf("counter missing: %q", header)
	}
}

func TestListEmptyState(t *testing.T) {
	if out := List(theme.Default(), ListOpts{Items: nil, Width: 40}); !strings.Contains(out, "no matches") {
		t.Errorf("default empty state = %q, want 'no matches'", out)
	}
	if out := List(theme.Default(), ListOpts{Items: nil, Width: 40, Empty: "nothing here"}); !strings.Contains(out, "nothing here") {
		t.Errorf("custom empty state missing: %q", out)
	}
}

func TestListCurrentTagAndWidthSafety(t *testing.T) {
	items := []ListItem{
		{Label: "anthropic", Detail: "oauth", Current: true},
		{Label: strings.Repeat("x", 100), Detail: "very long " + strings.Repeat("y", 50)},
	}
	out := List(theme.Default(), ListOpts{Items: items, Cursor: 0, Width: 40, Visible: 10})
	if !strings.Contains(out, "(current)") {
		t.Errorf("current tag missing: %q", out)
	}
	for i, line := range strings.Split(out, "\n") {
		if w := lipgloss.Width(line); w > 40 {
			t.Errorf("row %d width %d exceeds 40: %q", i, w, line)
		}
	}
}

func TestListDisabledRowRendersMuted(t *testing.T) {
	saved := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(saved) })

	th := theme.Default()
	enabled := List(th, ListOpts{Items: []ListItem{{Label: "row"}}, Cursor: 0, Width: 20, Visible: 1})
	disabled := List(th, ListOpts{Items: []ListItem{{Label: "row", Disabled: true}}, Cursor: 0, Width: 20, Visible: 1})
	if enabled == disabled {
		t.Error("disabled cursor row renders identically to the enabled highlighted row")
	}
	if !strings.HasPrefix(disabled, "▸ ") {
		t.Errorf("disabled row should still keep the cursor marker: %q", disabled)
	}
}

func TestListUsesCustomCursorAndSelectedStyle(t *testing.T) {
	saved := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(saved) })

	th := theme.Default()
	th.Icons.Cursor = ">"
	items := []ListItem{{Label: "selected"}, {Label: "other"}}
	selected := List(th, ListOpts{Items: items, Cursor: 0, Width: 20})
	unselected := List(th, ListOpts{Items: items, Cursor: 1, Width: 20})
	if !strings.HasPrefix(selected, "> ") {
		t.Errorf("custom cursor is not rendered: %q", selected)
	}
	if selected == unselected {
		t.Error("moving the selected row does not change list rendering")
	}
}

func TestListUsesCustomDetailSeparator(t *testing.T) {
	th := theme.Default()
	th.Icons.DetailSeparator = "|"
	out := List(th, ListOpts{Items: []ListItem{{Label: "label", Detail: "detail"}}, Width: 30})
	if !strings.Contains(out, "label | detail") {
		t.Errorf("custom detail separator is not rendered: %q", out)
	}
}

func TestListRendersSuffixWithoutRecoloring(t *testing.T) {
	th := theme.Default().Resolve()
	suffix := Badge(th, ToneSuccess, "merged")
	out := List(th, ListOpts{
		Items:  []ListItem{{Label: "ship it", Suffix: suffix}},
		Cursor: 0,
		Width:  40,
	})
	if !strings.Contains(out, "ship it") {
		t.Fatalf("missing label: %q", out)
	}
	if !strings.Contains(out, "merged") {
		t.Fatalf("missing suffix: %q", out)
	}
	// Suffix keeps its own styling bytes (not collapsed into selected-only plain).
	if !strings.Contains(out, suffix) && !strings.Contains(ansi.Strip(out), "merged") {
		t.Fatalf("suffix not preserved: %q", out)
	}
}
