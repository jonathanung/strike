package ui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

func sampleTree() []TreeNode {
	return []TreeNode{
		{ID: "a", Label: "alpha", Leaf: true},
		{
			ID: "b", Label: "beta", Expanded: true,
			Children: []TreeNode{
				{ID: "b1", Label: "beta-one", Leaf: true},
				{
					ID: "b2", Label: "beta-two", Expanded: false,
					Children: []TreeNode{
						{ID: "b2a", Label: "hidden", Leaf: true},
					},
				},
			},
		},
		{ID: "c", Label: "lazy", Lazy: true},
	}
}

func TestFlattenTreeSkipsCollapsedChildren(t *testing.T) {
	rows := FlattenTree(sampleTree())
	got := make([]string, len(rows))
	for i, r := range rows {
		got[i] = r.ID
	}
	want := []string{"a", "b", "b1", "b2", "c"}
	if len(got) != len(want) {
		t.Fatalf("ids = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ids = %v, want %v", got, want)
		}
	}
	// collapsed b2 must not expose hidden child
	for _, r := range rows {
		if r.ID == "hidden" || r.ID == "b2a" {
			t.Fatalf("collapsed child visible: %+v", r)
		}
	}
	// depths
	byID := map[string]TreeRow{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	if byID["a"].Depth != 0 || byID["b"].Depth != 0 {
		t.Errorf("root depths wrong: a=%d b=%d", byID["a"].Depth, byID["b"].Depth)
	}
	if byID["b1"].Depth != 1 || byID["b2"].Depth != 1 {
		t.Errorf("child depths wrong: b1=%d b2=%d", byID["b1"].Depth, byID["b2"].Depth)
	}
	if !byID["b"].Expandable || !byID["b"].Expanded {
		t.Errorf("beta should be expandable+expanded: %+v", byID["b"])
	}
	if !byID["b2"].Expandable || byID["b2"].Expanded {
		t.Errorf("beta-two should be expandable+collapsed: %+v", byID["b2"])
	}
	if !byID["c"].Expandable || byID["a"].Expandable {
		t.Errorf("lazy expandable / leaf not: c=%+v a=%+v", byID["c"], byID["a"])
	}
}

func TestFlattenTreeExpandedShowsNested(t *testing.T) {
	nodes := sampleTree()
	if !TreeToggleExpanded(nodes, []int{1, 1}) {
		t.Fatal("toggle b2 failed")
	}
	rows := FlattenTree(nodes)
	var saw bool
	for _, r := range rows {
		if r.ID == "b2a" {
			saw = true
			if r.Depth != 2 {
				t.Errorf("b2a depth = %d, want 2", r.Depth)
			}
			if got := r.Path; len(got) != 3 || got[0] != 1 || got[1] != 1 || got[2] != 0 {
				t.Errorf("b2a path = %v, want [1 1 0]", got)
			}
		}
	}
	if !saw {
		t.Fatal("expanded b2 did not show b2a")
	}
}

func TestTreeToggleExpanded(t *testing.T) {
	nodes := sampleTree()
	if TreeToggleExpanded(nodes, []int{0}) {
		t.Error("leaf should not toggle")
	}
	if !TreeToggleExpanded(nodes, []int{1}) {
		t.Fatal("beta toggle failed")
	}
	if nodes[1].Expanded {
		t.Error("beta should be collapsed after toggle")
	}
	rows := FlattenTree(nodes)
	for _, r := range rows {
		if r.ID == "b1" || r.ID == "b2" {
			t.Fatalf("children still visible after collapse: %s", r.ID)
		}
	}
	if TreeToggleExpanded(nodes, []int{9}) {
		t.Error("invalid path should not toggle")
	}
	if n, ok := TreeNodeAt(nodes, []int{1}); !ok || n.ID != "b" {
		t.Errorf("TreeNodeAt = (%+v, %v), want beta", n, ok)
	}
	if _, ok := TreeNodeAt(nodes, []int{1, 9}); ok {
		t.Error("TreeNodeAt invalid path should fail")
	}
}

func TestTreeRendersExpandCollapseAndIndent(t *testing.T) {
	th := theme.Default()
	out := Tree(th, TreeOpts{Nodes: sampleTree(), Cursor: 1, Width: 40})
	plain := ansi.Strip(out)
	lines := strings.Split(plain, "\n")
	if len(lines) != 5 {
		t.Fatalf("rows = %d, want 5\n%s", len(lines), plain)
	}
	// cursor on beta (index 1)
	if !strings.HasPrefix(lines[1], "▸ ") {
		t.Errorf("cursor row missing marker: %q", lines[1])
	}
	if !strings.Contains(lines[1], "▾") {
		t.Errorf("expanded beta missing expand glyph: %q", lines[1])
	}
	if !strings.Contains(lines[3], "▸") || !strings.Contains(lines[3], "beta-two") {
		// collapsed child uses TreeCollapsed; line also has no selection cursor
		t.Errorf("collapsed beta-two row: %q", lines[3])
	}
	// depth-1 leaf has more leading spaces than depth-0 leaf (cursor row uses a glyph)
	lead := func(s string) int { return len(s) - len(strings.TrimLeft(s, " ")) }
	if lead(lines[2]) <= lead(lines[0]) {
		t.Errorf("child not indented past root leaf: rootLead=%d childLead=%d\n%s", lead(lines[0]), lead(lines[2]), plain)
	}
	if !strings.Contains(lines[4], "lazy") {
		t.Errorf("lazy node missing: %q", lines[4])
	}
}

