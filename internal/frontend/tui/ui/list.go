package ui

import (
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/frontend/tui/theme"
)

// ListItem is one selectable row.
type ListItem struct {
	Label  string // primary text
	Detail string // muted trailing text after " — "
	// Prefix is optional pre-styled leading content (e.g. a colored glyph). It
	// is not recolored; width is measured with lipgloss.Width (ANSI-aware).
	Prefix string
	// Suffix is optional pre-styled trailing content (e.g. a Badge). It is not
	// recolored; width is measured with lipgloss.Width (ANSI-aware).
	Suffix   string
	Current  bool // tag the row "(current)" (the active selection)
	Disabled bool // render muted; callers still decide it cannot be chosen
}

// ListOpts configures List.
type ListOpts struct {
	Items   []ListItem // the (already filtered) rows to show
	Cursor  int        // index into Items of the highlighted row
	Width   int        // content width; every row is clamped to it
	Visible int        // max items shown at once (window); 0 shows all
	// Wrap word-wraps long labels/details across lines instead of ellipsis
	// truncation. Prefer for question/option bodies; leave off for dense
	// pickers where a fixed single-line window is intentional.
	Wrap bool
	// ShowFilter draws a "filter: <Filter>▏  <len(Items)>/<Total>" header.
	ShowFilter bool
	Filter     string // current filter text
	Total      int    // unfiltered count for the counter; 0 uses len(Items)
	Empty      string // message when Items is empty; default "no matches"
}

// List renders a picker body: an optional filter header, then a scrolling
// window of rows with a cursor. It powers the provider and model pickers and
// the command palette. It draws no border — wrap it in a Panel or Dialog.
//
//	body := ui.List(th, ui.ListOpts{
//	    Items:   items,
//	    Cursor:  cursor,
//	    Width:   ui.PanelInnerWidth(th, w),
//	    Visible: 10,
//	})
//	out := ui.Dialog(th, ui.DialogOpts{Title: "Select model", Width: w}, body)
//
// The window is centered on Cursor and always keeps it visible. Rows never
// exceed Width. With Wrap, one item may span multiple lines; Visible still
// counts items, not screen rows.
func List(th theme.Theme, opts ListOpts) string {
	th = th.Resolve()
	width := opts.Width
	if width < 1 {
		return ""
	}
	st := th.S()
	ic := resolveIcons(th)

	var b strings.Builder
	if opts.ShowFilter {
		total := opts.Total
		if total == 0 {
			total = len(opts.Items)
		}
		filter := st.Muted.Render("filter: ") + st.InputCursor.Render(opts.Filter+ic.FilterCursor)
		counter := st.Muted.Render(strconv.Itoa(len(opts.Items)) + "/" + strconv.Itoa(total))
		b.WriteString(truncate(th, filter+strings.Repeat(" ", th.Spacing.SM)+counter, width))
		b.WriteByte('\n')
	}

	if len(opts.Items) == 0 {
		empty := opts.Empty
		if empty == "" {
			empty = "no matches"
		}
		b.WriteString(st.Muted.Render(truncate(th, empty, width)))
		return b.String()
	}

	visible := opts.Visible
	if visible <= 0 || visible > len(opts.Items) {
		visible = len(opts.Items)
	}
	activeCursor := opts.Cursor >= 0 && opts.Cursor < len(opts.Items)
	cursor := clamp(opts.Cursor, 0, len(opts.Items)-1)
	start := clamp(cursor-visible/2, 0, max(0, len(opts.Items)-visible))
	end := min(len(opts.Items), start+visible)

	rows := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		rows = append(rows, listRow(th, st, ic, opts.Items[i], activeCursor && i == cursor, width, opts.Wrap))
	}
	b.WriteString(strings.Join(rows, "\n"))
	return b.String()
}

func listRow(th theme.Theme, st theme.Styles, ic theme.Icons, item ListItem, isCursor bool, width int, wrap bool) string {
	markerWidth := lipgloss.Width(ic.Cursor) + th.Spacing.XS
	marker := strings.Repeat(" ", markerWidth)
	if isCursor {
		marker = ic.Cursor + strings.Repeat(" ", th.Spacing.XS)
	}
	labelStyle := st.Text
	switch {
	case item.Disabled:
		// Disabled wins over selection: the cursor may rest on a disabled row
		// (it stays navigable) but the label reads muted, not highlighted.
		labelStyle = st.Muted
	case isCursor:
		labelStyle = st.Selected
	}

	label := item.Label
	if item.Current {
		label += " (current)"
	}
	prefix := item.Prefix
	prefixW := lipgloss.Width(prefix)
	prefixGap := ""
	if prefix != "" && (label != "" || item.Detail != "") {
		prefixGap = strings.Repeat(" ", th.Spacing.XS)
		prefixW += th.Spacing.XS
	}
	suffix := item.Suffix
	suffixW := lipgloss.Width(suffix)
	suffixGap := ""
	if suffix != "" {
		suffixGap = strings.Repeat(" ", th.Spacing.XS)
		suffixW += th.Spacing.XS
	}
	if wrap {
		return listRowWrapped(th, st, ic, prefix, prefixGap, prefixW, label, item.Detail, suffix, suffixGap, suffixW, marker, markerWidth, labelStyle, width)
	}
	budget := width - suffixW
	if budget < 1 {
		suffix = ""
		suffixGap = ""
		suffixW = 0
		budget = width
	}
	bodyPlain := label
	bodyStyled := labelStyle.Render(label)
	if item.Detail != "" {
		separator := strings.Repeat(" ", th.Spacing.XS) + ic.DetailSeparator + strings.Repeat(" ", th.Spacing.XS)
		bodyPlain += separator + item.Detail
		bodyStyled += st.Muted.Render(separator + item.Detail)
	}
	// Lead is marker + optional pre-styled prefix (never recolored). Reserve it
	// so overflow truncates label/detail only — legend and status samples keep
	// their semantic color at typical modal widths.
	leadW := markerWidth + prefixW
	bodyBudget := budget - leadW
	if bodyBudget < 1 {
		// Extreme narrow: cannot keep prefix styling width-safe; strip and clamp.
		plain := marker + ansi.Strip(prefix) + prefixGap + bodyPlain
		return labelStyle.Render(truncate(th, plain, width))
	}
	if lipgloss.Width(bodyPlain) > bodyBudget {
		bodyStyled = labelStyle.Render(truncate(th, bodyPlain, bodyBudget))
	}
	line := marker + prefix + prefixGap + bodyStyled
	if suffix == "" {
		return line
	}
	return line + suffixGap + suffix
}

