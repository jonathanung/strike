package ui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

// ListItem is one selectable row.
type ListItem struct {
	Label    string // primary text
	Detail   string // muted trailing text after " — "
	Current  bool   // tag the row "(current)" (the active selection)
	Disabled bool   // render muted; callers still decide it cannot be chosen
}

// ListOpts configures List.
type ListOpts struct {
	Items   []ListItem // the (already filtered) rows to show
	Cursor  int        // index into Items of the highlighted row
	Width   int        // content width; every row is clamped to it
	Visible int        // max rows shown at once (window); 0 shows all
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
//	    Width:   ui.InnerWidth(w),
//	    Visible: 10,
//	})
//	out := ui.Dialog(th, ui.DialogOpts{Title: "Select model", Width: w}, body)
//
// The window is centered on Cursor and always keeps it visible. Rows never
// exceed Width.
func List(th theme.Theme, opts ListOpts) string {
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
		filter := st.Muted.Render("filter: ") + st.Text.Render(opts.Filter+"▏")
		counter := st.Muted.Render(strconv.Itoa(len(opts.Items)) + "/" + strconv.Itoa(total))
		b.WriteString(truncate(filter+"  "+counter, width))
		b.WriteByte('\n')
	}

	if len(opts.Items) == 0 {
		empty := opts.Empty
		if empty == "" {
			empty = "no matches"
		}
		b.WriteString(st.Muted.Render(truncate(empty, width)))
		return b.String()
	}

	visible := opts.Visible
	if visible <= 0 || visible > len(opts.Items) {
		visible = len(opts.Items)
	}
	cursor := clamp(opts.Cursor, 0, len(opts.Items)-1)
	start := clamp(cursor-visible/2, 0, max(0, len(opts.Items)-visible))
	end := min(len(opts.Items), start+visible)

	selected := lipgloss.NewStyle().Foreground(th.Highlight).Bold(true)
	rows := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		rows = append(rows, listRow(st, ic, selected, opts.Items[i], i == cursor, width))
	}
	b.WriteString(strings.Join(rows, "\n"))
	return b.String()
}

func listRow(st theme.Styles, ic theme.Icons, selected lipgloss.Style, item ListItem, isCursor bool, width int) string {
	marker := "  "
	if isCursor {
		marker = ic.Cursor + " "
	}
	labelStyle := st.Text
	switch {
	case item.Disabled:
		// Disabled wins over selection: the cursor may rest on a disabled row
		// (it stays navigable) but the label reads muted, not highlighted.
		labelStyle = st.Muted
	case isCursor:
		labelStyle = selected
	}

	label := item.Label
	if item.Current {
		label += " (current)"
	}
	plain := marker + label
	line := marker + labelStyle.Render(label)
	if item.Detail != "" {
		plain += " — " + item.Detail
		line += st.Muted.Render(" — " + item.Detail)
	}
	if lipgloss.Width(plain) > width {
		// Restyle the whole truncated row in one style to keep it width-safe.
		return labelStyle.Render(truncate(plain, width))
	}
	return line
}
