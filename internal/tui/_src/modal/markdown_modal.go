package tui

import (
	"path/filepath"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

// markdownModal is a large scrim overlay hosting a rendered markdown file
// (/md-read when mdReadMode is modal). Shares host-size + wide outer width
// with terminalModal so editor and reader presentation stay aligned.
type markdownModal struct {
	path   string
	source string
	vp     viewport.Model
	// renderMarkdown is overridable in tests; nil means glamourRender.
	renderMarkdown func(source string, width int) (string, error)
	// hostW/hostH stamped by Model.View for near-fullscreen sizing.
	hostW, hostH int
	contentW     int // last rendered body width (re-render on change)
}

func newMarkdownModal(path, source string) *markdownModal {
	return &markdownModal{
		path:   path,
		source: source,
		vp:     viewport.New(1, 0),
	}
}

func (m *markdownModal) setHostSize(w, h int) {
	m.hostW, m.hostH = w, h
}

func (m *markdownModal) update(msg tea.KeyMsg) (modal, tea.Cmd) {
	if isEscape(msg) || msg.String() == "q" || msg.Type == tea.KeyCtrlG {
		return nil, nil
	}
	var cmd tea.Cmd
	m.vp, cmd = m.vp.Update(msg)
	return m, cmd
}

func (m *markdownModal) view(width int, th theme.Theme) string {
	th = th.Resolve()
	outerW := m.hostW
	outerH := m.hostH
	if outerW < 1 {
		outerW = width
	}
	if outerH < 1 {
		outerH = 20
	}
	innerW := max(20, min(width, outerW-2))
	innerH := max(8, outerH-2)
	title := "markdown"
	if m.path != "" {
		title = "markdown " + filepath.Base(m.path)
	}
	contentW := ui.PanelInnerWidth(th, innerW)
	contentH := ui.PanelInnerHeight(innerW, innerH)
	if contentW < 1 {
		contentW = max(1, innerW-2)
	}
	if contentH < 1 {
		contentH = max(1, innerH-2)
	}
	m.ensureRendered(contentW, contentH)
	return ui.Panel(th, ui.PanelOpts{
		Title:   title,
		Width:   innerW,
		Height:  innerH,
		Focused: true,
		Footer:  dotJoin(th, "esc/q close", "up/down scroll"),
	}, m.vp.View())
}

func (m *markdownModal) ensureRendered(contentW, contentH int) {
	m.vp.Width = max(1, contentW)
	m.vp.Height = max(0, contentH)
	if contentW == m.contentW && m.vp.TotalLineCount() > 0 {
		return
	}
	m.contentW = contentW
	fn := m.renderMarkdown
	if fn == nil {
		fn = glamourRender
	}
	out, err := fn(m.source, contentW)
	if err != nil {
		m.vp.SetContent("md-read: render failed: " + err.Error())
		return
	}
	m.vp.SetContent(out)
}
