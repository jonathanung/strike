package tui

// paneGutter returns the left|right body split gutter width. Kept at Spacing.XS
// so the canonical 93-col split threshold (60+gutter+32) stays intact; Family
// breathing room comes from soft rounded card chrome, not a wider pane gutter.
func (m Model) paneGutter() int {
	return m.th.Resolve().Spacing.XS
}
