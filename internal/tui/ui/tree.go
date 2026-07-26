package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

// TreeNode is one node in a Tree. Children render only when Expanded is true.
// Lazy marks a node that may gain children later — it still shows an expand
// control when Children is empty. Leaf forces a non-expandable row even if
// Children is non-empty (callers should leave Children nil for leaves).
type TreeNode struct {
	ID     string // optional stable identity for callers
	Label  string
	Detail string // muted trailing text after DetailSeparator
	// Suffix is optional pre-styled trailing content (e.g. a Badge). Not recolored.
	Suffix   string
	Children []TreeNode
	Expanded bool
	Lazy     bool // expandable with no Children yet (lazy-load placeholder)
	Leaf     bool // never shows expand/collapse control
	Disabled bool
	Current  bool // tag the row "(current)"
	Tone     Tone // label color when not selected/disabled; Default = Text
}

// TreeOpts configures Tree.
type TreeOpts struct {
	Nodes   []TreeNode // root nodes
	Cursor  int        // index into FlattenTree(Nodes) of the highlighted row
	Width   int        // content width; every row is clamped to it
	Visible int        // max rows shown at once (window); 0 shows all
	Empty   string     // message when no visible rows; default "no items"
}

// TreeRow is one visible row after expand/collapse flattening. Path is the
// index path from the root Nodes slice (nodes[path[0]].Children[path[1]]…).
type TreeRow struct {
	Path       []int
	Depth      int
	ID         string
	Label      string
	Detail     string
	Suffix     string
	Expanded   bool
	Expandable bool
	Disabled   bool
	Current    bool
	Tone       Tone
}

// FlattenTree walks nodes depth-first and returns one TreeRow per visible
// node, skipping children of collapsed nodes. Callers use it to map Cursor
// indices onto nodes for navigation and expand/collapse.
func FlattenTree(nodes []TreeNode) []TreeRow {
	var out []TreeRow
	var walk func(ns []TreeNode, prefix []int, depth int)
	walk = func(ns []TreeNode, prefix []int, depth int) {
		for i, n := range ns {
			path := make([]int, len(prefix)+1)
			copy(path, prefix)
			path[len(prefix)] = i
			exp := treeExpandable(n)
			out = append(out, TreeRow{
				Path:       path,
				Depth:      depth,
				ID:         n.ID,
				Label:      n.Label,
				Detail:     n.Detail,
				Suffix:     n.Suffix,
				Expanded:   n.Expanded,
				Expandable: exp,
				Disabled:   n.Disabled,
				Current:    n.Current,
				Tone:       n.Tone,
			})
			if exp && n.Expanded && len(n.Children) > 0 {
				walk(n.Children, path, depth+1)
			}
		}
	}
	walk(nodes, nil, 0)
	return out
}

// TreeNodeAt returns the node at path, or false when the path is invalid.
func TreeNodeAt(nodes []TreeNode, path []int) (TreeNode, bool) {
	p := treeNodePtr(nodes, path)
	if p == nil {
		return TreeNode{}, false
	}
	return *p, true
}

// TreeToggleExpanded flips Expanded on the node at path when it is expandable.
// It mutates nodes in place and returns true when a flip occurred.
func TreeToggleExpanded(nodes []TreeNode, path []int) bool {
	p := treeNodePtr(nodes, path)
	if p == nil || !treeExpandable(*p) {
		return false
	}
	p.Expanded = !p.Expanded
	return true
}

