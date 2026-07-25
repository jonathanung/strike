package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

type placeholderWindow struct {
	windowID    string
	windowTitle string
	body        string
	width       int
	height      int
	lastInput   string
}

func newPlaceholderWindow(id, title, body string) placeholderWindow {
	return placeholderWindow{windowID: id, windowTitle: title, body: body}
}

func (w placeholderWindow) id() string { return w.windowID }

func (w placeholderWindow) title() string { return w.windowTitle }

func (w placeholderWindow) init() tea.Cmd { return nil }

func (w placeholderWindow) update(msg tea.Msg) (window, tea.Cmd) {
	if msg, ok := msg.(tea.KeyMsg); ok {
		w.lastInput = msg.String()
	}
	return w, nil
}

func (w placeholderWindow) resize(width, height int) window {
	w.width, w.height = max(0, width), max(0, height)
	return w
}

func (w placeholderWindow) view(th theme.Theme) string {
	if w.width <= 0 {
		return ""
	}
	st := th.Resolve().S()
	lines := []string{wrapWindowText(st.Title.Render(w.windowTitle), w.width), wrapWindowText(st.Text.Render(w.body), w.width)}
	if w.lastInput != "" {
		lines = append(lines, wrapWindowText(st.Muted.Render("last input: "+w.lastInput), w.width))
	}
	return strings.Join(lines, "\n")
}

func wrapWindowText(text string, width int) string {
	return lipgloss.NewStyle().Width(max(1, width)).Render(text)
}
