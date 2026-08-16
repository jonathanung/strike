package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/frontend/tui/theme"
)

// wide enough for horizontal pane split so right compose is exercised.
const dirtyMaskTestWidth = 120
const dirtyMaskTestHeight = 40

func warmDirtyMaskFrame(t *testing.T) Model {
	t.Helper()
	m, _ := newAppTestModel(nil, nil)
	t.Setenv("STRIKE_WORKING_CHROME", "animate")
	styleSpinner(&m.spin, m.th)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: dirtyMaskTestWidth, Height: dirtyMaskTestHeight})
	_ = m.renderFrame()
	if m.frames == nil || m.frames.width == 0 {
		t.Fatal("expected warm frame cache after renderFrame")
	}
	if m.frames.rightComposeN < 1 {
		t.Fatalf("expected right pane compose on warm paint, got %d", m.frames.rightComposeN)
	}
	return m
}

func TestSpinnerTickSkipsRightPaneCompose(t *testing.T) {
	m := warmDirtyMaskFrame(t)
	m.turnRunning = true
	beforeRight := m.frames.rightComposeN
	beforeHeader := m.frames.headerComposeN

	tick := m.spin.Tick()
	m = updateApp(t, m, tick)
	_ = m.renderFrame()

	if m.frames.rightComposeN != beforeRight {
		t.Fatalf("spinner tick re-entered rightPaneView: compose %d → %d", beforeRight, m.frames.rightComposeN)
	}
	if m.frames.headerComposeN <= beforeHeader {
		t.Fatalf("spinner tick should recompose header: compose %d → %d", beforeHeader, m.frames.headerComposeN)
	}
}

func TestDirtyMaskInvalidateResizeFocusThemeModal(t *testing.T) {
	cases := []struct {
		name string
		act  func(Model) Model
	}{
		{
			name: "resize",
			act: func(m Model) Model {
				return updateApp(t, m, tea.WindowSizeMsg{Width: dirtyMaskTestWidth + 10, Height: dirtyMaskTestHeight})
			},
		},
		{
			name: "focus cycle",
			act: func(m Model) Model {
				m = updateApp(t, m, tea.KeyPressMsg{Code: 'l', Mod: tea.ModCtrl})
				if m.focus != focusRight {
					t.Fatalf("focus = %v, want right", m.focus)
				}
				return m
			},
		},
		{
			name: "theme apply",
			act: func(m Model) Model {
				entry := theme.Entry{ID: "test-alt", Theme: theme.Default()}
				entry.Theme.Accent = theme.Default().AccentAlt
				m.applyThemeEntry(entry)
				m.clearFrameSkip()
				return m
			},
		},
		{
			name: "modal open",
			act: func(m Model) Model {
				return updateApp(t, m, tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl})
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := warmDirtyMaskFrame(t)
			before := base.frames.rightComposeN
			base = tc.act(base)
			_ = base.renderFrame()
			if base.frames.rightComposeN <= before {
				t.Fatalf("%s did not recompose right pane: %d → %d", tc.name, before, base.frames.rightComposeN)
			}
		})
	}
}

func TestDirtyMaskSpinnerPreservesRightPixels(t *testing.T) {
	m := warmDirtyMaskFrame(t)
	m.turnRunning = true
	rightBefore := m.frames.right
	if rightBefore == "" {
		t.Fatal("expected non-empty right cache")
	}
	m = updateApp(t, m, m.spin.Tick())
	_ = m.renderFrame()
	if m.frames.right != rightBefore {
		t.Fatal("spinner tick changed cached right pane string")
	}
	view := ansi.Strip(viewString(m))
	if len(view) < 10 {
		t.Fatalf("view too short after spinner: %q", view)
	}
	if !strings.Contains(view, "strike") && !strings.Contains(view, "ready") && !strings.Contains(view, "working") {
		// Brand or agent status should still appear; avoid glyph overfitting.
		t.Fatalf("view missing expected chrome after spinner:\n%s", view)
	}
}

func TestColdCacheSpinnerStillComposesRight(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	t.Setenv("STRIKE_WORKING_CHROME", "animate")
	styleSpinner(&m.spin, m.th)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: dirtyMaskTestWidth, Height: dirtyMaskTestHeight})
	if m.frames.width != 0 {
		t.Fatalf("width fingerprint = %d, want 0 before first paint", m.frames.width)
	}
	m.turnRunning = true
	m = updateApp(t, m, m.spin.Tick())
	_ = m.renderFrame()
	if m.frames.rightComposeN < 1 {
		t.Fatal("cold cache spinner path must still compose right pane")
	}
}

func TestDirtyMaskDefaultPathRecomposesLeftAfterDirectMutation(t *testing.T) {
	// Mutations outside Update must not stick to a stale left cache.
	m := warmDirtyMaskFrame(t)
	m.cells = append(m.cells, &userCell{text: "hello-from-stream"})
	m.refreshViewport()
	view := ansi.Strip(m.renderFrame())
	if !strings.Contains(view, "hello-from-stream") {
		t.Fatalf("stale left cache hid streamed cell:\n%s", view)
	}
}
