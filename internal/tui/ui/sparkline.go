package ui

import (
	"strings"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

// Sparkline renders a fixed-width activity chart from non-negative samples.
// Unknown/empty data draws a muted hollow row (MeterEmpty), never fabricated
// zeros from missing measurements. Values are scaled to the series max so the
// tallest sample uses the top Sparkline glyph; a single sample fills the width
// at its relative height against itself (full bar).
//
// width ≤ 0 returns "". Glyphs come from theme.Icons.Sparkline (low→high) with
// MeterEmpty as the floor cell when the series max is zero or samples empty.
func Sparkline(th theme.Theme, width int, samples []float64) string {
	th = th.Resolve()
	if width <= 0 {
		return ""
	}
	ic := resolveIcons(th)
	levels := []rune(ic.Sparkline)
	if len(levels) == 0 {
		levels = []rune(theme.DefaultIcons().Sparkline)
	}
	st := th.S()
	emptyCell := ic.MeterEmpty
	if emptyCell == "" {
		emptyCell = theme.DefaultIcons().MeterEmpty
	}

	if len(samples) == 0 {
		return st.Muted.Render(strings.Repeat(emptyCell, width))
	}

	// Resample to width: take the max in each bucket so bursts stay visible.
	buckets := make([]float64, width)
	filled := make([]bool, width)
	for i, v := range samples {
		if v < 0 {
			continue
		}
		idx := i * width / len(samples)
		if idx >= width {
			idx = width - 1
		}
		if !filled[idx] || v > buckets[idx] {
			buckets[idx] = v
			filled[idx] = true
		}
	}

	maxV := 0.0
	any := false
	for i, ok := range filled {
		if !ok {
			continue
		}
		any = true
		if buckets[i] > maxV {
			maxV = buckets[i]
		}
	}
	if !any {
		return st.Muted.Render(strings.Repeat(emptyCell, width))
	}

	var b strings.Builder
	for i := 0; i < width; i++ {
		if !filled[i] {
			b.WriteString(st.Muted.Render(emptyCell))
			continue
		}
		if maxV <= 0 {
			// Measured zeros: show the lowest spark glyph, not an unknown hollow.
			b.WriteString(st.AccentAlt.Render(string(levels[0])))
			continue
		}
		// Map (0, max] onto levels; exact zero uses the floor glyph.
		level := 0
		if buckets[i] > 0 {
			level = int(buckets[i] / maxV * float64(len(levels)-1))
			if level < 0 {
				level = 0
			}
			if level >= len(levels) {
				level = len(levels) - 1
			}
			// Non-zero samples always rise at least one step when multiple levels.
			if level == 0 && len(levels) > 1 && buckets[i] > 0 {
				level = 1
			}
		}
		b.WriteString(st.AccentAlt.Render(string(levels[level])))
	}
	return b.String()
}
