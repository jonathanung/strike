package ui

import (
	"strconv"
	"strings"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

// defaultDiffMaxLines is used when DiffPreviewOpts.MaxLines is <= 0.
const defaultDiffMaxLines = 12

// DiffPreviewOpts configures DiffPreview.
type DiffPreviewOpts struct {
	Path      string
	Old       string
	New       string
	MaxLines  int // max hunk body lines; <=0 → defaultDiffMaxLines (12)
	Width     int // required for output; <=0 → return ""
	ShowStats bool
}

type diffOp int

const (
	diffEqual diffOp = iota
	diffDelete
	diffInsert
)

type diffLine struct {
	op   diffOp
	text string
}

// DiffPreview renders a unified +/-/context diff of Old→New using theme
// DiffAdded/DiffRemoved styles. It is width-safe and height-bounded.
func DiffPreview(th theme.Theme, opts DiffPreviewOpts) string {
	if opts.Width <= 0 {
		return ""
	}
	if opts.Old == "" && opts.New == "" && opts.Path == "" && !opts.ShowStats {
		return ""
	}
	th = th.Resolve()
	st := th.S()
	ic := resolveIcons(th)
	space := strings.Repeat(" ", th.Spacing.XS)

	maxLines := opts.MaxLines
	if maxLines <= 0 {
		maxLines = defaultDiffMaxLines
	}

	lines := lineDiff(opts.Old, opts.New)
	added, removed := 0, 0
	for _, dl := range lines {
		switch dl.op {
		case diffInsert:
			added++
		case diffDelete:
			removed++
		}
	}

	var parts []string

	// Header does not consume MaxLines.
	if opts.Path != "" || opts.ShowStats {
		var headerBits []string
		if opts.Path != "" {
			headerBits = append(headerBits, st.Muted.Render(opts.Path))
		}
		if opts.ShowStats {
			headerBits = append(headerBits,
				st.DiffAdded.Render("+"+strconv.Itoa(added)),
				st.DiffRemoved.Render("-"+strconv.Itoa(removed)),
			)
		}
		header := strings.Join(headerBits, space)
		if h := truncate(th, header, opts.Width); h != "" {
			parts = append(parts, h)
		}
	}

	// Empty old+new with only a stats/path header still renders the header.
	if opts.Old == "" && opts.New == "" && len(lines) == 0 {
		return strings.Join(parts, "\n")
	}

	body, overflow := trimDiffBody(lines, maxLines)
	for _, dl := range body {
		parts = append(parts, truncate(th, formatDiffLine(st, dl), opts.Width))
	}
	if overflow > 0 {
		more := ic.Ellipsis + space + "(" + strconv.Itoa(overflow) + space + "more lines)"
		parts = append(parts, truncate(th, st.Muted.Render(more), opts.Width))
	}

	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "\n")
}

func formatDiffLine(st theme.Styles, dl diffLine) string {
	switch dl.op {
	case diffDelete:
		return st.DiffRemoved.Render("-" + dl.text)
	case diffInsert:
		return st.DiffAdded.Render("+" + dl.text)
	default:
		return st.Muted.Render(" " + dl.text)
	}
}

// trimDiffBody prefers the changed region when truncating to maxLines.
// Leading equal lines are dropped first, then trailing equal lines; if still
// over budget, the first maxLines of the remaining middle are kept.
// overflow is the count of original hunk lines not shown.
func trimDiffBody(lines []diffLine, maxLines int) (body []diffLine, overflow int) {
	if maxLines <= 0 || len(lines) <= maxLines {
		return lines, 0
	}
	start := 0
	for len(lines)-start > maxLines && start < len(lines) && lines[start].op == diffEqual {
		start++
	}
	end := len(lines)
	for end-start > maxLines && end > start && lines[end-1].op == diffEqual {
		end--
	}
	body = lines[start:end]
	if len(body) > maxLines {
		body = body[:maxLines]
	}
	return body, len(lines) - len(body)
}

// splitLines splits s on \n. An empty string yields no lines so a pure
// insertion/deletion does not invent a blank counterpart line.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// lineDiff splits old and new on \n, finds the longest common prefix and
// longest common suffix of equal lines, then treats the middle as all-old
// deleted followed by all-new inserted. This is prefix/suffix matching, not
// a full LCS diff.
func lineDiff(oldStr, newStr string) []diffLine {
	a := splitLines(oldStr)
	b := splitLines(newStr)

	pref := 0
	for pref < len(a) && pref < len(b) && a[pref] == b[pref] {
		pref++
	}
	suff := 0
	for pref+suff < len(a) && pref+suff < len(b) && a[len(a)-1-suff] == b[len(b)-1-suff] {
		suff++
	}

	out := make([]diffLine, 0, len(a)+len(b))
	for i := 0; i < pref; i++ {
		out = append(out, diffLine{op: diffEqual, text: a[i]})
	}
	for i := pref; i < len(a)-suff; i++ {
		out = append(out, diffLine{op: diffDelete, text: a[i]})
	}
	for i := pref; i < len(b)-suff; i++ {
		out = append(out, diffLine{op: diffInsert, text: b[i]})
	}
	for i := len(a) - suff; i < len(a); i++ {
		out = append(out, diffLine{op: diffEqual, text: a[i]})
	}
	return out
}
