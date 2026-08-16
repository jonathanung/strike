package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

// window is a value-oriented right-pane surface. Implementations return their
// replacement from update and resize so Model copies never share mutable state.
type window interface {
	id() string
	title() string
	init() tea.Cmd
	update(tea.Msg) (window, tea.Cmd)
	resize(width, height int) window
	view(theme.Theme) string
}
