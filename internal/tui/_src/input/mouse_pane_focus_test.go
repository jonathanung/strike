package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestMousePaneFocusHitTesting(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	gutter := m.paneGutter()
	geo := computePaneGeometry(m.width, gutter, m.focus)
	l := computeLayout(geo.leftWidth, m.height, m.composer.Height(), m.completionPopupHeightFor(geo.leftWidth), m.showDangerBanner(), m.noticeRowsFor(geo.leftWidth))
	bodyY := l.header

	tests := []struct {
		name    string
		x, y    int
		want    paneFocus
		wantHit bool
	}{
		{"left border", 0, bodyY, focusLeft, true},
		{"left body", geo.leftWidth - 1, bodyY, focusLeft, true},
		{"gutter", geo.leftWidth, bodyY, focusLeft, false},
		{"right border", geo.leftWidth + geo.gutter, bodyY, focusRight, true},
		{"right body", m.width - 1, bodyY, focusRight, true},
		{"header", 0, 0, focusLeft, false},
		{"footer", 0, m.height - 1, focusLeft, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := m.paneFocusAtMouse(tt.x, tt.y)
			if got != tt.want || ok != tt.wantHit {
				t.Fatalf("paneFocusAtMouse(%d, %d) = (%v, %v), want (%v, %v)", tt.x, tt.y, got, ok, tt.want, tt.wantHit)
			}
		})
	}
}

func TestMouseClickChangesVisiblePaneFocus(t *testing.T) {
	tests := []struct {
		name        string
		width       int
		orientation splitOrientation
		initial     paneFocus
		click       func(Model) (int, int)
		want        paneFocus
	}{
		{
			name:    "horizontal secondary",
			width:   120,
			initial: focusLeft,
			click: func(m Model) (int, int) {
				geo := computePaneGeometry(m.width, m.paneGutter(), m.focus)
				return geo.leftWidth + geo.gutter, 1
			},
			want: focusRight,
		},
		{
			name:    "horizontal primary",
			width:   120,
			initial: focusRight,
			click: func(m Model) (int, int) {
				return 0, 1
			},
			want: focusLeft,
		},
		{
			name:        "vertical secondary",
			width:       120,
			orientation: orientVertical,
			initial:     focusLeft,
			click: func(m Model) (int, int) {
				l := computeLayout(m.width, m.height, m.composer.Height(), m.completionPopupHeightFor(m.width), m.showDangerBanner(), m.noticeRowsFor(m.width))
				bodyHeight := l.transcript + l.notice + l.popup + l.composer
				geo := computeVerticalPaneGeometry(m.width, bodyHeight, m.paneGutter(), m.focus)
				return 0, l.header + geo.leftHeight + geo.gutter
			},
			want: focusRight,
		},
		{
			name:    "single pane stays focused",
			width:   80,
			initial: focusRight,
			click: func(m Model) (int, int) {
				return 0, 1
			},
			want: focusRight,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := newAppTestModel(nil, nil)
			m.splitOrientation = tt.orientation
			m.focus = tt.initial
			m = updateApp(t, m, tea.WindowSizeMsg{Width: tt.width, Height: 40})
			x, y := tt.click(m)
			m = updateApp(t, m, tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
			if m.focus != tt.want {
				t.Fatalf("focus after click = %v, want %v", m.focus, tt.want)
			}
		})
	}
}

func TestMousePaneFocusDoesNotBypassModalOrGutter(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	geo := computePaneGeometry(m.width, m.paneGutter(), m.focus)
	m = updateApp(t, m, tea.MouseClickMsg{X: geo.leftWidth, Y: 1, Button: tea.MouseLeft})
	if m.focus != focusLeft {
		t.Fatalf("gutter click changed focus to %v", m.focus)
	}

	m.modal = &appProbeModal{}
	m.reflow()
	m = updateApp(t, m, tea.MouseClickMsg{X: geo.leftWidth + geo.gutter, Y: 1, Button: tea.MouseLeft})
	if m.focus != focusLeft {
		t.Fatalf("modal click changed focus to %v", m.focus)
	}
}
