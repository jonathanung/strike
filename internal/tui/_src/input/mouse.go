package tui

import (
	"errors"
	"net/url"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// openURIMsg is emitted after a best-effort open of a clicked hyperlink.
// Errors are non-fatal (shown as a notice); success is silent.
type openURIMsg struct {
	uri string
	err error
}

// handleMouse routes wheel scrolling, region-limited text selection, and
// left-click hit testing for the transcript (path:line refs, OSC 8 links,
// collapsible tool/explore cells).
//
// Mouse cell motion is enabled so the terminal cannot natively highlight UI
// chrome. App-owned drag selection only starts inside the transcript and
// prompt content regions; chrome presses clear any active selection.
func (m Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	switch msg.Button { //nolint:exhaustive
	case tea.MouseButtonWheelUp:
		if msg.Action != tea.MouseActionPress {
			return m, nil
		}
		m.viewport.ScrollUp(m.viewport.MouseWheelDelta)
		return m, nil
	case tea.MouseButtonWheelDown:
		if msg.Action != tea.MouseActionPress {
			return m, nil
		}
		m.viewport.ScrollDown(m.viewport.MouseWheelDelta)
		return m, nil
	case tea.MouseButtonLeft:
		return m.handleMouseLeft(msg)
	default:
		return m, nil
	}
}

func (m Model) handleMouseLeft(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	switch msg.Action {
	case tea.MouseActionPress:
		if region, ok := m.textSelectRegionAt(msg.X, msg.Y); ok {
			m.textSel.start(screenPos{X: msg.X, Y: msg.Y}, region)
			return m, nil
		}
		// Chrome / right pane / modal: no selection highlight.
		m.textSel.clear()
		return m, nil

	case tea.MouseActionMotion:
		if m.textSel.dragging {
			m.textSel.drag(screenPos{X: msg.X, Y: msg.Y})
		}
		return m, nil

	case tea.MouseActionRelease:
		if !m.textSel.dragging {
			return m, nil
		}
		m.textSel.drag(screenPos{X: msg.X, Y: msg.Y})
		if m.textSel.finish() {
			frame := m.renderFrame()
			if text := extractTextSelection(frame, m.textSel); text != "" {
				m.cellClip.stage(text)
			}
			return m, nil
		}
		// Bare click inside a select region → existing hit testing.
		return m.handleMouseClick(msg)

	default:
		return m, nil
	}
}

// handleMouseClick runs path/link/expand actions for a left click that did not
// become a drag selection.
func (m Model) handleMouseClick(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.modal != nil || len(m.cells) == 0 {
		return m, nil
	}
	// Prefer path:line citations (open in editor) — same as Enter.
	if ref, ok := m.fileRefAtMouse(msg); ok {
		return m.openFileRef(ref)
	}
	originX, originY, ok := m.transcriptContentOrigin()
	if !ok {
		return m, nil
	}
	relX := msg.X - originX
	relY := msg.Y - originY
	if relX < 0 || relY < 0 || relX >= m.viewport.Width || relY >= m.viewport.Height {
		return m, nil
	}
	// OSC 8 targets under the cursor (http(s) / bare file:// titles).
	if uri := m.osc8AtViewport(relX, relY); uri != "" {
		// file:// with #L fragment is handled via fileRefAtMouse above when
		// plain text has path:line; remaining file:// opens via OS helper.
		if ref, ok := fileRefFromURI(uri); ok {
			return m.openFileRef(ref)
		}
		return m, openURICmd(uri)
	}
	// Toggle collapsible tool/explore cell under the click.
	contentLine := m.viewport.YOffset + relY
	idx := m.cellIndexAtContentLine(contentLine)
	if idx < 0 {
		return m, nil
	}
	switch c := m.cells[idx].(type) {
	case *toolCell:
		if !c.collapsible() {
			return m, nil
		}
		m.selectedCell = idx
		c.toggleExpanded()
		m.reflow()
		return m, nil
	case *exploreCell:
		if !c.collapsible() {
			return m, nil
		}
		m.selectedCell = idx
		c.toggleExpanded()
		m.reflow()
		return m, nil
	case *subagentResultCell:
		if !c.collapsible() {
			return m, nil
		}
		m.selectedCell = idx
		c.toggleExpanded()
		m.reflow()
		return m, nil
	}
	return m, nil
}

// fileRefFromURI parses file:///abs/path#L12 into a fileRef when possible.
func fileRefFromURI(raw string) (fileRef, bool) {
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Scheme, "file") {
		return fileRef{}, false
	}
	path := u.Path
	if path == "" {
		return fileRef{}, false
	}
	line := 0
	if frag := strings.TrimPrefix(u.Fragment, "L"); frag != "" && frag != u.Fragment {
		if n, err := strconv.Atoi(frag); err == nil && n > 0 {
			line = n
		}
	}
	return fileRef{Path: filepath.FromSlash(path), Line: line}, true
}

// cellIndexAtContentLine maps an absolute viewport content line to a cell
// index using the same layout as refreshViewport (cells joined by blank lines).
func (m *Model) cellIndexAtContentLine(line int) int {
	if line < 0 || len(m.cells) == 0 {
		return -1
	}
	width := max(1, m.viewport.Width)
	cur := 0
	for i, c := range m.cells {
		block := m.renderCell(c, width)
		// postLinkify may restyle lines but does not change line count.
		h := lipgloss.Height(block)
		if h < 1 {
			h = 1
		}
		if line >= cur && line < cur+h {
			return i
		}
		cur += h
		if i < len(m.cells)-1 {
			cur++ // separator blank line from "\n\n"
		}
	}
	return -1
}

// osc8AtViewport returns the OSC 8 URI under a point inside the viewport
// content area (relX/relY are 0-based within the viewport).
func (m Model) osc8AtViewport(relX, relY int) string {
	view := m.viewport.View()
	if view == "" {
		return ""
	}
	lines := strings.Split(view, "\n")
	if relY < 0 || relY >= len(lines) {
		return ""
	}
	return osc8URIAtCell(lines[relY], relX)
}

// openURICmd opens uri with the platform handler (browser or file manager).
// Only http(s) and file schemes are accepted.
func openURICmd(uri string) tea.Cmd {
	return func() tea.Msg {
		if err := openURI(uri); err != nil {
			return openURIMsg{uri: uri, err: err}
		}
		return openURIMsg{uri: uri}
	}
}

func openURI(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		// ok
	case "file":
		if u.Path == "" && u.Host == "" {
			return errInvalidURI
		}
		if u.Host != "" && u.Host != "localhost" {
			return errInvalidURI
		}
		raw = filepath.FromSlash(u.Path)
	default:
		return errInvalidURI
	}
	return startOpen(raw)
}

var errInvalidURI = errors.New("unsupported or invalid link")

// startOpen launches the platform open helper. Overridable in tests.
var startOpen = startOpenDefault

func startOpenDefault(target string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", target)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		cmd = exec.Command("xdg-open", target)
	}
	return cmd.Start()
}
