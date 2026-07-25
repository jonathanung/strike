package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

// namedWindow is a thin right-pane slot identified by id/title. Content is
// rendered by Model (context/activity bodies); the window only tracks size.
type namedWindow struct {
	windowID    string
	windowTitle string
	width       int
	height      int
}

func newNamedWindow(id, title string) namedWindow {
	return namedWindow{windowID: id, windowTitle: title}
}

func (w namedWindow) id() string { return w.windowID }

func (w namedWindow) title() string { return w.windowTitle }

func (w namedWindow) init() tea.Cmd { return nil }

func (w namedWindow) update(tea.Msg) (window, tea.Cmd) { return w, nil }

func (w namedWindow) resize(width, height int) window {
	w.width, w.height = max(0, width), max(0, height)
	return w
}

func (w namedWindow) view(theme.Theme) string { return "" }
