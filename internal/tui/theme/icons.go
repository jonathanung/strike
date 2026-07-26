package theme

// Icons is the TUI's glyph set. It lives alongside the color roles so glyphs,
// like colors, have exactly one source: views reference Icons fields instead
// of writing "❯" or "✓" inline, and a single edit re-glyphs the whole UI.
type Icons struct {
	Prompt          string // ❯ user prompt / input marker
	Assistant       string // ● assistant label bullet
	Tool            string // ⚙ tool-call label
	OK              string // ✓ success glyph
	Err             string // ✗ error glyph
	Info            string // ◦ informational glyph
	Agent           string // ◆ agent marker
	Bolt            string // ⚡ brand motif
	Dot             string // · inline separator
	Cursor          string // ▸ selection cursor
	InputCursor     string // > composer/text-input cursor
	FilterCursor    string // ▏ active filter cursor
	ToolGuide       string // │ tool transcript guide
	BadgeLeft       string // [ badge opening delimiter
	BadgeRight      string // ] badge closing delimiter
	DetailSeparator string // — list detail separator
	Ellipsis        string // … truncation marker
	LogoTopRule     string // ▁ logo top rule
	LogoBottomRule  string // ▔ logo bottom rule
	MeterFill       string // █ context-meter filled cell
	MeterEmpty      string // ░ context-meter empty cell
	TreeExpanded    string // ▾ expanded tree node marker
	TreeCollapsed   string // ▸ collapsed tree node marker
	// Sparkline is low→high bar glyphs for activity charts (one cell each).
	// Empty falls back to DefaultIcons; ui.Sparkline indexes into runes.
	Sparkline string
}

// DefaultIcons returns the stock glyph set. A zero Icons value is treated as
// "unset" by the ui package, which falls back to these, so components stay
// usable when handed a bare theme.
func DefaultIcons() Icons {
	return Icons{
		Prompt:       "❯",
		Assistant:    "●",
		Tool:         "⚙",
		OK:           "✓",
		Err:          "✗",
		Info:         "◦",
		Agent:        "◆",
		Bolt:         "⚡",
		Dot:          "·",
		Cursor:       "▸",
		InputCursor:  ">",
		FilterCursor: "▏",
		ToolGuide:    "│",
		BadgeLeft:    "[", BadgeRight: "]", DetailSeparator: "—", Ellipsis: "…",
		LogoTopRule: "▁", LogoBottomRule: "▔",
		MeterFill: "█", MeterEmpty: "░",
		TreeExpanded: "▾", TreeCollapsed: "▸",
		Sparkline: "▁▂▃▄▅▆▇█",
	}
}

func resolveIcons(i, d Icons) Icons {
	if i.Prompt == "" {
		i.Prompt = d.Prompt
	}
	if i.Assistant == "" {
		i.Assistant = d.Assistant
	}
	if i.Tool == "" {
		i.Tool = d.Tool
	}
	if i.OK == "" {
		i.OK = d.OK
	}
	if i.Err == "" {
		i.Err = d.Err
	}
	if i.Info == "" {
		i.Info = d.Info
	}
	if i.Agent == "" {
		i.Agent = d.Agent
	}
	if i.Bolt == "" {
		i.Bolt = d.Bolt
	}
	if i.Dot == "" {
		i.Dot = d.Dot
	}
	if i.Cursor == "" {
		i.Cursor = d.Cursor
	}
	if i.InputCursor == "" {
		i.InputCursor = d.InputCursor
	}
	if i.FilterCursor == "" {
		i.FilterCursor = d.FilterCursor
	}
	if i.ToolGuide == "" {
		i.ToolGuide = d.ToolGuide
	}
	if i.BadgeLeft == "" {
		i.BadgeLeft = d.BadgeLeft
	}
	if i.BadgeRight == "" {
		i.BadgeRight = d.BadgeRight
	}
	if i.DetailSeparator == "" {
		i.DetailSeparator = d.DetailSeparator
	}
	if i.Ellipsis == "" {
		i.Ellipsis = d.Ellipsis
	}
	if i.LogoTopRule == "" {
		i.LogoTopRule = d.LogoTopRule
	}
	if i.LogoBottomRule == "" {
		i.LogoBottomRule = d.LogoBottomRule
	}
	if i.MeterFill == "" {
		i.MeterFill = d.MeterFill
	}
	if i.MeterEmpty == "" {
		i.MeterEmpty = d.MeterEmpty
	}
	if i.TreeExpanded == "" {
		i.TreeExpanded = d.TreeExpanded
	}
	if i.TreeCollapsed == "" {
		i.TreeCollapsed = d.TreeCollapsed
	}
	if i.Sparkline == "" {
		i.Sparkline = d.Sparkline
	}
	return i
}
