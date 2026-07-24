package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

// Card is one bento tile: a Panel with a preferred width. Bodies are wrapped
// to the tile's inner width before rendering.
type Card struct {
	Title  string // top-border title
	Footer string // bottom-border hint
	Body   string // tile content (wrapped to fit)
	Width  int    // preferred outer width; capped to the row width
	Tone   Tone   // border accent; ToneDefault renders a calm dim border
}

// Bento packs cards left-to-right into rows that fit width, separated by gap
// columns, wrapping to a new row when the next card would overflow. Cards in a
// row share a height so their borders align, and a card wider than width is
// capped so the layout collapses to a single column on narrow screens. It is
// the welcome dashboard layout.
//
//	cards := []ui.Card{
//	    {Title: "strike", Body: ui.Logo(th), Width: 30},
//	    {Title: "get started", Body: providerLines, Width: 34},
//	    {Title: "keys", Body: keyLines, Width: 28},
//	}
//	dashboard := ui.Bento(th, viewportWidth, 2, cards)
func Bento(th theme.Theme, width, gap int, cards []Card) string {
	if width < 1 || len(cards) == 0 {
		return ""
	}
	if gap < 0 {
		gap = 0
	}

	var rows [][]Card
	var cur []Card
	curW := 0
	for _, c := range cards {
		cw := clamp(c.Width, 1, width)
		c.Width = cw
		need := cw
		if len(cur) > 0 {
			need += gap
		}
		if len(cur) > 0 && curW+need > width {
			rows = append(rows, cur)
			cur, curW = nil, 0
			need = cw
		}
		cur = append(cur, c)
		curW += need
	}
	if len(cur) > 0 {
		rows = append(rows, cur)
	}

	blocks := make([]string, len(rows))
	for i, row := range rows {
		blocks[i] = bentoRow(th, row, gap)
	}
	return strings.Join(blocks, "\n")
}

// bentoRow renders one row of cards at a shared height, joined with gap spaces.
func bentoRow(th theme.Theme, cards []Card, gap int) string {
	rendered := make([]string, len(cards))
	height := 0
	for i, c := range cards {
		rendered[i] = cardPanel(th, c, 0)
		if h := lineCount(rendered[i]); h > height {
			height = h
		}
	}
	for i, c := range cards {
		rendered[i] = cardPanel(th, c, height)
	}
	return joinHoriz(rendered, gap, height)
}

func cardPanel(th theme.Theme, c Card, height int) string {
	return Panel(th, PanelOpts{
		Title:  c.Title,
		Footer: c.Footer,
		Width:  c.Width,
		Height: height,
		Dim:    c.Tone == ToneDefault,
		Tone:   c.Tone,
	}, wrapText(c.Body, InnerWidth(c.Width)))
}

// joinHoriz concatenates equal-height blocks side by side, separated by gap
// spaces. Every block is expected to be rectangular (Panel guarantees this);
// short blocks are padded to height with blank lines matching their width.
func joinHoriz(blocks []string, gap, height int) string {
	if len(blocks) == 0 {
		return ""
	}
	if len(blocks) == 1 {
		return blocks[0]
	}
	cols := make([][]string, len(blocks))
	for i, blk := range blocks {
		lines := strings.Split(blk, "\n")
		blank := strings.Repeat(" ", lipgloss.Width(lines[0]))
		for len(lines) < height {
			lines = append(lines, blank)
		}
		cols[i] = lines
	}
	sep := strings.Repeat(" ", gap)
	out := make([]string, height)
	for row := 0; row < height; row++ {
		parts := make([]string, len(cols))
		for i := range cols {
			parts[i] = cols[i][row]
		}
		out[row] = strings.Join(parts, sep)
	}
	return strings.Join(out, "\n")
}
