package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

// undoChoice is one /undo option.
type undoChoice struct {
	label        string
	detail       string
	restoreFiles bool
}

// undoPreview is the last completed turn's harness file summary for /undo UX.
// Built from TurnCompleted (Files + checkpoint coverage flags).
type undoPreview struct {
	files     []protocol.TurnFileChange
	skipped   int
	uncovered []string
	// checkpointsGone is set when the host knows durable checkpoint bytes are
	// unavailable (legacy / failed load). Normally false after #573 persistence.
	checkpointsGone bool
}

func (p undoPreview) hasFiles() bool {
	return len(p.files) > 0 || p.skipped > 0
}

func (p undoPreview) hasUncovered() bool {
	return len(p.uncovered) > 0
}

// undoModal picks chat-only rewind vs rewind + per-file restore.
type undoModal struct {
	choices []undoChoice
	cursor  int
	ops     chan<- protocol.Op
	preview undoPreview
}

func newUndoModal(ops chan<- protocol.Op, preview undoPreview) *undoModal {
	filesDetail := "drop the last turn and restore files edited in that turn"
	if n := len(preview.files); n > 0 {
		filesDetail = fmt.Sprintf("drop the last turn and restore %d path(s)", n)
	} else if preview.skipped > 0 {
		filesDetail = "drop the last turn; checkpointed paths were not capturable"
	} else if preview.hasUncovered() {
		filesDetail = "drop the last turn; no harness file snapshots (uncovered mutations)"
	}
	m := &undoModal{
		ops:     ops,
		preview: preview,
		choices: []undoChoice{
			{
				label:        "chat only",
				detail:       "drop the last turn from history; keep disk changes",
				restoreFiles: false,
			},
			{
				label:        "chat and files",
				detail:       filesDetail,
				restoreFiles: true,
			},
		},
		// Prefer full restore when the user opens the picker, unless the turn
		// has no restorable harness paths (still allow choosing files).
		cursor: 1,
	}
	if !preview.hasFiles() && !preview.hasUncovered() {
		// Nothing known to restore — bias chat-only.
		m.cursor = 0
	}
	return m
}

func (m *undoModal) update(msg tea.KeyPressMsg) (modal, tea.Cmd) {
	if isEscape(msg) {
		return nil, nil
	}
	switch msg.String() {
	case "up", "k", "ctrl+p":
		if m.cursor > 0 {
			m.cursor--
		}
		return m, nil
	case "down", "j", "ctrl+n":
		if m.cursor < len(m.choices)-1 {
			m.cursor++
		}
		return m, nil
	case "enter":
		if m.cursor < 0 || m.cursor >= len(m.choices) {
			return m, nil
		}
		ops, restore := m.ops, m.choices[m.cursor].restoreFiles
		return nil, func() tea.Msg {
			ops <- protocol.Rewind{RestoreFiles: restore}
			return nil
		}
	case "1":
		ops := m.ops
		return nil, func() tea.Msg {
			ops <- protocol.Rewind{RestoreFiles: false}
			return nil
		}
	case "2":
		ops := m.ops
		return nil, func() tea.Msg {
			ops <- protocol.Rewind{RestoreFiles: true}
			return nil
		}
	default:
		return m, nil
	}
}

func (m *undoModal) view(width int, th theme.Theme) string {
	inner := max(1, ui.PanelInnerWidth(th, width))
	var b strings.Builder
	if body := formatUndoPreview(m.preview, inner); body != "" {
		b.WriteString(body)
		b.WriteByte('\n')
		b.WriteByte('\n')
	}
	items := make([]ui.ListItem, len(m.choices))
	for i, c := range m.choices {
		items[i] = ui.ListItem{
			Label:  c.label,
			Detail: c.detail,
		}
	}
	b.WriteString(ui.List(th, ui.ListOpts{
		Items:   items,
		Cursor:  m.cursor,
		Width:   inner,
		Visible: len(m.choices),
	}))
	return ui.Dialog(th, ui.DialogOpts{
		Title: "Undo last turn",
		Hint:  dotJoin(th, "up/down/j/k move", "1/2 or enter select", "esc close"),
		Width: width,
	}, b.String())
}

// formatUndoPreview renders paths-to-restore and coverage warnings for the
// undo modal. Empty when there is nothing useful to show.
func formatUndoPreview(p undoPreview, width int) string {
	if !p.hasFiles() && !p.hasUncovered() {
		return ""
	}
	var lines []string
	if len(p.files) > 0 {
		lines = append(lines, fmt.Sprintf("Paths to restore (%d):", len(p.files)))
		const maxShow = 12
		for i, f := range p.files {
			if i >= maxShow {
				lines = append(lines, fmt.Sprintf("  … and %d more", len(p.files)-maxShow))
				break
			}
			kind := strings.TrimSpace(f.Kind)
			path := strings.TrimSpace(f.Path)
			if path == "" {
				continue
			}
			if kind != "" {
				lines = append(lines, fmt.Sprintf("  %s  %s", kind, path))
			} else {
				lines = append(lines, "  "+path)
			}
		}
	} else if p.skipped > 0 {
		lines = append(lines, "No restorable harness paths in the last turn.")
	}
	if p.skipped > 0 {
		lines = append(lines, fmt.Sprintf("Checkpoint skipped: %d path(s) (oversized/unreadable).", p.skipped))
	}
	if p.hasUncovered() {
		reasons := strings.Join(p.uncovered, ", ")
		lines = append(lines, "Warning: uncovered mutations ("+reasons+") — shell/other changes are NOT restored.")
	}
	if p.checkpointsGone {
		lines = append(lines, "Warning: checkpoint bytes unavailable — file restore will not revert disk.")
	}
	if len(lines) == 0 {
		return ""
	}
	// Soft-wrap is handled by Dialog; keep plain text for List/Dialog body.
	_ = width
	return strings.Join(lines, "\n")
}
