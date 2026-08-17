package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/jonathanung/strike-cli/internal/frontend/tui/theme"
)

// TestFocusedPaneHasSoftChromeNotBodyWash locks chrome:soft focus: SurfaceFocus
// title edge, BorderFocus outline (fg), body Surface, dim SurfaceMuted.
func TestFocusedPaneHasSoftChromeNotBodyWash(t *testing.T) {
	setTUITrueColor(t)
	th := theme.Default()
	th.Chrome = theme.ChromeSoft
	th.Surface = fixedColor("#112233")
	th.SurfaceFocus = fixedColor("#445566")
	th.BorderFocus = fixedColor("#778899")
	th.SurfaceMuted = fixedColor("#aabbcc")
	m, _ := newAppTestModelWithOptions(Options{Theme: &th})
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	view := viewString(m)
	if !strings.Contains(view, rgbBGSGR("#445566")) {
		t.Fatal("focused title edge missing SurfaceFocus")
	}
	// Soft outline: BorderFocus as foreground on rounded verticals.
	if !strings.Contains(view, rgbSGR("#778899")) {
		t.Fatal("focused outline missing BorderFocus fg")
	}
	if strings.Contains(view, rgbBGSGR("#778899")) {
		t.Fatal("focused chrome still uses solid BorderFocus fill")
	}
	if !strings.Contains(view, rgbBGSGR("#112233")) {
		t.Fatal("focused body missing normal Surface")
	}
	if !strings.Contains(view, rgbBGSGR("#aabbcc")) {
		t.Fatal("dim pane missing SurfaceMuted")
	}
	m = updateApp(t, m, tea.KeyPressMsg{Code: 'l', Mod: tea.ModCtrl})
	rightView := viewString(m)
	if !strings.Contains(rightView, rgbSGR("#778899")) {
		t.Fatal("right-focused view missing BorderFocus outline")
	}
	if !strings.Contains(rightView, rgbBGSGR("#445566")) {
		t.Fatal("right-focused title edge missing SurfaceFocus")
	}
}

// TestFocusedPaneHasBorderedChromeNotBodyWash locks stock Default() focus:
// BorderFocus outline only — no title-edge SurfaceFocus wash, no FocusBar.
func TestFocusedPaneHasBorderedChromeNotBodyWash(t *testing.T) {
	setTUITrueColor(t)
	th := theme.Default()
	th.Surface = fixedColor("#112233")
	th.SurfaceFocus = fixedColor("#445566")
	th.BorderFocus = fixedColor("#778899")
	th.SurfaceMuted = fixedColor("#aabbcc")
	m, _ := newAppTestModelWithOptions(Options{Theme: &th})
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	view := viewString(m)
	if !strings.Contains(view, rgbSGR("#778899")) {
		t.Fatal("focused outline missing BorderFocus fg")
	}
	if strings.Contains(view, rgbBGSGR("#778899")) {
		t.Fatal("focused chrome still uses solid BorderFocus fill")
	}
	if strings.Contains(view, rgbBGSGR("#445566")) {
		t.Fatal("bordered focus should not wash title edge with SurfaceFocus")
	}
	m = updateApp(t, m, tea.KeyPressMsg{Code: 'l', Mod: tea.ModCtrl})
	rightView := viewString(m)
	if !strings.Contains(rightView, rgbSGR("#778899")) {
		t.Fatal("right-focused view missing BorderFocus outline")
	}
	if strings.Contains(rightView, rgbBGSGR("#445566")) {
		t.Fatal("right-focused bordered chrome washed title with SurfaceFocus")
	}
}
