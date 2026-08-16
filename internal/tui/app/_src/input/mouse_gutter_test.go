package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestMousePaneFocusUsesPaneGutter locks pane hit-testing against paneGutter (XS).
func TestMousePaneFocusUsesPaneGutter(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	gutter := m.paneGutter()
	if gutter != m.th.Resolve().Spacing.XS {
		t.Fatalf("paneGutter = %d, want XS %d", gutter, m.th.Resolve().Spacing.XS)
	}
	geo := computePaneGeometry(m.width, gutter, m.focus)
	l := computeLayout(geo.leftWidth, m.height, m.composer.Height(), m.completionPopupHeightFor(geo.leftWidth), m.showDangerBanner(), m.noticeRowsFor(geo.leftWidth))
	bodyY := l.header

	// Gutter cells must not change focus.
	got, ok := m.paneFocusAtMouse(geo.leftWidth, bodyY)
	if ok {
		t.Fatalf("gutter hit returned focus %v", got)
	}
	// Right border after gutter focuses right.
	got, ok = m.paneFocusAtMouse(geo.leftWidth+geo.gutter, bodyY)
	if !ok || got != focusRight {
		t.Fatalf("right border = (%v, %v), want (right, true)", got, ok)
	}
}
