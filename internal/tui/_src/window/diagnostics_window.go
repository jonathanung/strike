package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

const diagnosticsWindowID = "diagnostics"

// diagnosticsRefreshInterval caps idle diagnostic rescans (~1 Hz).
const diagnosticsRefreshInterval = time.Second

// diagnosticsRefreshMsg triggers a cheap re-list of live diagnostics.
type diagnosticsRefreshMsg struct{}

func diagnosticsRefreshCmd() tea.Cmd {
	return tea.Tick(diagnosticsRefreshInterval, func(time.Time) tea.Msg {
		return diagnosticsRefreshMsg{}
	})
}

// diagnosticsWindow is the right-pane browser for language-server findings.
type diagnosticsWindow struct {
	lsp    host.LSP
	root   string
	items  []host.Diagnostic
	cursor int
	width  int
	height int
}

func newDiagnosticsWindow() diagnosticsWindow {
	return diagnosticsWindow{}
}

func (w diagnosticsWindow) id() string { return diagnosticsWindowID }

func (w diagnosticsWindow) title() string { return "diagnostics" }

// init does not arm the idle poll. Polling runs only while the diagnostics
// pane is active (see diagnosticsWindowActive).
func (w diagnosticsWindow) init() tea.Cmd { return nil }

// diagnosticsWindowActive reports whether diagnostics is in the active
// right-pane group (and therefore visible when the right pane is shown).
func diagnosticsWindowActive(r windowRegistry) bool {
	for _, wi := range r.activeGroup().members {
		if wi < 0 || wi >= len(r.windows) {
			continue
		}
		if r.windows[wi].id() == diagnosticsWindowID {
			return true
		}
	}
	return false
}

// diagnosticsPollCmd arms the ~1 Hz rescan when the diagnostics pane is active.
func diagnosticsPollCmd(r windowRegistry) tea.Cmd {
	if !diagnosticsWindowActive(r) {
		return nil
	}
	return diagnosticsRefreshCmd()
}

// rightPanePollCmd arms idle polls/ticks for active right-pane windows
// (files, diagnostics, pets). Nil when none need a tick.
func rightPanePollCmd(r windowRegistry) tea.Cmd {
	return tea.Batch(filesPollCmd(r), diagnosticsPollCmd(r), petsAnimCmd(r))
}

func (w diagnosticsWindow) update(msg tea.Msg) (window, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return w.handleKey(msg)
	}
	return w, nil
}

func (w diagnosticsWindow) resize(width, height int) window {
	w.width, w.height = max(0, width), max(0, height)
	return w
}

func (w diagnosticsWindow) view(th theme.Theme) string {
	if w.width <= 0 {
		return ""
	}
	th = th.Resolve()
	st := th.S()
	if w.lsp == nil {
		return lipgloss.NewStyle().Width(max(1, w.width)).Render(
			st.Muted.Render("diagnostics unavailable"),
		)
	}
	visible := w.height
	if visible < 1 {
		visible = 0
	}
	items := make([]ui.ListItem, len(w.items))
	for i, d := range w.items {
		items[i] = ui.ListItem{
			Label:  diagnosticsLabel(d),
			Detail: diagnosticsDetail(w.root, d),
		}
	}
	return ui.List(th, ui.ListOpts{
		Items:   items,
		Cursor:  w.cursor,
		Width:   w.width,
		Visible: visible,
		Empty:   "no diagnostics",
	})
}

func diagnosticsLabel(d host.Diagnostic) string {
	sev := strings.TrimSpace(d.Severity)
	if sev == "" {
		sev = "error"
	}
	return sanitizeDisplayData(sev)
}

func diagnosticsDetail(root string, d host.Diagnostic) string {
	path := displayPath(root, d.Path)
	loc := fmt.Sprintf("%s:%d:%d", path, d.Line, d.Character)
	msg := strings.TrimSpace(d.Message)
	if msg == "" {
		msg = "(no message)"
	}
	parts := []string{loc, msg}
	if src := strings.TrimSpace(d.Source); src != "" {
		parts = append(parts, "["+src+"]")
	}
	if code := strings.TrimSpace(d.Code); code != "" {
		parts = append(parts, "("+code+")")
	}
	return sanitizeDisplayData(strings.Join(parts, " "))
}

func (w diagnosticsWindow) bind(lsp host.LSP, root string) diagnosticsWindow {
	w.lsp = lsp
	w.root = strings.TrimSpace(root)
	return w.reload()
}

func (w diagnosticsWindow) reload() diagnosticsWindow {
	if w.lsp == nil {
		w.items = nil
		w.cursor = 0
		return w
	}
	w.items = append([]host.Diagnostic(nil), w.lsp.Diagnostics()...)
	if len(w.items) == 0 {
		w.cursor = 0
	} else if w.cursor >= len(w.items) {
		w.cursor = len(w.items) - 1
	} else if w.cursor < 0 {
		w.cursor = 0
	}
	return w
}

func (w diagnosticsWindow) handleKey(msg tea.KeyPressMsg) (diagnosticsWindow, tea.Cmd) {
	if w.lsp == nil {
		return w, nil
	}
	switch msg.String() {
	case "up", "k":
		if w.cursor > 0 {
			w.cursor--
		}
	case "down", "j":
		if w.cursor < len(w.items)-1 {
			w.cursor++
		}
	case "r":
		w = w.reload()
	case "enter", "right", "l":
		if len(w.items) == 0 || w.cursor < 0 || w.cursor >= len(w.items) {
			return w, nil
		}
		d := w.items[w.cursor]
		path := d.Path
		if w.root != "" {
			if rel, err := filepath.Rel(w.root, d.Path); err == nil && !strings.HasPrefix(rel, "..") {
				path = rel
			}
		}
		return w, func() tea.Msg {
			return filesOpenMsg{path: path, line: d.Line}
		}
	}
	return w, nil
}

// configureDiagnosticsWindow binds host.LSP + workDir onto the diagnostics slot.
func configureDiagnosticsWindow(r windowRegistry, root string, lsp host.LSP) windowRegistry {
	for i, w := range r.windows {
		dw, ok := w.(diagnosticsWindow)
		if !ok {
			continue
		}
		next := dw.bind(lsp, root)
		windows := append([]window(nil), r.windows...)
		windows[i] = next
		r.windows = windows
		return r
	}
	return r
}

// refreshDiagnosticsWindows reloads diagnostics panes from host.LSP.
func refreshDiagnosticsWindows(r windowRegistry) windowRegistry {
	if len(r.windows) == 0 {
		return r
	}
	windows := append([]window(nil), r.windows...)
	changed := false
	for i, w := range windows {
		dw, ok := w.(diagnosticsWindow)
		if !ok {
			continue
		}
		windows[i] = dw.reload()
		changed = true
	}
	if !changed {
		return r
	}
	r.windows = windows
	return r
}
