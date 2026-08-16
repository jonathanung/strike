package tui

import (
	"path/filepath"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jonathanung/strike-cli/internal/frontend/tui/theme"
)

const markdownWindowID = "markdown"

type markdownWindow struct {
	path   string
	source string
	width  int
	height int
	vp     viewport.Model
	// renderMarkdown is overridable in tests; nil means glamourRender.
	renderMarkdown func(source string, width int) (string, error)
}

func newMarkdownWindow() markdownWindow {
	return markdownWindow{vp: viewport.New(viewport.WithWidth(1), viewport.WithHeight(0))}
}

func (w markdownWindow) id() string { return markdownWindowID }

func (w markdownWindow) title() string {
	if w.path == "" {
		return "markdown"
	}
	return filepath.Base(w.path)
}

func (w markdownWindow) init() tea.Cmd { return nil }

func (w markdownWindow) update(msg tea.Msg) (window, tea.Cmd) {
	var cmd tea.Cmd
	w.vp, cmd = w.vp.Update(msg)
	return w, cmd
}

// load sets the markdown source and path, resets scroll, and re-renders when
// a positive width is already known.
func (w markdownWindow) load(path, source string) markdownWindow {
	w.path = path
	w.source = source
	w.vp.GotoTop()
	w.vp.SetContent("")
	if w.width > 0 {
		w = w.render()
	}
	return w
}

func (w markdownWindow) resize(width, height int) window {
	width, height = max(0, width), max(0, height)
	widthChanged := width != w.width
	w.width, w.height = width, height
	w.vp.SetWidth(max(1, width))
	w.vp.SetHeight(max(0, height))
	if w.path != "" && w.source != "" && widthChanged && width > 0 {
		w = w.render()
	} else {
		// Clamp scroll so growing the pane doesn't leave empty padding.
		w.vp.SetYOffset(w.vp.YOffset())
	}
	return w
}

func (w markdownWindow) view(th theme.Theme) string {
	if w.width <= 0 {
		return ""
	}
	if w.path == "" {
		st := th.Resolve().S()
		return lipgloss.NewStyle().Width(max(1, w.width)).Render(
			st.Muted.Render("No file open — /md-read <path|@path>"),
		)
	}
	return w.vp.View()
}

func (w markdownWindow) render() markdownWindow {
	fn := w.renderMarkdown
	if fn == nil {
		fn = glamourRender
	}
	out, err := fn(w.source, w.width)
	if err != nil {
		w.vp.SetContent("md-read: render failed: " + err.Error())
		return w
	}
	w.vp.SetContent(out)
	return w
}
