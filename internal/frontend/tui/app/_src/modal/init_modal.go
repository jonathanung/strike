package tui

import (
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/jonathanung/strike-cli/internal/frontend/host"
	"github.com/jonathanung/strike-cli/internal/frontend/tui/theme"
	"github.com/jonathanung/strike-cli/internal/frontend/tui/ui"
)

// initConfirmModal asks before replacing an existing AGENTS.md.
type initConfirmModal struct {
	path    string
	init    host.ProjectInit
	choice  int // 0 = replace, 1 = cancel
	decided bool
}

func newInitConfirmModal(path string, init host.ProjectInit) *initConfirmModal {
	return &initConfirmModal{path: path, init: init, choice: 1}
}

func (m *initConfirmModal) update(msg tea.KeyPressMsg) (modal, tea.Cmd) {
	if m.decided {
		return nil, nil
	}
	if isEscape(msg) {
		m.decided = true
		return nil, func() tea.Msg { return initResultMsg{canceled: true} }
	}
	switch msg.String() {
	case "left", "h", "shift+tab", "up", "k":
		m.choice = 0
	case "right", "l", "tab", "down", "j":
		m.choice = 1
	case "y", "1":
		return m.confirmReplace()
	case "n", "2":
		m.decided = true
		return nil, func() tea.Msg { return initResultMsg{canceled: true} }
	case "enter":
		if m.choice == 0 {
			return m.confirmReplace()
		}
		m.decided = true
		return nil, func() tea.Msg { return initResultMsg{canceled: true} }
	}
	return m, nil
}

func (m *initConfirmModal) confirmReplace() (modal, tea.Cmd) {
	if m.decided {
		return nil, nil
	}
	m.decided = true
	init := m.init
	return nil, func() tea.Msg {
		if init == nil {
			return initResultMsg{err: "project init unavailable"}
		}
		path, _, err := init.Write(true)
		if err != nil {
			return initResultMsg{err: err.Error()}
		}
		return initResultMsg{path: path, replaced: true}
	}
}

func (m *initConfirmModal) view(width int, th theme.Theme) string {
	th = th.Resolve()
	st := th.S()
	inner := max(1, ui.PanelInnerWidth(th, width))
	display := m.path
	if base := filepath.Base(display); base != "" && base != "." {
		display = base
	}
	heading := wrapToWidth(st.WarningStrong.Render(display+" already exists"), inner)
	detail := wrapToWidth(st.Text.Render("Replace with a freshly scanned project template? Your current file will be overwritten."), inner)
	choices := []struct {
		key, label string
	}{
		{"1", "replace"},
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
	body := heading + "\n" + detail + strings.Repeat("\n", max(1, th.Spacing.SM)) + strings.Join(parts, sep)
	return ui.Dialog(th, ui.DialogOpts{
		Title: "init",
		Hint:  dotJoin(th, "←/→ select", "enter confirm", "esc cancel"),
		Width: width,
		Tone:  ui.ToneWarning,
	}, body)
}

// initResultMsg is delivered after /init write or cancel.
type initResultMsg struct {
	path     string
	created  bool
	replaced bool
	canceled bool
	err      string
}
