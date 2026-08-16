package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jonathanung/strike-cli/internal/frontend/tui/theme"
	"github.com/jonathanung/strike-cli/internal/frontend/tui/ui"
	"github.com/jonathanung/strike-cli/pkg/timeline"
)

// timelineModal shows a collapsed structured run timeline (turns/tools/…).
type timelineModal struct {
	tr     timeline.Trace
	scroll int
}

func newTimelineModal(tr timeline.Trace) *timelineModal {
	return &timelineModal{tr: tr}
}

func (m *timelineModal) update(msg tea.KeyPressMsg) (modal, tea.Cmd) {
	if isEscape(msg) || msg.String() == "q" || msg.String() == "enter" {
		return nil, nil
	}
	switch msg.String() {
	case "up", "ctrl+p", "k":
		if m.scroll > 0 {
			m.scroll--
		}
	case "down", "ctrl+n", "j":
		m.scroll++
	}
	return m, nil
}

func (m *timelineModal) view(width int, th theme.Theme) string {
	th = th.Resolve()
	st := th.S()
	inner := max(1, ui.PanelInnerWidth(th, width))
	if width < 4 {
		inner = max(1, width)
	}
	lines := m.bodyLines(th)
	const maxBody = 20
	if m.scroll > max(0, len(lines)-maxBody) {
		m.scroll = max(0, len(lines)-maxBody)
	}
	visible := lines
	if len(lines) > maxBody {
		end := min(len(lines), m.scroll+maxBody)
		visible = lines[m.scroll:end]
	}
	wrapped := make([]string, 0, len(visible))
	for _, line := range visible {
		wrapped = append(wrapped, lipgloss.NewStyle().Width(inner).Render(line))
	}
	body := strings.Join(wrapped, "\n")
	if width < 4 {
		return body
	}
	hint := dotJoin(th, "esc close")
	if len(lines) > maxBody {
		hint = dotJoin(th, "↑/↓ scroll", "esc close")
	}
	_ = st
	return ui.Dialog(th, ui.DialogOpts{
		Title: "Run timeline",
		Hint:  hint,
		Width: width,
	}, body)
}

func (m *timelineModal) bodyLines(th theme.Theme) []string {
	th = th.Resolve()
	st := th.S()
	s := m.tr.Summary
	var lines []string
	if id := strings.TrimSpace(m.tr.SessionID); id != "" {
		lines = append(lines, st.Muted.Render("session ")+st.Text.Render(id))
	}
	lines = append(lines, st.Muted.Render(dotJoin(th,
		fmt.Sprintf("turns %d", s.Turns),
		fmt.Sprintf("tools %d", s.Tools),
		fmt.Sprintf("provider %d", s.Providers),
		fmt.Sprintf("children %d", s.Children),
		fmt.Sprintf("failed %d", s.Failed),
		fmt.Sprintf("canceled %d", s.Canceled),
	)))
	if s.InputTok > 0 || s.OutputTok > 0 {
		lines = append(lines, st.Muted.Render(dotJoin(th,
			fmt.Sprintf("tokens in %d", s.InputTok),
			fmt.Sprintf("out %d", s.OutputTok),
		)))
	}
	if s.DurationMs > 0 {
		lines = append(lines, st.Muted.Render(fmt.Sprintf("span %s", formatTimelineDuration(s.DurationMs))))
	}
	lines = append(lines, "")
	collapsed := timeline.FormatCollapsed(m.tr.Entries, 0)
	if collapsed == "" {
		lines = append(lines, st.Muted.Render("(no entries)"))
		return lines
	}
	for _, line := range strings.Split(collapsed, "\n") {
		lines = append(lines, st.Text.Render(line))
	}
	lines = append(lines, "")
	lines = append(lines, st.Muted.Render(dotJoin(th,
		"export: /timeline export [path]",
		"diag: /diag [export [path]]",
		"redacted JSON",
	)))
	return lines
}

func formatTimelineDuration(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	if ms < 60_000 {
		return fmt.Sprintf("%.1fs", float64(ms)/1000)
	}
	return fmt.Sprintf("%.1fm", float64(ms)/60_000)
}
