package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestComputeLayoutRegionsSumToHeightAndPickCompactMode(t *testing.T) {
	tests := []struct {
		name         string
		width        int
		height       int
		composerRows int
		popup        int
		danger       bool
		wantCompact  bool
	}{
		{name: "80x24 bordered", width: 80, height: 24, composerRows: 2, wantCompact: false},
		{name: "120x40 bordered with danger", width: 120, height: 40, composerRows: 3, danger: true, wantCompact: false},
		{name: "50x15 compact", width: 50, height: 15, composerRows: 2, wantCompact: true},
		{name: "narrow width forces compact", width: 40, height: 30, composerRows: 2, wantCompact: true},
		{name: "with completion popup", width: 80, height: 24, composerRows: 2, popup: 5, wantCompact: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := computeLayout(tt.width, tt.height, tt.composerRows, tt.popup, tt.danger)
			if l.compact != tt.wantCompact {
				t.Errorf("compact = %v, want %v", l.compact, tt.wantCompact)
			}
			sum := l.header + l.transcript + l.notice + l.popup + l.composer + l.hints + l.danger
			if sum != tt.height {
				t.Errorf("region sum = %d, want screen height %d (%+v)", sum, tt.height, l)
			}
			if l.transcript < 0 || l.transcriptInnerHeight() < 0 {
				t.Errorf("negative transcript height: outer=%d inner=%d", l.transcript, l.transcriptInnerHeight())
			}
			if l.transcriptInnerWidth(tt.width) < 1 {
				t.Errorf("transcript inner width = %d, want >= 1", l.transcriptInnerWidth(tt.width))
			}
			if tt.danger && l.danger != 1 {
				t.Errorf("danger banner height = %d, want 1", l.danger)
			}
		})
	}
}

func TestViewFillsExactlyScreenHeightAtCommonSizes(t *testing.T) {
	for _, size := range []tea.WindowSizeMsg{
		{Width: 80, Height: 24},
		{Width: 120, Height: 40},
		{Width: 50, Height: 15},
	} {
		name := itoa(size.Width) + "x" + itoa(size.Height)
		t.Run(name, func(t *testing.T) {
			m, _ := newAppTestModel([]string{"build"}, nil)
			m = updateApp(t, m, size)
			view := m.View()
			lines := strings.Split(view, "\n")
			if len(lines) != size.Height {
				t.Fatalf("view has %d lines, want exactly screen height %d", len(lines), size.Height)
			}
			for i, line := range lines {
				if w := ansi.StringWidth(line); w > size.Width {
					t.Errorf("line %d width %d exceeds screen width %d: %q", i, w, size.Width, ansi.Strip(line))
				}
			}
		})
	}
}
