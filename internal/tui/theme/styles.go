package theme

import "github.com/charmbracelet/lipgloss"

// Styles holds the handful of lipgloss styles nearly every view needs,
// precomputed from a Theme's color roles. Building them once through S()
// keeps views free of repeated lipgloss.NewStyle().Foreground(...) noise and
// guarantees they all draw from the same tokens.
type Styles struct {
	Text      lipgloss.Style // primary foreground text
	Muted     lipgloss.Style // de-emphasized text
	Accent    lipgloss.Style // primary emphasis
	AccentAlt lipgloss.Style // secondary emphasis
	Title     lipgloss.Style // accent + bold, for headings and labels
	Success   lipgloss.Style // positive state
	Warning   lipgloss.Style // caution state
	Error     lipgloss.Style // failure state
}

// S returns the common styles for this theme. It allocates fresh styles on
// each call (lipgloss styles are cheap value types); call it once per render
// and reuse the result.
func (t Theme) S() Styles {
	return Styles{
		Text:      lipgloss.NewStyle().Foreground(t.Text),
		Muted:     lipgloss.NewStyle().Foreground(t.TextMuted),
		Accent:    lipgloss.NewStyle().Foreground(t.Accent),
		AccentAlt: lipgloss.NewStyle().Foreground(t.AccentAlt),
		Title:     lipgloss.NewStyle().Foreground(t.Accent).Bold(true),
		Success:   lipgloss.NewStyle().Foreground(t.Success),
		Warning:   lipgloss.NewStyle().Foreground(t.Warning),
		Error:     lipgloss.NewStyle().Foreground(t.Error),
	}
}
