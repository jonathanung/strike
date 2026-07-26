package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

// PanelOpts configures Panel.
type PanelOpts struct {
	// Title is drawn in the top chrome row. Empty draws a plain top bar
	// (solid) or plain top border (bordered).
	Title string
	// Footer is a short muted hint in the bottom chrome row. Long hints are
	// better placed inside the body (see Dialog); chrome truncates.
	Footer string
	// Width is the mandatory outer width. Output is exactly this many cells
	// wide when Width >= 1; narrower panels drop chrome and degrade to plain,
	// truncated text.
	Width int
	// Height, when > 0, fixes the outer height: content is truncated or
	// blank-padded to fit. Heights below two render structurally without panel
	// chrome. Zero fits the content.
	Height int
	// Borderless omits panel chrome and padding. The body is structurally fit
	// to Width and Height; Title, Footer, Focused, Dim, and Tone are ignored.
	Borderless bool
	// Focused selects the focus surface/border token. When Tone is default,
	// Focused wins over Dim (full precedence: Tone > Focused > Dim > default).
	Focused bool
	// Dim selects the muted surface/border for calm, inactive chrome (e.g.
	// bento tiles). Ignored when Focused or Tone is set.
	Dim bool
	// Tone overrides chrome emphasis entirely (e.g. ToneWarning for a
	// permission dialog). Non-default Tone wins over Focused and Dim.
	Tone Tone
}

// Panel is the framed tile primitive: a solid surface (default) or optional
// box-drawing border, with its title in the top chrome and an optional hint
// in the bottom chrome. It is the building block for Dialog and Card and for
// the app's transcript and composer regions.
//
//	body := "streaming transcript…"
//	out := ui.Panel(th, ui.PanelOpts{Title: "session", Width: 60, Focused: true}, body)
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
	chrome, padX, inner := panelMetrics(th, width)

	rows := strings.Split(body, "\n")
	for i, r := range rows {
		rows[i] = padRight(th, r, inner)
	}
	if opts.Height > 0 {
		contentRows := opts.Height
		if chrome {
			contentRows = max(0, opts.Height-2)
		}
		rows = fitRows(th, rows, contentRows, inner)
	}

	if !chrome {
		// Too narrow for chrome: plain, width-clamped text.
		return strings.Join(rows, "\n")
	}

	if th.Chrome == theme.ChromeBordered {
		return renderBorderedPanel(th, opts, width, padX, rows)
	}
	return renderSolidPanel(th, opts, width, padX, rows)
}

func renderSolidPanel(th theme.Theme, opts PanelOpts, width, padX int, rows []string) string {
	bg := panelSurfaceColor(th, opts)
	titleStyle := panelTitleStyle(th, opts)
	var b strings.Builder
	b.WriteString(solidEdge(th, opts.Title, width, padX, bg, titleStyle))
	pad := strings.Repeat(" ", padX)
	for _, row := range rows {
		b.WriteByte('\n')
		b.WriteString(paintSurface(pad+row+pad, width, bg))
	}
	b.WriteByte('\n')
	b.WriteString(solidEdge(th, opts.Footer, width, padX, bg, th.S().Muted))
	return b.String()
}

func renderBorderedPanel(th theme.Theme, opts PanelOpts, width, padX int, rows []string) string {
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
// PanelInnerWidth. It returns width itself when too narrow for chrome, and
// 0 when width <= 0.
func InnerWidth(width int) int {
	return PanelInnerWidth(theme.Default(), width)
}

// PanelInnerWidth reports the content width using th's resolved panel spacing
// and chrome mode. Themed callers must use this instead of InnerWidth.
func PanelInnerWidth(th theme.Theme, width int) int {
	_, _, inner := panelMetrics(th.Resolve(), width)
	return inner
}

// PanelInnerHeight reports the body height available inside a Panel with the
// supplied outer dimensions. It clamps nonpositive dimensions to zero and
// subtracts chrome rows only when chrome fits at width and height.
func PanelInnerHeight(width, height int) int {
	if width <= 0 || height <= 0 {
		return 0
	}
	if width < 3 || height < 2 {
		return height
	}
	return max(0, height-2)
}

// panelMetrics reports, for an outer width: whether chrome fits, the
// horizontal padding inside it, and the resulting content width.
func panelMetrics(th theme.Theme, width int) (chrome bool, padX, inner int) {
	switch {
	case width < 1:
		return false, 0, 0
	case width < 3:
		return false, 0, width
	}
	chrome = true
	padX = clamp(th.Spacing.XS, 0, (width-1)/2)
	if th.Chrome == theme.ChromeBordered {
		if width < 6 {
			return true, 0, width - 2
		}
		padX = clamp(th.Spacing.XS, 0, (width-3)/2)
		return true, padX, width - 2 - 2*padX
	}
	// Solid: no vertical frame columns — padding only.
	return true, padX, width - 2*padX
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

func panelSurfaceColor(th theme.Theme, opts PanelOpts) lipgloss.TerminalColor {
	th = th.Resolve()
	switch {
	case opts.Tone != ToneDefault:
		// Tone panels still use the focus surface; title/edge carry the tone.
		return th.SurfaceFocus
	case opts.Focused:
		return th.SurfaceFocus
	case opts.Dim:
		return th.SurfaceMuted
	default:
		return th.Surface
	}
}

func panelTitleStyle(th theme.Theme, opts PanelOpts) lipgloss.Style {
	th = th.Resolve()
	s := th.S()
	switch {
	case opts.Tone != ToneDefault:
		return toneStrongStyle(th, opts.Tone)
	case opts.Focused:
		return s.Title
	case opts.Dim:
		return s.MutedStrong
	default:
		return s.Title
	}
}

// solidEdge builds one full-width surface bar with an optional label.
func solidEdge(th theme.Theme, label string, width, padX int, bg lipgloss.TerminalColor, labelStyle lipgloss.Style) string {
	if label == "" {
		return paintSurface(strings.Repeat(" ", width), width, bg)
	}
	inner := max(0, width-2*padX)
	label = truncate(th, label, inner)
	seg := strings.Repeat(" ", padX) + labelStyle.Render(label)
	// Trailing pad keeps the bar rectangular under the surface background.
	if gap := width - lipgloss.Width(seg); gap > 0 {
		seg += strings.Repeat(" ", gap)
	}
	return paintSurface(seg, width, bg)
}

// paintSurface applies a solid background across exactly width cells. Nested
// styles that clear the background (SGR 0 / 49) are patched so the surface
// fill stays continuous across the row.
func paintSurface(content string, width int, bg lipgloss.TerminalColor) string {
	if width < 1 {
		return ""
	}
	rendered := lipgloss.NewStyle().Background(bg).Width(width).MaxWidth(width).Render(content)
	if prefix := theme.BackgroundPrefix(bg); prefix != "" {
		return restoreBackground(rendered, prefix)
	}
	return rendered
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
