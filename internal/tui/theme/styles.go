package theme

import "charm.land/lipgloss/v2"

// Styles holds the handful of lipgloss styles nearly every view needs,
// precomputed from a Theme's color roles. Building them once through S()
// keeps views free of repeated lipgloss.NewStyle().Foreground(...) noise and
// guarantees they all draw from the same tokens.
type Styles struct {
	Text                                                       lipgloss.Style // primary foreground text
	Muted                                                      lipgloss.Style // de-emphasized text
	Accent                                                     lipgloss.Style // primary emphasis
	AccentAlt                                                  lipgloss.Style // secondary emphasis
	Title                                                      lipgloss.Style // accent + bold, for headings and labels
	Success                                                    lipgloss.Style // positive state
	Warning                                                    lipgloss.Style // caution state
	Error                                                      lipgloss.Style // failure state
	Danger                                                     lipgloss.Style // destructive state
	TextStrong, MutedStrong, AccentStrong, AccentAltStrong     lipgloss.Style
	SuccessStrong, WarningStrong, ErrorStrong, DangerStrong    lipgloss.Style
	Selected, SelectedUnderline                                lipgloss.Style
	TextSelection                                              lipgloss.Style // mouse drag highlight
	UserLabel, AssistantLabel, ToolLabel                       lipgloss.Style
	Input, InputPrompt, InputPlaceholder, InputCursor, Spinner lipgloss.Style
	Border, BorderFocus, BorderMuted                           lipgloss.Style
	Surface, SurfaceFocus, SurfaceMuted                        lipgloss.Style
	DiffAdded, DiffRemoved                                     lipgloss.Style
	// Diff*Strong bold the changed intra-line spans (word-diff highlights).
	DiffAddedStrong, DiffRemovedStrong lipgloss.Style
	// Diff*Line paint full add/remove rows: role FG on SurfaceMuted wash.
	DiffAddedLine, DiffRemovedLine lipgloss.Style
}

// S returns the common styles for this theme. It allocates fresh styles on
// each call (lipgloss styles are cheap value types); call it once per render
// and reuse the result. AdaptiveColor → lipgloss color conversion happens
// here via AdaptiveColor.RGBA (compat).
func (t Theme) S() Styles {
	t = t.Resolve()
	base := lipgloss.NewStyle()
	return Styles{
		Text: base.Foreground(t.Text), Muted: base.Foreground(t.TextMuted), Accent: base.Foreground(t.Accent), AccentAlt: base.Foreground(t.AccentAlt), Title: base.Foreground(t.Accent).Bold(true), Success: base.Foreground(t.Success), Warning: base.Foreground(t.Warning), Error: base.Foreground(t.Error), Danger: base.Foreground(t.Danger),
		TextStrong: base.Foreground(t.Text).Bold(true), MutedStrong: base.Foreground(t.TextMuted).Bold(true), AccentStrong: base.Foreground(t.Accent).Bold(true), AccentAltStrong: base.Foreground(t.AccentAlt).Bold(true), SuccessStrong: base.Foreground(t.Success).Bold(true), WarningStrong: base.Foreground(t.Warning).Bold(true), ErrorStrong: base.Foreground(t.Error).Bold(true), DangerStrong: base.Foreground(t.Danger).Bold(true),
		Selected: base.Foreground(t.Highlight).Bold(true), SelectedUnderline: base.Foreground(t.Highlight).Bold(true).Underline(true),
		TextSelection: base.Foreground(t.Background).Background(t.Highlight).Reverse(true),
		UserLabel:     base.Foreground(t.UserLabel).Bold(true), AssistantLabel: base.Foreground(t.Accent).Bold(true), ToolLabel: base.Foreground(t.ToolLabel).Bold(true),
		Input: base.Foreground(t.Text), InputPrompt: base.Foreground(t.Accent), InputPlaceholder: base.Foreground(t.TextMuted), InputCursor: base.Foreground(t.Accent), Spinner: base.Foreground(t.AccentAlt),
		Border: base.Foreground(t.Border), BorderFocus: base.Foreground(t.BorderFocus), BorderMuted: base.Foreground(t.BorderMuted),
		Surface: base.Foreground(t.Text).Background(t.Surface), SurfaceFocus: base.Foreground(t.Text).Background(t.SurfaceFocus), SurfaceMuted: base.Foreground(t.TextMuted).Background(t.SurfaceMuted),
		DiffAdded: base.Foreground(t.DiffAdded), DiffRemoved: base.Foreground(t.DiffRemoved),
		// Strong spans share the line wash so bold islands do not punch holes
		// in the SurfaceMuted band on add/remove rows.
		DiffAddedStrong: base.Foreground(t.DiffAdded).Bold(true).Background(t.SurfaceMuted), DiffRemovedStrong: base.Foreground(t.DiffRemoved).Bold(true).Background(t.SurfaceMuted),
		// Line washes share SurfaceMuted so add/remove rows read as tinted bands
		// without new color roles.
		DiffAddedLine: base.Foreground(t.DiffAdded).Background(t.SurfaceMuted), DiffRemovedLine: base.Foreground(t.DiffRemoved).Background(t.SurfaceMuted),
	}
}
