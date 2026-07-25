package ui

import (
	"strings"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

// DialogOpts configures Dialog.
type DialogOpts struct {
	Title  string // woven into the top border
	Hint   string // muted control hint, placed as the last body line
	Width  int    // outer width; body is clamped to PanelInnerWidth(th, Width)
	Height int    // optional fixed outer height (0 fits content)
	Tone   Tone   // border override (e.g. ToneWarning); default is the accent
}

// Dialog is the standard modal frame: a focused Panel with the title in its
// top border and a muted hint line at the foot of the body. Routing every
// modal (permission, api-key, pickers, palette) through Dialog makes them all
// look identical.
//
//	body := ui.List(th, ui.ListOpts{Items: items, Cursor: cur, Width: ui.PanelInnerWidth(th, w)})
//	out := ui.Dialog(th, ui.DialogOpts{
//	    Title: "Select model", Hint: "↑/↓ move · enter select · esc close", Width: w,
//	}, body)
//
// The body should already be wrapped to PanelInnerWidth(th, Width); Dialog
// truncates the hint and Panel truncates any over-long body line.
func Dialog(th theme.Theme, opts DialogOpts, body string) string {
	th = th.Resolve()
	content := body
	if opts.Hint != "" {
		hint := th.S().Muted.Render(truncate(th, opts.Hint, PanelInnerWidth(th, opts.Width)))
		content = strings.TrimRight(body, "\n") + strings.Repeat("\n", max(1, th.Spacing.SM)) + hint
	}
	return Panel(th, PanelOpts{
		Title:   opts.Title,
		Width:   opts.Width,
		Height:  opts.Height,
		Focused: true,
		Tone:    opts.Tone,
	}, content)
}
