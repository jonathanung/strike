package ui

import (
	"strconv"
	"strings"

	"github.com/jonathanung/strike-cli/internal/frontend/tui/theme"
)

// FormatTokens renders a non-negative token count compactly for status chrome
// and the context pane: exact below 1000, k above that, M from one million.
// Under 10k and 10M a single fractional digit is shown when it is non-zero
// (1500 → "1.5k", 1500000 → "1.5M"); otherwise the integer form is used
// (1000 → "1k", 215000 → "215k", 1000000 → "1M").
func FormatTokens(n int) string {
	if n < 0 {
		n = 0
	}
	if n < 1000 {
		return strconv.Itoa(n)
	}
	if n < 1_000_000 {
		if n < 10_000 {
			whole := n / 1000
			frac := (n % 1000) / 100
			if frac == 0 {
				return strconv.Itoa(whole) + "k"
			}
			return strconv.Itoa(whole) + "." + strconv.Itoa(frac) + "k"
		}
		return strconv.Itoa(n/1000) + "k"
	}
	if n < 10_000_000 {
		whole := n / 1_000_000
		frac := (n % 1_000_000) / 100_000
		if frac == 0 {
			return strconv.Itoa(whole) + "M"
		}
		return strconv.Itoa(whole) + "." + strconv.Itoa(frac) + "M"
	}
	return strconv.Itoa(n/1_000_000) + "M"
}

// Meter renders a fixed-width ratio bar. ratio is clamped to [0, 1]; a negative
// ratio means unknown and draws a fully hollow muted bar. Fill color tracks
// pressure: Success below 0.7, Warning from 0.7 through 0.9, Error above 0.9.
// Glyphs come from theme.Icons (MeterFill / MeterEmpty).
func Meter(th theme.Theme, width int, ratio float64) string {
	th = th.Resolve()
	if width <= 0 {
		return ""
	}
	ic := resolveIcons(th)
	fillGlyph := ic.MeterFill
	emptyGlyph := ic.MeterEmpty
	empty := th.S().Muted.Render(strings.Repeat(emptyGlyph, width))
	if ratio < 0 {
		return empty
	}
	if ratio > 1 {
		ratio = 1
	}
	filled := int(ratio*float64(width) + 0.5)
	if filled > width {
		filled = width
	}
	if filled < 0 {
		filled = 0
	}
	fillStyle := th.S().Success
	switch {
	case ratio > 0.9:
		fillStyle = th.S().Error
	case ratio >= 0.7:
		fillStyle = th.S().Warning
	}
	bar := fillStyle.Render(strings.Repeat(fillGlyph, filled))
	if filled < width {
		bar += th.S().Muted.Render(strings.Repeat(emptyGlyph, width-filled))
	}
	return bar
}
