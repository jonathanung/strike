package theme

import (
	"unicode"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
)

// ChromeMode selects how panels paint primary chrome.
type ChromeMode uint8

const (
	// ChromeUnset resolves to ChromeSoft (Default() chrome until #1234).
	ChromeUnset ChromeMode = iota
	// ChromeSolid paints panels as filled surfaces with title/footer bars
	// (no box-drawing frame).
	ChromeSolid
	// ChromeBordered paints classic box-drawing panel borders (outline only,
	// minimal surface wash). Token-file north star (schemas/ui-tokens.json).
	ChromeBordered
	// ChromeSoft paints surface-filled bodies with a rounded box outline
	// (╭╮╰╯). Still Default() until #1234 applies bordered chrome.
	ChromeSoft
)

// BorderWeight selects a stock border glyph preset (ChromeBordered / ChromeSoft).
type BorderWeight uint8

const (
	BorderWeightUnset BorderWeight = iota
	BorderWeightLight
	BorderWeightHeavy
)

// BorderStyle controls the six glyphs used to render bordered/soft panels.
type BorderStyle struct {
	Weight                                     BorderWeight
	TopLeft, TopRight, BottomLeft, BottomRight string
	Horizontal, Vertical                       string
}

func resolveChrome(c ChromeMode) ChromeMode {
	switch c {
	case ChromeSolid:
		return ChromeSolid
	case ChromeBordered:
		return ChromeBordered
	case ChromeSoft:
		return ChromeSoft
	default:
		return ChromeSoft
	}
}

func lightBorderStyle() BorderStyle {
	return BorderStyle{Weight: BorderWeightLight, TopLeft: "╭", TopRight: "╮", BottomLeft: "╰", BottomRight: "╯", Horizontal: "─", Vertical: "│"}
}

func heavyBorderStyle() BorderStyle {
	return BorderStyle{Weight: BorderWeightHeavy, TopLeft: "┏", TopRight: "┓", BottomLeft: "┗", BottomRight: "┛", Horizontal: "━", Vertical: "┃"}
}

func resolveBorderStyle(b BorderStyle) BorderStyle {
	if b.Weight != BorderWeightHeavy {
		b.Weight = BorderWeightLight
	}
	preset := lightBorderStyle()
	if b.Weight == BorderWeightHeavy {
		preset = heavyBorderStyle()
	}
	if !oneCell(b.TopLeft) {
		b.TopLeft = preset.TopLeft
	}
	if !oneCell(b.TopRight) {
		b.TopRight = preset.TopRight
	}
	if !oneCell(b.BottomLeft) {
		b.BottomLeft = preset.BottomLeft
	}
	if !oneCell(b.BottomRight) {
		b.BottomRight = preset.BottomRight
	}
	if !oneCell(b.Horizontal) {
		b.Horizontal = preset.Horizontal
	}
	if !oneCell(b.Vertical) {
		b.Vertical = preset.Vertical
	}
	return b
}

func oneCell(s string) bool {
	if lipgloss.Width(s) != 1 || lipgloss.Height(s) != 1 {
		return false
	}
	for i := 0; i < len(s); {
		if s[i] < 0x20 || (s[i] >= 0x7f && s[i] <= 0x9f) {
			return false
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 || unicode.IsControl(r) {
			return false
		}
		i += size
	}
	return true
}

// Spacing provides named layout gaps. Its initialized mask preserves an
// explicit zero supplied through NewSpacing or the With methods.
type Spacing struct {
	None, XS, SM, MD, LG, Label int
	set                         uint8
}

const (
	spacingXS uint8 = 1 << iota
	spacingSM
	spacingMD
	spacingLG
	spacingLabel
)

// NewSpacing creates spacing tokens, including explicit zero values.
func NewSpacing(xs, sm, md, lg int) Spacing {
	return Spacing{XS: xs, SM: sm, MD: md, LG: lg, set: spacingXS | spacingSM | spacingMD | spacingLG}
}
func (s Spacing) WithXS(v int) Spacing    { s.XS, s.set = v, s.set|spacingXS; return s }
func (s Spacing) WithSM(v int) Spacing    { s.SM, s.set = v, s.set|spacingSM; return s }
func (s Spacing) WithMD(v int) Spacing    { s.MD, s.set = v, s.set|spacingMD; return s }
func (s Spacing) WithLG(v int) Spacing    { s.LG, s.set = v, s.set|spacingLG; return s }
func (s Spacing) WithLabel(v int) Spacing { s.Label, s.set = v, s.set|spacingLabel; return s }

func resolveSpacing(s, d Spacing) Spacing {
	if s.XS != 0 {
		s.set |= spacingXS
	}
	if s.SM != 0 {
		s.set |= spacingSM
	}
	if s.MD != 0 {
		s.set |= spacingMD
	}
	if s.LG != 0 {
		s.set |= spacingLG
	}
	if s.Label != 0 {
		s.set |= spacingLabel
	}
	if s.set&spacingXS == 0 {
		s.XS = d.XS
	}
	if s.set&spacingSM == 0 {
		s.SM = d.SM
	}
	if s.set&spacingMD == 0 {
		s.MD = d.MD
	}
	if s.set&spacingLG == 0 {
		s.LG = d.LG
	}
	if s.set&spacingLabel == 0 {
		s.Label = d.Label
	}
	if s.None < 0 {
		s.None = 0
	}
	s.XS = max(0, s.XS)
	s.SM = max(0, s.SM)
	s.MD = max(0, s.MD)
	s.LG = max(0, s.LG)
	s.Label = max(0, s.Label)
	return s
}
