package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

type statefulTestWindow struct {
	windowID    string
	windowTitle string
	updates     []string
	width       int
	height      int
}

func (w statefulTestWindow) id() string    { return w.windowID }
func (w statefulTestWindow) title() string { return w.windowTitle }
func (w statefulTestWindow) init() tea.Cmd { return nil }
func (w statefulTestWindow) update(msg tea.Msg) (window, tea.Cmd) {
	w.updates = append(w.updates, msg.(tea.KeyMsg).String())
	return w, nil
}
func (w statefulTestWindow) resize(width, height int) window {
	w.width, w.height = width, height
	return w
}
func (w statefulTestWindow) view(theme.Theme) string { return w.windowID }

var _ window = statefulTestWindow{}

func testWindow(t *testing.T, w window) statefulTestWindow {
	t.Helper()
	got, ok := w.(statefulTestWindow)
	if !ok {
		t.Fatalf("window = %T, want statefulTestWindow", w)
	}
	return got
}

func TestWindowRegistryPreservesOrderAndValueStateAcrossCycleUpdateAndResize(t *testing.T) {
	r := windowRegistry{windows: []window{
		statefulTestWindow{windowID: "one", windowTitle: "One"},
		statefulTestWindow{windowID: "two", windowTitle: "Two"},
	}}
	if got := r.active().id(); got != "one" {
		t.Fatalf("initial active window = %q, want one", got)
	}
	r = r.cycle()
	if got := r.active().id(); got != "two" {
		t.Fatalf("cycled active window = %q, want two", got)
	}
	r, _ = r.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if got := testWindow(t, r.active()).updates; len(got) != 1 || got[0] != "x" {
		t.Fatalf("active update = %q, want [x]", got)
	}
	r = r.resize(23, 7)
	if got := testWindow(t, r.active()); got.width != 23 || got.height != 7 || len(got.updates) != 1 {
		t.Errorf("active resize replacement = %+v, want dimensions and prior state", got)
	}
	r = r.cycle()
	if got := testWindow(t, r.active()); got.windowID != "one" || len(got.updates) != 0 || got.width != 23 || got.height != 7 {
		t.Errorf("inactive window was changed or not resized: %+v", got)
	}
	r = r.cycle()
	if got := testWindow(t, r.active()); got.windowID != "two" || len(got.updates) != 1 || got.updates[0] != "x" {
		t.Errorf("active state did not survive cycling away and back: %+v", got)
	}
}

func TestModelsHaveIsolatedWindowRegistryState(t *testing.T) {
	left, _ := newAppTestModel(nil, nil)
	right, _ := newAppTestModel(nil, nil)
	left.windows = windowRegistry{windows: []window{statefulTestWindow{windowID: "left"}}}
	right.windows = windowRegistry{windows: []window{statefulTestWindow{windowID: "right"}}}
	left.focus = focusRight
	left = updateApp(t, left, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	left = updateApp(t, left, tea.WindowSizeMsg{Width: 11, Height: 5})

	if got := testWindow(t, left.windows.active()); len(got.updates) != 1 || got.width != 11 || got.height != 3 {
		t.Errorf("left model window = %+v, want its update and compact right-pane body dimensions 11x3", got)
	}
	if got := testWindow(t, right.windows.active()); len(got.updates) != 0 || got.width != 0 || got.height != 0 {
		t.Errorf("right model shared left window state: %+v", got)
	}
}

func TestRightWindowResizeUsesActualPanelInnerHeight(t *testing.T) {
	for _, tt := range []struct {
		name          string
		width, height int
		wantHeight    int
	}{
		{"one-column unbordered pane", 1, 40, 38},
		{"canonical bordered pane", 80, 40, 36},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := newAppTestModel(nil, nil)
			m.focus = focusRight
			m.windows = windowRegistry{windows: []window{statefulTestWindow{windowID: "right"}}}
			m = updateApp(t, m, tea.WindowSizeMsg{Width: tt.width, Height: tt.height})

			got := testWindow(t, m.windows.active())
			if got.width != ui.PanelInnerWidth(m.th, tt.width) || got.height != tt.wantHeight {
				t.Errorf("%dx%d right window dimensions = %dx%d, want %dx%d", tt.width, tt.height, got.width, got.height, ui.PanelInnerWidth(m.th, tt.width), tt.wantHeight)
			}
		})
	}
}

