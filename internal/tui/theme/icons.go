package theme

// Icons is the TUI's glyph set. It lives alongside the color roles so glyphs,
// like colors, have exactly one source: views reference Icons fields instead
// of writing "❯" or "✓" inline, and a single edit re-glyphs the whole UI.
type Icons struct {
	Prompt    string // ❯ user prompt / input marker
	Assistant string // ● assistant label bullet
	Tool      string // ⚙ tool-call label
	OK        string // ✓ success glyph
	Err       string // ✗ error glyph
	Info      string // ◦ informational glyph
	Agent     string // ◆ agent marker
	Bolt      string // ⚡ brand motif
	Dot       string // · inline separator
	Cursor    string // ▸ selection cursor
}

// DefaultIcons returns the stock glyph set. A zero Icons value is treated as
// "unset" by the ui package, which falls back to these, so components stay
// usable when handed a bare theme.
func DefaultIcons() Icons {
	return Icons{
		Prompt:    "❯",
		Assistant: "●",
		Tool:      "⚙",
		OK:        "✓",
		Err:       "✗",
		Info:      "◦",
		Agent:     "◆",
		Bolt:      "⚡",
		Dot:       "·",
		Cursor:    "▸",
	}
}
