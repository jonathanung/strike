package common

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestPadWideGlyphsEgyptianHieroglyphs(t *testing.T) {
	const h = "𓀀" // U+13000
	if ansi.StringWidth(h) != 1 {
		t.Fatalf("precondition: libraries still measure hieroglyph as 1, got %d", ansi.StringWidth(h))
	}
	got := PadWideGlyphs(h)
	if got != h+" " {
		t.Fatalf("PadWideGlyphs(%q) = %q, want hieroglyph + space", h, got)
	}
	if w := ansi.StringWidth(got); w != 2 {
		t.Fatalf("padded width = %d, want 2 so layout matches double-cell paint", w)
	}
}

func TestPadWideGlyphsIdempotentAndPreservesASCII(t *testing.T) {
	const h = "𓁹"
	once := PadWideGlyphs(h)
	twice := PadWideGlyphs(once)
	if once != twice {
		t.Fatalf("not idempotent: %q vs %q", once, twice)
	}
	if got := PadWideGlyphs("hello 中 😀"); got != "hello 中 😀" {
		t.Fatalf("ASCII/CJK/emoji changed: %q", got)
	}
	if got := PadWideGlyphs(""); got != "" {
		t.Fatalf("empty: %q", got)
	}
}

func TestPadWideGlyphsMeasuredWidthDoubles(t *testing.T) {
	const n = 40
	raw := strings.Repeat("𓂀", n)
	padded := PadWideGlyphs(raw)
	if got, want := ansi.StringWidth(padded), 2*n; got != want {
		t.Fatalf("padded width = %d, want %d", got, want)
	}
	// A width budget that would overflow unpadded now fits half as many glyphs.
	const width = 40
	wrapped := ""
	// Manual fit using measured width after pad (mirrors wrap/truncate).
	for _, r := range padded {
		next := wrapped + string(r)
		if ansi.StringWidth(next) > width {
			break
		}
		wrapped = next
	}
	if got := ansi.StringWidth(wrapped); got > width {
		t.Fatalf("fitted line width %d > %d", got, width)
	}
	if glyphs := strings.Count(wrapped, "𓂀"); glyphs != width/2 {
		t.Fatalf("fitted %d glyphs, want %d", glyphs, width/2)
	}
}

func TestPadWideGlyphsPreservesANSI(t *testing.T) {
	const h = "𓃀"
	styled := "\x1b[32m" + h + "\x1b[0m"
	got := PadWideGlyphs(styled)
	if !strings.HasPrefix(got, "\x1b[32m") || !strings.HasSuffix(got, "\x1b[0m") {
		t.Fatalf("ANSI framing lost: %q", got)
	}
	plain := ansi.Strip(got)
	if plain != h+" " {
		t.Fatalf("plain = %q, want hieroglyph + space", plain)
	}
}

func TestPadWideGlyphsSkipsFormatControlsAlone(t *testing.T) {
	// Egyptian Hieroglyph Format Controls are not in the pad set.
	const joiner = "\U00013430"
	if isOverflowingNeutral([]rune(joiner)[0]) {
		t.Fatal("format control should not be treated as overflowing neutral")
	}
	if got := PadWideGlyphs(joiner); got != joiner {
		t.Fatalf("format control padded: %q", got)
	}
}

func TestContainsOverflowingNeutralFastPath(t *testing.T) {
	if containsOverflowingNeutral("plain ASCII \x1b[31mred\x1b[0m") {
		t.Fatal("ASCII/ANSI should be fast false")
	}
	if !containsOverflowingNeutral("x𓀀y") {
		t.Fatal("hieroglyph should be detected")
	}
	// Invalid trailing byte should not panic.
	_ = containsOverflowingNeutral(string([]byte{0xff}))
}
