package tui

import (
	"errors"
	"net/url"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

// openURIMsg is emitted after a best-effort open of a clicked hyperlink.
// Errors are non-fatal (shown as a notice); success is silent.
type openURIMsg struct {
	uri string
	err error
}

// handleMouse routes wheel scrolling and left-click hit testing for the
// transcript: OSC 8 links open where present, collapsible tool cells toggle.
func (m Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if msg.Action != tea.MouseActionPress {
		return m, nil
	}
	switch msg.Button { //nolint:exhaustive
	case tea.MouseButtonWheelUp:
		m.viewport.ScrollUp(m.viewport.MouseWheelDelta)
		return m, nil
	case tea.MouseButtonWheelDown:
		m.viewport.ScrollDown(m.viewport.MouseWheelDelta)
		return m, nil
	case tea.MouseButtonLeft:
		if m.modal != nil || len(m.cells) == 0 {
			return m, nil
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
		// Prefer OSC 8 targets under the cursor (file/url links).
		if uri := m.osc8AtViewport(relX, relY); uri != "" {
			return m, openURICmd(uri)
		}
		// Otherwise toggle the collapsible tool/explore cell under the click.
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
		}
		return m, nil
	default:
		return m, nil
	}
}

// transcriptContentOrigin returns the screen coordinates of the top-left cell
// of the transcript viewport body (inside panel chrome when bordered).
func (m Model) transcriptContentOrigin() (x, y int, ok bool) {
	if !m.ready || m.width <= 0 || m.height <= 0 || len(m.cells) == 0 {
		return 0, 0, false
	}
	gutter := m.th.Resolve().Spacing.XS
	leftWidth := m.width
	var hGeometry paneGeometry
	if m.splitOrientation != orientVertical {
		hGeometry = computePaneGeometry(m.width, gutter, m.focus)
		leftWidth = hGeometry.leftCandidateWidth(m.width)
	}
	l := computeLayout(leftWidth, m.height, m.composer.Height(), m.completionPopupHeightFor(leftWidth), m.dangerouslySkipPermissions, m.noticeRowsFor(leftWidth))
	bodyHeight := l.transcript + l.notice + l.popup + l.composer

	showLeft := true
	if m.splitOrientation == orientVertical {
		geo := computeVerticalPaneGeometry(m.width, bodyHeight, gutter, m.focus)
		if geo.mode == paneSingle && m.focus == focusRight {
			showLeft = false
		}
	} else if hGeometry.mode == paneSingle && m.focus == focusRight {
		showLeft = false
	}
	if !showLeft || l.transcript <= 0 || m.viewport.Height <= 0 {
		return 0, 0, false
	}

	// Content starts after the header strip.
	y = l.header
	compact := leftWidth < compactWidth || m.height < compactHeight
	if !compact {
		// Panel top border + left border + horizontal pad.
		y++
		_, padX, _ := panelMetricsFor(m.th, leftWidth)
		x = 1 + padX
	}
	return x, y, true
}

func panelMetricsFor(th theme.Theme, width int) (bordered bool, padX, inner int) {
	// Mirror ui.panelMetrics without exporting it: same thresholds.
	th = th.Resolve()
	switch {
	case width < 1:
		return false, 0, 0
	case width < 3:
		return false, 0, width
	case width < 6:
		return true, 0, width - 2
	default:
		padX = th.Spacing.XS
		if padX < 0 {
			padX = 0
		}
		maxPad := (width - 3) / 2
		if padX > maxPad {
			padX = maxPad
		}
		return true, padX, width - 2 - 2*padX
	}
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
		// Reject empty / non-local targets.
		if u.Path == "" && u.Host == "" {
			return errInvalidURI
		}
		// Normalize file://localhost/path → path for the OS opener.
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