func TestTreeWindowKeepsCursorVisible(t *testing.T) {
	nodes := make([]TreeNode, 20)
	for i := range nodes {
		nodes[i] = TreeNode{ID: string(rune('a' + i%26)), Label: "item-" + strings.Repeat("x", i%3) + string(rune('0'+i%10)), Leaf: true}
	}
	// unique labels
	for i := range nodes {
		nodes[i].Label = "item-" + string(rune('a'+i))
		if i >= 26 {
			nodes[i].Label = "item-" + string(rune('A'+i-26))
		}
	}
	th := theme.Default()
	for _, cursor := range []int{0, 5, 10, 19} {
		out := Tree(th, TreeOpts{Nodes: nodes, Cursor: cursor, Width: 40, Visible: 5})
		lines := strings.Split(out, "\n")
		if len(lines) != 5 {
			t.Errorf("cursor %d: %d rows, want 5", cursor, len(lines))
		}
		want := nodes[cursor].Label
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

func TestTreeTinyWidthsAreSafe(t *testing.T) {
	nodes := sampleTree()
	for _, width := range []int{0, 1, 2, 8} {
		out := Tree(theme.Default(), TreeOpts{Nodes: nodes, Cursor: 0, Width: width})
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

func TestTreeEmptyState(t *testing.T) {
	if out := Tree(theme.Default(), TreeOpts{Nodes: nil, Width: 40}); !strings.Contains(out, "no items") {
		t.Errorf("default empty = %q", out)
	}
	if out := Tree(theme.Default(), TreeOpts{Nodes: nil, Width: 40, Empty: "empty tree"}); !strings.Contains(out, "empty tree") {
		t.Errorf("custom empty missing: %q", out)
	}
}

func TestTreeCurrentDetailDisabledAndTone(t *testing.T) {
	th := theme.Default()
	nodes := []TreeNode{
		{Label: "cur", Detail: "meta", Current: true, Leaf: true},
		{Label: "off", Disabled: true, Leaf: true},
		{Label: "hot", Tone: ToneSuccess, Leaf: true},
	}
	out := Tree(th, TreeOpts{Nodes: nodes, Cursor: 0, Width: 48})
	if !strings.Contains(out, "(current)") {
		t.Errorf("current tag missing: %q", out)
	}
	if !strings.Contains(ansi.Strip(out), "cur") || !strings.Contains(ansi.Strip(out), "meta") {
		t.Errorf("detail missing: %q", out)
	}
	enabled := Tree(th, TreeOpts{Nodes: []TreeNode{{Label: "row", Leaf: true}}, Cursor: 0, Width: 20})
	disabled := Tree(th, TreeOpts{Nodes: []TreeNode{{Label: "row", Disabled: true, Leaf: true}}, Cursor: 0, Width: 20})
	if enabled == disabled {
		t.Error("disabled cursor row renders identically to enabled")
	}
	toned := Tree(th, TreeOpts{Nodes: []TreeNode{{Label: "row", Tone: ToneSuccess, Leaf: true}}, Cursor: -1, Width: 20})
	plain := Tree(th, TreeOpts{Nodes: []TreeNode{{Label: "row", Leaf: true}}, Cursor: -1, Width: 20})
	if toned == plain {
		t.Error("tone does not change rendering")
	}
}

func TestTreeUsesCustomIconsAndSpacing(t *testing.T) {
	th := theme.Default()
	th.Icons.Cursor = ">"
	th.Icons.TreeExpanded = "v"
	th.Icons.TreeCollapsed = ">"
	th.Icons.DetailSeparator = "|"
	th.Spacing = theme.NewSpacing(0, 4, 3, 4) // XS=0, SM=4 indent
	nodes := []TreeNode{
		{
			Label: "root", Expanded: true,
			Children: []TreeNode{{Label: "child", Detail: "d", Leaf: true}},
		},
	}
	out := Tree(th, TreeOpts{Nodes: nodes, Cursor: 0, Width: 40})
	plain := ansi.Strip(out)
	lines := strings.Split(plain, "\n")
	if len(lines) != 2 {
		t.Fatalf("rows = %d\n%s", len(lines), plain)
	}
	if !strings.HasPrefix(lines[0], ">v") && !strings.HasPrefix(lines[0], "> v") {
		// XS=0 so cursor and expand may abut: ">vroot" or with gap from expander pad
		if !strings.Contains(lines[0], "v") || !strings.HasPrefix(lines[0], ">") {
			t.Errorf("custom cursor/expand missing: %q", lines[0])
		}
	}
	// XS=0 → no spaces around DetailSeparator
	if !strings.Contains(lines[1], "child|d") {
		t.Errorf("custom detail sep missing: %q", lines[1])
	}
	lead := func(s string) int { return len(s) - len(strings.TrimLeft(s, " ")) }
	if lead(lines[1]) < 4 {
		t.Errorf("expected SM=4 indent on child, lead=%d\n%s", lead(lines[1]), plain)
	}
}

func TestTreeLeafIgnoresChildren(t *testing.T) {
	nodes := []TreeNode{{
		Label: "file", Leaf: true, Expanded: true,
		Children: []TreeNode{{Label: "ghost", Leaf: true}},
	}}
	rows := FlattenTree(nodes)
	if len(rows) != 1 || rows[0].Expandable {
		t.Fatalf("leaf should hide children: %+v", rows)
	}
}
