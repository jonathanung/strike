package ui

import "github.com/jonathanung/strike-cli/internal/tui/theme"

// Badge is a compact bracketed chip for a short status token, tone-colored.
// Use it for header pills like the current provider/model or the active
// agent.
//
//	ui.Badge(th, ui.ToneAccent, "anthropic/claude-sonnet-5") // [ anthropic/claude-sonnet-5 ]
//	ui.Badge(th, ui.ToneSuccess, "authed")                   // [ authed ]
//
// Badge does not take a width; it sizes to its text. Truncate the text before
// calling if it must fit a budget.
func Badge(th theme.Theme, tone Tone, text string) string {
	bracket := th.S().Muted
	label := toneStyle(th, tone).Bold(true)
	return bracket.Render("[") + " " + label.Render(text) + " " + bracket.Render("]")
}
