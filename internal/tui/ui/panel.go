package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

// PanelOpts configures Panel.
type PanelOpts struct {
	// Title is embedded in the top border: ╭─ Title ─────╮. Empty draws a
	// plain top border.
	Title string
	// Footer is a short hint embedded in the bottom border, muted:
	// ╰─ esc close ─╯. Long hints are better placed inside the body (see
	// Dialog); the border truncates.
	Footer string
	// Width is the mandatory outer width. Output is exactly this many cells
	// wide (borders included) when Width >= 3; narrower panels drop the
	// border and degrade to plain, truncated text.
	Width int
	// Height, when > 0, fixes the outer height: content is truncated or
	// blank-padded to fit. Heights below two render structurally without panel
	// chrome. Zero fits the content.
	Height int
	// Borderless omits panel chrome and padding. The body is structurally fit
	// to Width and Height; Title, Footer, Focused, Dim, and Tone are ignored.
	Borderless bool
	// Focused selects BorderFocus instead of Border. When Tone is default,
	// Focused wins over Dim (full precedence: Tone > Focused > Dim > default).
	Focused bool
	// Dim selects BorderMuted for calm, inactive chrome (e.g. bento tiles).
	// Ignored when Focused or Tone is set.
	Dim bool
	// Tone overrides the border color entirely (e.g. ToneWarning for a
	// permission dialog). Non-default Tone wins over Focused and Dim.
	Tone Tone
}

// Panel is the framed tile primitive: a rounded border with its title woven
// into the top edge and an optional hint woven into the bottom edge. It is
// the building block for Dialog and Card and for the app's transcript and
// composer regions.
//
//	body := "streaming transcript…"
//	out := ui.Panel(th, ui.PanelOpts{Title: "session", Width: 60, Focused: true}, body)
//	// ╭─ session ───────────────────────────────────────────────╮
//	// │ streaming transcript…                                    │
//	// ╰──────────────────────────────────────────────────────────╯
//
// Output never exceeds Width. Themed callers wrap body to
// PanelInnerWidth(th, Width) for the nicest result; Panel truncates any line
// that is still too long.
func Panel(th theme.Theme, opts PanelOpts, body string) string {
	th = th.Resolve()
	if opts.Width < 1 {
		return ""
	}
	width := opts.Width
	if opts.Borderless || (opts.Height > 0 && opts.Height < 2) {
		rows := strings.Split(body, "\n")
		for i, row := range rows {
			rows[i] = padRight(th, row, width)
		}
		if opts.Height > 0 {
			rows = fitRows(th, rows, opts.Height, width)
		}
		return strings.Join(rows, "\n")
	}
	bordered, padX, inner := panelMetrics(th, width)

	rows := strings.Split(body, "\n")
	for i, r := range rows {
		rows[i] = padRight(th, r, inner)
	}
	if opts.Height > 0 {
		contentRows := opts.Height
		if bordered {
			contentRows = max(0, opts.Height-2)
		}
		rows = fitRows(th, rows, contentRows, inner)
	}

	if !bordered {
		// Too narrow for a border: plain, width-clamped text.
		return strings.Join(rows, "\n")
	}

	color := panelBorderColor(th, opts)
	bs := lipgloss.NewStyle().Foreground(color)
	border := th.BorderStyle
	horiz := width - 2

	var b strings.Builder
	b.WriteString(bs.Render(border.TopLeft))
	b.WriteString(edgeBorder(th, opts.Title, horiz, color, th.S().Title))
	b.WriteString(bs.Render(border.TopRight))
	for _, row := range rows {
		b.WriteByte('\n')
		b.WriteString(bs.Render(border.Vertical))
		b.WriteString(strings.Repeat(" ", padX))
		b.WriteString(row)
		b.WriteString(strings.Repeat(" ", padX))
		b.WriteString(bs.Render(border.Vertical))
	}
	b.WriteByte('\n')
	b.WriteString(bs.Render(border.BottomLeft))
	b.WriteString(edgeBorder(th, opts.Footer, horiz, color, th.S().Muted))
	b.WriteString(bs.Render(border.BottomRight))
	return b.String()
}

// InnerWidth is the default-theme compatibility helper for the content width
// inside a Panel of the given outer width. Themed callers must use
// PanelInnerWidth. It returns width itself when too narrow for a border, and
// 0 when width <= 0.
func InnerWidth(width int) int {
	return PanelInnerWidth(theme.Default(), width)
}

// PanelInnerWidth reports the content width using th's resolved panel spacing.
// Themed callers must use this instead of InnerWidth.
func PanelInnerWidth(th theme.Theme, width int) int {
	_, _, inner := panelMetrics(th.Resolve(), width)
	return inner
}

// PanelInnerHeight reports the body height available inside a Panel with the
// supplied outer dimensions. It clamps nonpositive dimensions to zero and
// subtracts border rows only when a border fits at width and height.
func PanelInnerHeight(width, height int) int {
	if width <= 0 || height <= 0 {
		return 0
	}
	if width < 3 || height < 2 {
		return height
	}
	return max(0, height-2)
}

// panelMetrics reports, for an outer width: whether a border fits, the
// horizontal padding inside it, and the resulting content width.
func panelMetrics(th theme.Theme, width int) (bordered bool, padX, inner int) {
	switch {
	case width < 1:
		return false, 0, 0
	case width < 3:
		return false, 0, width
	case width < 6:
		return true, 0, width - 2
	default:
		padX = clamp(th.Spacing.XS, 0, (width-3)/2)
		return true, padX, width - 2 - 2*padX
	}
}

func panelBorderColor(th theme.Theme, opts PanelOpts) lipgloss.AdaptiveColor {
	th = th.Resolve()
	switch {
	case opts.Tone != ToneDefault:
		return toneColor(th, opts.Tone)
	case opts.Focused:
		return th.BorderFocus
	case opts.Dim:
		return th.BorderMuted
	default:
		return th.Border
	}
}

// edgeBorder builds one horizontal border run of exactly horiz cells, with an
// optional label woven in as ─ label ─────. label is drawn with labelStyle,
// the rule with the border color; an over-long label is truncated to keep the
// run exactly horiz wide.
func edgeBorder(th theme.Theme, label string, horiz int, color lipgloss.AdaptiveColor, labelStyle lipgloss.Style) string {
	bs := lipgloss.NewStyle().Foreground(color)
	rule := func(n int) string {
		if n <= 0 {
			return ""
		}
		return bs.Render(strings.Repeat(th.BorderStyle.Horizontal, n))
	}
	// Need "─" + " label " + at least one "─": 4 rule/space cells minimum.
	maxLabel := horiz - 4
	if label == "" || maxLabel < 1 {
		return rule(horiz)
	}
	label = truncate(th, label, maxLabel)
	seg := " " + label + " "
	trail := horiz - 1 - lipgloss.Width(seg)
	return rule(1) + labelStyle.Render(seg) + rule(trail)
}

// fitRows forces rows to exactly n lines: extra trailing lines are dropped,
// missing lines are added as blank rows of inner width.
func fitRows(th theme.Theme, rows []string, n, inner int) []string {
	if n <= 0 {
		return nil
	}
	if len(rows) > n {
		return rows[:n]
	}
	for len(rows) < n {
		rows = append(rows, padRight(th, "", inner))
	}
	return rows
}
