package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/pkg/diag"
)

// diagFinishedMsg is delivered after async diagnostic bundle export completes.
type diagFinishedMsg struct {
	path string
	err  error
}

func (m Model) handleDiagCommand(args []string) (tea.Model, tea.Cmd) {
	m.resetComposer()
	m.clearNotice()
	if len(args) > 0 {
		switch args[0] {
		case "export":
			return m.handleDiagExport(args[1:])
		case "help", "-h", "--help":
			m.setNotice("usage: /diag | /diag export [path]", true)
			return m, nil
		default:
			m.setNotice("usage: /diag | /diag export [path]", true)
			return m, nil
		}
	}
	// Bare /diag exports to the default path (same as /diag export).
	return m.handleDiagExport(nil)
}

func (m Model) handleDiagExport(args []string) (tea.Model, tea.Cmd) {
	pathArg := ""
	if len(args) > 1 {
		m.setNotice("usage: /diag export [path]", true)
		return m, nil
	}
	if len(args) == 1 {
		if strings.HasPrefix(args[0], "-") {
			m.setNotice("usage: /diag export [path]", true)
			return m, nil
		}
		pathArg = args[0]
	}
	var path string
	if pathArg == "" {
		path = defaultDiagExportPath(m.workDir, m.sessionID, time.Now())
	} else {
		resolved, err := resolveExportPath(m.workDir, pathArg)
		if err != nil {
			m.setNotice("diag export: "+err.Error(), true)
			return m, nil
		}
		path = resolved
	}
	m.pendingDiagExportPath = path
	m.setNotice("exporting diagnostic bundle…", false)
	ops := m.ops
	return m, func() tea.Msg {
		ops <- protocol.InspectDiagnosticBundle{}
		return nil
	}
}

func defaultDiagExportPath(workDir, sessionID string, now time.Time) string {
	if now.IsZero() {
		now = time.Now()
	}
	stamp := now.UTC().Format("20060102-150405")
	short := shortSessionID(sessionID)
	if short == "" {
		short = "session"
	}
	name := fmt.Sprintf("strike-diag-%s-%s.json", short, stamp)
	workDir = strings.TrimSpace(workDir)
	if workDir != "" {
		return filepath.Join(workDir, ".strike", "exports", name)
	}
	return filepath.Join(os.TempDir(), name)
}

func (m *Model) applyDiagnosticBundle(ev protocol.DiagnosticBundle) tea.Cmd {
	path := strings.TrimSpace(m.pendingDiagExportPath)
	m.pendingDiagExportPath = ""
	if path == "" {
		// Unsolicited bundle: compact notice (rpc/other frontends may still use the event).
		n := ev.Prompt.LayerCount
		m.setNotice(fmt.Sprintf("diagnostic bundle: %d layers, %d system chars", n, ev.Prompt.SystemChars), false)
		return nil
	}
	b := diag.FromProtocol(ev)
	m.setNotice("writing diagnostic bundle…", false)
	return diagExportCmd(path, b)
}

func diagExportCmd(path string, b diag.Bundle) tea.Cmd {
	return func() tea.Msg {
		err := diag.ExportJSON(path, b)
		if err != nil {
			return diagFinishedMsg{err: err}
		}
		return diagFinishedMsg{path: path}
	}
}

func (m Model) applyDiagFinished(msg diagFinishedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.setNotice("diag export failed: "+msg.err.Error(), true)
		return m, nil
	}
	display := displayPath(m.workDir, msg.path)
	if display == "" {
		display = msg.path
	}
	m.setNotice("diagnostic bundle exported to "+display, false)
	return m, nil
}