// Tree renders an expand/collapse tree body: a scrolling window of indented
// rows with a cursor. It powers the file explorer and agent multiplexing
// trees. It draws no border — wrap it in a Panel or Dialog.
//
//	rows := ui.FlattenTree(nodes)
//	body := ui.Tree(th, ui.TreeOpts{
//	    Nodes:   nodes,
//	    Cursor:  cursor,
//	    Width:   ui.PanelInnerWidth(th, w),
//	    Visible: h,
//	})
//
// The window is centered on Cursor and always keeps it visible. Rows never
// exceed Width. Lazy children are a data concern: keep Lazy set and fill
// Children when the parent expands.
func Tree(th theme.Theme, opts TreeOpts) string {
	th = th.Resolve()
	width := opts.Width
	if width < 1 {
		return ""
	}
	st := th.S()
	ic := resolveIcons(th)
	rows := FlattenTree(opts.Nodes)

	if len(rows) == 0 {
		empty := opts.Empty
		if empty == "" {
			empty = "no items"
		}
		return st.Muted.Render(truncate(th, empty, width))
	}

	visible := opts.Visible
	if visible <= 0 || visible > len(rows) {
		visible = len(rows)
	}
	activeCursor := opts.Cursor >= 0 && opts.Cursor < len(rows)
	cursor := clamp(opts.Cursor, 0, len(rows)-1)
	start := clamp(cursor-visible/2, 0, max(0, len(rows)-visible))
	end := min(len(rows), start+visible)

	out := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		out = append(out, treeRow(th, st, ic, rows[i], activeCursor && i == cursor, width))
	}
	return strings.Join(out, "\n")
}

func treeExpandable(n TreeNode) bool {
	if n.Leaf {
		return false
	}
	return n.Lazy || len(n.Children) > 0
}

func treeNodePtr(nodes []TreeNode, path []int) *TreeNode {
	if len(path) == 0 {
		return nil
	}
	if path[0] < 0 || path[0] >= len(nodes) {
		return nil
	}
	if len(path) == 1 {
		return &nodes[path[0]]
	}
	return treeNodePtr(nodes[path[0]].Children, path[1:])
}

func treeRow(th theme.Theme, st theme.Styles, ic theme.Icons, row TreeRow, isCursor bool, width int) string {
	markerWidth := lipgloss.Width(ic.Cursor) + th.Spacing.XS
	marker := strings.Repeat(" ", markerWidth)
	if isCursor {
		marker = ic.Cursor + strings.Repeat(" ", th.Spacing.XS)
	}

	indent := strings.Repeat(" ", row.Depth*th.Spacing.SM)
	expW := max(lipgloss.Width(ic.TreeExpanded), lipgloss.Width(ic.TreeCollapsed))
	exp := strings.Repeat(" ", expW)
	if row.Expandable {
		if row.Expanded {
			exp = padRight(th, ic.TreeExpanded, expW)
		} else {
			exp = padRight(th, ic.TreeCollapsed, expW)
		}
	}
	gap := strings.Repeat(" ", th.Spacing.XS)
	prefix := marker + indent + exp + gap

	labelStyle := st.Text
	switch {
	case row.Disabled:
		labelStyle = st.Muted
	case isCursor:
		labelStyle = st.Selected
	case row.Tone != ToneDefault:
		labelStyle = toneStyle(th, row.Tone)
	}

	label := row.Label
	if row.Current {
		label += " (current)"
	}
	suffix := row.Suffix
	suffixW := lipgloss.Width(suffix)
	suffixGap := ""
	if suffix != "" {
		suffixGap = strings.Repeat(" ", th.Spacing.XS)
		suffixW += th.Spacing.XS
	}
	budget := width - suffixW
	if budget < 1 {
		suffix = ""
		suffixGap = ""
		suffixW = 0
		budget = width
	}
	plain := prefix + label
	line := prefix + labelStyle.Render(label)
	if row.Detail != "" {
		separator := strings.Repeat(" ", th.Spacing.XS) + ic.DetailSeparator + strings.Repeat(" ", th.Spacing.XS)
		plain += separator + row.Detail
		line += st.Muted.Render(separator + row.Detail)
	}
	if lipgloss.Width(plain) > budget {
		return labelStyle.Render(truncate(th, plain, width))
	}
	if suffix == "" {
		return line
	}
	return line + suffixGap + suffix
}