func TestCompactRightPaneIsBorderlessAndUsesFullBodyDimensionsAtThresholds(t *testing.T) {
	for _, tt := range []struct {
		name                        string
		width, height, wantW, wantH int
		borderless                  bool
	}{
		{"59x30 compact width", 59, 30, 59, 28, true},
		{"80x19 compact height", 80, 19, 80, 17, true},
		{"60x20 bordered threshold", 60, 20, 56, 16, false},
		{"93x60 canonical split", 93, 60, 28, 56, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := newAppTestModel(nil, nil)
			m.focus = focusRight
			m.windows = windowRegistry{windows: []window{statefulTestWindow{windowID: "right", windowTitle: "context"}}}
			m = updateApp(t, m, tea.WindowSizeMsg{Width: tt.width, Height: tt.height})

			got := testWindow(t, m.windows.active())
			if got.width != tt.wantW || got.height != tt.wantH {
				t.Errorf("registry dimensions = %dx%d, want %dx%d", got.width, got.height, tt.wantW, tt.wantH)
			}
			view := m.View()
			rows := strings.Split(view, "\n")
			if len(rows) != tt.height {
				t.Fatalf("canvas rows = %d, want %d", len(rows), tt.height)
			}
			for i, row := range rows {
				if got := lipgloss.Width(row); got != tt.width {
					t.Errorf("canvas row %d width = %d, want %d", i, got, tt.width)
				}
			}
			plain := strings.Join(rows, "\n")
			if tt.borderless && (strings.Contains(plain, "╭") || strings.Contains(plain, "╰") || strings.Contains(plain, "context")) {
				t.Errorf("compact right pane retained panel chrome: %q", plain)
			}
			if !tt.borderless && !strings.Contains(plain, "context") {
				t.Errorf("bordered right pane omitted its title: %q", plain)
			}
		})
	}
}

func TestDefaultWindowRegistryHasTwoUniqueNamedWindows(t *testing.T) {
	r := newWindowRegistry()
	if len(r.windows) != 2 {
		t.Fatalf("window count = %d, want 2", len(r.windows))
	}
	wantIDs := []string{"context", "activity"}
	seenIDs, seenTitles := map[string]bool{}, map[string]bool{}
	for i, w := range r.windows {
		if w.id() == "" || w.title() == "" || seenIDs[w.id()] || seenTitles[w.title()] {
			t.Errorf("window identity is missing or not unique: id=%q title=%q", w.id(), w.title())
		}
		seenIDs[w.id()], seenTitles[w.title()] = true, true
		if i < len(wantIDs) && w.id() != wantIDs[i] {
			t.Errorf("windows[%d].id() = %q, want %q", i, w.id(), wantIDs[i])
		}
		if _, ok := w.(namedWindow); !ok {
			t.Errorf("window = %#v, want a namedWindow", w)
		}
		// Resize is width-safe: dimensions stick and view stays within width.
		for _, size := range []struct{ w, h int }{{80, 3}, {12, 5}, {1, 1}} {
			resized := w.resize(size.w, size.h)
			nw, ok := resized.(namedWindow)
			if !ok || nw.width != size.w || nw.height != size.h {
				t.Errorf("resize(%d,%d) = %#v, want namedWindow %dx%d", size.w, size.h, resized, size.w, size.h)
			}
		}
	}
	if !seenIDs["context"] || !seenIDs["activity"] {
		t.Errorf("default registry ids = %v, want context and activity", seenIDs)
	}

	// Full Model.View at split size shows real context content, not a placeholder.
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 93, Height: 40})
	plain := ansi.Strip(m.View())
	if strings.Contains(plain, "Context window placeholder.") {
		t.Errorf("split view still shows placeholder copy:\n%s", plain)
	}
	if !strings.Contains(plain, "none") && !strings.Contains(plain, "/provider") {
		t.Errorf("split view missing real context content (none or /provider):\n%s", plain)
	}
	if !strings.Contains(plain, "context") {
		t.Errorf("split view missing context window title:\n%s", plain)
	}
}
