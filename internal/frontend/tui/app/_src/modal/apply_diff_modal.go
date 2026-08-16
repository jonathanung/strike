package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/jonathanung/strike-cli/internal/frontend/host"
	"github.com/jonathanung/strike-cli/internal/frontend/tui/theme"
	"github.com/jonathanung/strike-cli/internal/frontend/tui/ui"
)

// applyDiffModal confirms writing a shown edit/patch into the active worktree.
type applyDiffModal struct {
	files      host.Files
	path       string // edit target path (empty for multi-file patch)
	oldString  string
	newString  string
	replaceAll bool
	patch      string // full apply_patch envelope when non-empty
	summary    string // short description for the dialog body
	choice     int    // 0 = apply, 1 = cancel
	decided    bool
	// diffOffset scrolls the DiffPreview body when the hunk exceeds MaxLines.
	// When diffScrolled is false, Offset 0 keeps change-preferring trim; the
	// first scroll snaps to DiffWindowStart then applies the delta.
	diffOffset   int
	diffScrolled bool
}

func newApplyDiffModalEdit(files host.Files, path, oldString, newString string, replaceAll bool) *applyDiffModal {
	sum := path
	if replaceAll {
		sum += " (replace all)"
	}
	return &applyDiffModal{
		files:      files,
		path:       path,
		oldString:  oldString,
		newString:  newString,
		replaceAll: replaceAll,
		summary:    sum,
		choice:     0,
	}
}

func newApplyDiffModalPatch(files host.Files, patch string) *applyDiffModal {
	return &applyDiffModal{
		files:   files,
		patch:   patch,
		summary: "multi-file patch",
		choice:  0,
	}
}

func (m *applyDiffModal) update(msg tea.KeyPressMsg) (modal, tea.Cmd) {
	if m.decided {
		return nil, nil
	}
	if isEscape(msg) {
		m.decided = true
		return nil, func() tea.Msg { return applyDiffResultMsg{canceled: true} }
	}
	switch msg.String() {
	case "left", "h", "shift+tab":
		m.choice = 0
	case "right", "l", "tab":
		m.choice = 1
	case "up", "k":
		// Prefer scrolling a tall diff; otherwise move choice to apply.
		if m.scrollDiff(-1) {
			return m, nil
		}
		m.choice = 0
	case "down", "j":
		if m.scrollDiff(1) {
			return m, nil
		}
		m.choice = 1
	case "pgup", "ctrl+u":
		m.scrollDiff(-diffPreviewMaxLinesModal)
		return m, nil
	case "pgdown", "ctrl+d":
		m.scrollDiff(diffPreviewMaxLinesModal)
		return m, nil
	case "y", "1", "a":
		return m.confirmApply()
	case "n", "2":
		m.decided = true
		return nil, func() tea.Msg { return applyDiffResultMsg{canceled: true} }
	case "enter":
		if m.choice == 0 {
			return m.confirmApply()
		}
		m.decided = true
		return nil, func() tea.Msg { return applyDiffResultMsg{canceled: true} }
	}
	return m, nil
}

// scrollDiff moves the diff window by delta lines. Returns false when the
// hunk fits in MaxLines (so callers can fall back to choice navigation).
func (m *applyDiffModal) scrollDiff(delta int) bool {
	if m.patch != "" || (m.oldString == "" && m.newString == "") {
		return false
	}
	maxOff := ui.DiffMaxOffset(m.oldString, m.newString, diffPreviewMaxLinesModal)
	if maxOff <= 0 {
		return false
	}
	cur := m.diffOffset
	if !m.diffScrolled {
		// Snap from change-preferring auto window to an absolute offset so the
		// first ↓/↑ continues from what the user already sees.
		cur = ui.DiffWindowStart(m.oldString, m.newString, diffPreviewMaxLinesModal)
		m.diffScrolled = true
	}
	next := cur + delta
	if next < 0 {
		next = 0
	}
	if next > maxOff {
		next = maxOff
	}
	m.diffOffset = next
	return true
}

func (m *applyDiffModal) confirmApply() (modal, tea.Cmd) {
	if m.decided {
		return nil, nil
	}
	m.decided = true
	files := m.files
	path, oldS, newS, replaceAll := m.path, m.oldString, m.newString, m.replaceAll
	patch := m.patch
	return nil, func() tea.Msg {
		if files == nil {
			return applyDiffResultMsg{err: "file apply unavailable"}
		}
		if patch != "" {
			summary, err := files.ApplyPatch(patch)
			if err != nil {
				return applyDiffResultMsg{err: err.Error()}
			}
			return applyDiffResultMsg{summary: firstLine(summary), multi: true}
		}
		res, err := files.ApplyEdit(host.EditApply{
			Path:       path,
			OldString:  oldS,
			NewString:  newS,
			ReplaceAll: replaceAll,
		})
		if err != nil {
			return applyDiffResultMsg{err: err.Error()}
		}
		return applyDiffResultMsg{
			path:    res.Path,
			count:   res.Count,
			already: res.Already,
		}
	}
}

func (m *applyDiffModal) view(width int, th theme.Theme) string {
	th = th.Resolve()
	st := th.S()
	inner := max(1, ui.PanelInnerWidth(th, width))
	heading := wrapToWidth(st.WarningStrong.Render("Apply patch to worktree?"), inner)
	detail := wrapToWidth(st.Text.Render(m.summary), inner)
	var diffSection string
	scrollable := false
	if m.patch == "" && (m.oldString != "" || m.newString != "") {
		moreHint := ""
		if ui.DiffExceeds(m.oldString, m.newString, diffPreviewMaxLinesModal) {
			scrollable = true
			moreHint = "↑/↓ scroll"
		}
		// Clamp offset if the hunk shrank (should not happen mid-modal).
		if maxOff := ui.DiffMaxOffset(m.oldString, m.newString, diffPreviewMaxLinesModal); m.diffOffset > maxOff {
			m.diffOffset = maxOff
		}
		diffBlock := ui.DiffPreview(th, ui.DiffPreviewOpts{
			Path:      "",
			Old:       m.oldString,
			New:       m.newString,
			MaxLines:  diffPreviewMaxLinesModal,
			Width:     inner,
			ShowStats: true,
			Offset:    m.diffOffset,
			MoreHint:  moreHint,
		})
		if diffBlock != "" {
			diffSection = "\n" + diffBlock
		}
	}
	choices := []struct {
		key, label string
	}{
		{"1", "apply"},
		{"2", "cancel"},
	}
	parts := make([]string, len(choices))
	for i, c := range choices {
		label := c.key + ")" + themedSpace(th.Spacing.Label) + c.label
		style := st.Muted
		if i == m.choice {
			style = st.SelectedUnderline
		}
		parts[i] = style.Render(label)
	}
	sep := themedSpace(th.Spacing.SM)
	body := heading + "\n" + detail + diffSection + strings.Repeat("\n", max(1, th.Spacing.SM)) + strings.Join(parts, sep)
	hints := []string{"←/→ select", "enter confirm", "esc cancel"}
	if scrollable {
		hints = []string{"↑/↓ scroll", "←/→ select", "enter confirm", "esc cancel"}
	}
	return ui.Dialog(th, ui.DialogOpts{
		Title: "apply patch",
		Hint:  dotJoin(th, hints...),
		Width: width,
		Tone:  ui.ToneWarning,
	}, body)
}

// applyDiffResultMsg is delivered after confirm/cancel of worktree patch apply.
type applyDiffResultMsg struct {
	path     string
	count    int
	already  bool
	summary  string
	multi    bool
	canceled bool
	err      string
}
