package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2/compat"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

// TestThemeChromeModesFrames asserts soft/solid/bordered chrome is observable
// on a full app frame (Family soft default has rounded box outline).
func TestThemeChromeModesFrames(t *testing.T) {
	savedDark := compat.HasDarkBackground
	t.Cleanup(func() { compat.HasDarkBackground = savedDark })
	compat.HasDarkBackground = true

	for _, tc := range []struct {
		name    string
		chrome  theme.ChromeMode
		wantBox bool
	}{
		{name: "soft", chrome: theme.ChromeSoft, wantBox: true},
		{name: "solid", chrome: theme.ChromeSolid, wantBox: false},
		{name: "bordered", chrome: theme.ChromeBordered, wantBox: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			th := theme.Default()
			th.Chrome = tc.chrome
			m, _ := newAppTestModelWithOptions(Options{Theme: &th})
			m = updateApp(t, m, tea.WindowSizeMsg{Width: 93, Height: 40})
			plain := ansi.Strip(viewString(m))
			hasBox := strings.ContainsAny(plain, "╭╮╰╯┌┐└┘│─")
			if hasBox != tc.wantBox {
				t.Fatalf("box-drawing present=%v, want %v\n%s", hasBox, tc.wantBox, plain)
			}
			if !strings.Contains(plain, "context") {
				t.Fatalf("split frame missing context pane:\n%s", plain)
			}
		})
	}
}