// listRowWrapped word-wraps label and detail so primary option text is fully
// readable inside modal widths. The cursor marker and optional prefix sit on
// the first line only; continuation lines indent to the label column. Detail
// follows the label as muted wrapped lines (separator on the first detail line).
func listRowWrapped(th theme.Theme, st theme.Styles, ic theme.Icons, prefix, prefixGap string, prefixW int, label, detail, suffix, suffixGap string, suffixW int, marker string, markerWidth int, labelStyle lipgloss.Style, width int) string {
	leadW := markerWidth + prefixW
	budget := width - leadW
	if budget < 1 {
		return truncate(th, marker+ansi.Strip(prefix), width)
	}
	// Reserve suffix on the first line when it fits beside at least one cell of text.
	firstBudget := budget
	if suffix != "" {
		if budget-suffixW >= 1 {
			firstBudget = budget - suffixW
		} else {
			suffix = ""
			suffixGap = ""
			suffixW = 0
		}
	}

	labelLines := wrapPlainLines(label, firstBudget, budget)
	var lines []string
	for i, plain := range labelLines {
		styled := labelStyle.Render(plain)
		if i == 0 {
			row := marker + prefix + prefixGap + styled
			if suffix != "" {
				row += suffixGap + suffix
			}
			if lipgloss.Width(row) > width {
				// Keep pre-styled prefix; clamp the label cell into the remainder.
				remain := width - leadW
				if remain < 1 {
					row = labelStyle.Render(truncate(th, marker+ansi.Strip(prefix)+prefixGap+plain, width))
				} else {
					row = marker + prefix + prefixGap + labelStyle.Render(truncate(th, plain, remain))
				}
			}
			lines = append(lines, row)
			continue
		}
		row := strings.Repeat(" ", leadW) + styled
		if lipgloss.Width(row) > width {
			row = labelStyle.Render(truncate(th, strings.Repeat(" ", leadW)+plain, width))
		}
		lines = append(lines, row)
	}
	if detail == "" {
		return strings.Join(lines, "\n")
	}

	sep := strings.Repeat(" ", th.Spacing.XS) + ic.DetailSeparator + strings.Repeat(" ", th.Spacing.XS)
	detailPlain := sep + detail
	// First detail line may continue after a short label on the same visual
	// block; keep detail on its own wrapped block under the label column.
	for _, plain := range wrapPlainLines(detailPlain, budget, budget) {
		styled := st.Muted.Render(plain)
		row := strings.Repeat(" ", leadW) + styled
		if lipgloss.Width(row) > width {
			row = st.Muted.Render(truncate(th, strings.Repeat(" ", leadW)+plain, width))
		}
		lines = append(lines, row)
	}
	return strings.Join(lines, "\n")
}

// wrapPlainLines word-wraps plain text. firstWidth applies to the first line
// only (so a trailing suffix can share that row); later lines use nextWidth.
func wrapPlainLines(s string, firstWidth, nextWidth int) []string {
	if s == "" {
		return []string{""}
	}
	firstWidth = max(1, firstWidth)
	nextWidth = max(1, nextWidth)
	// Wrap at firstWidth, then re-wrap any overlong continuation... simpler:
	// wrap whole string at nextWidth, then if first line exceeds firstWidth,
	// re-split. For equal widths this is one pass.
	if firstWidth == nextWidth {
		return splitWrapped(wrapText(s, firstWidth))
	}
	// First line budget may be tighter; take what fits, wrap the rest at nextWidth.
	head := fitPlainPrefix(s, firstWidth)
	if head == s {
		return []string{s}
	}
	rest := strings.TrimPrefix(s, head)
	rest = strings.TrimLeft(rest, " ")
	if rest == "" {
		return []string{head}
	}
	out := []string{head}
	out = append(out, splitWrapped(wrapText(rest, nextWidth))...)
	return out
}

func splitWrapped(s string) []string {
	parts := strings.Split(s, "\n")
	for i, p := range parts {
		parts[i] = strings.TrimRight(p, " ")
	}
	return parts
}

// fitPlainPrefix returns the longest prefix of s whose display width is <= width,
// breaking on spaces when a word would overflow (hard-breaks otherwise).
func fitPlainPrefix(s string, width int) string {
	if width < 1 || s == "" {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	// Walk runes accumulating display width; remember last space.
	var b strings.Builder
	lastSpace := -1
	w := 0
	for _, r := range s {
		rw := lipgloss.Width(string(r))
		if w+rw > width {
			break
		}
		b.WriteRune(r)
		w += rw
		if r == ' ' {
			lastSpace = b.Len()
		}
	}
	out := b.String()
	if lastSpace > 0 && lipgloss.Width(strings.TrimRight(out[:lastSpace], " ")) > 0 {
		return strings.TrimRight(out[:lastSpace], " ")
	}
	if out == "" {
		// Single wide cell: force one display cell via truncate.
		return ansi.Truncate(s, width, "")
	}
	return out
}
