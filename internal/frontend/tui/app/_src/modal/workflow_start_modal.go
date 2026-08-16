package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/jonathanung/strike-cli/internal/frontend/host"
	"github.com/jonathanung/strike-cli/internal/frontend/tui/theme"
	"github.com/jonathanung/strike-cli/internal/frontend/tui/ui"
	"github.com/jonathanung/strike-cli/internal/protocol"
)

// workflowStartModal previews phase-0 permission grants and autonomy before
// sending StartWorkflow. Confirm mutates engine state; cancel leaves it alone.
type workflowStartModal struct {
	summary host.WorkflowSummary
	ops     chan<- protocol.Op
	auton   protocol.Autonomy
	choice  int // 0 = start, 1 = cancel
	decided bool
}

func newWorkflowStartModal(summary host.WorkflowSummary, ops chan<- protocol.Op, auton protocol.Autonomy) *workflowStartModal {
	return &workflowStartModal{summary: summary, ops: ops, auton: auton, choice: 0}
}

func (m *workflowStartModal) update(msg tea.KeyPressMsg) (modal, tea.Cmd) {
	if m.decided {
		return nil, nil
	}
	if isEscape(msg) {
		m.decided = true
		return nil, func() tea.Msg {
			return workflowStartResultMsg{name: m.summary.Name, canceled: true}
		}
	}
	switch msg.String() {
	case "left", "h", "shift+tab", "up", "k":
		m.choice = 0
	case "right", "l", "tab", "down", "j":
		m.choice = 1
	case "y", "1":
		return m.confirmStart()
	case "n", "2":
		m.decided = true
		return nil, func() tea.Msg {
			return workflowStartResultMsg{name: m.summary.Name, canceled: true}
		}
	case "enter":
		if m.choice == 0 {
			return m.confirmStart()
		}
		m.decided = true
		return nil, func() tea.Msg {
			return workflowStartResultMsg{name: m.summary.Name, canceled: true}
		}
	}
	return m, nil
}

func (m *workflowStartModal) confirmStart() (modal, tea.Cmd) {
	if m.decided {
		return nil, nil
	}
	m.decided = true
	ops := m.ops
	name := m.summary.Name
	return nil, func() tea.Msg {
		if ops != nil {
			ops <- protocol.StartWorkflow{Name: name}
		}
		return workflowStartResultMsg{name: name, started: true}
	}
}

func (m *workflowStartModal) view(width int, th theme.Theme) string {
	th = th.Resolve()
	st := th.S()
	inner := max(1, ui.PanelInnerWidth(th, width))

	title := sanitizeDisplayData(m.summary.Name)
	src := sanitizeDisplayData(m.summary.Source)
	if src == "" {
		src = host.WorkflowSourceBuiltin
	}
	heading := wrapToWidth(st.Title.Render(fmt.Sprintf("Start %s", title)), inner)
	meta := wrapToWidth(st.Muted.Render(fmt.Sprintf(
		"source %s / %d phase(s) / autonomy %s",
		src, len(m.summary.Phases), m.auton.Normalize(),
	)), inner)

	var bodyParts []string
	bodyParts = append(bodyParts, heading, meta)

	if desc := strings.TrimSpace(m.summary.Description); desc != "" {
		bodyParts = append(bodyParts, wrapToWidth(st.Text.Render(sanitizeDisplayData(desc)), inner))
	}

	// Phase 0 grants - shown before any engine mutation.
	grantsBlock := workflowPhaseGrantsView(th, m.summary, inner)
	bodyParts = append(bodyParts, grantsBlock)

	warn := wrapToWidth(st.Warning.Render(
		"Starting applies phase permissions and may pin the phase agent. Session history is kept.",
	), inner)
	bodyParts = append(bodyParts, warn)

	choices := []struct {
		key, label string
	}{
		{"1", "start"},
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
	bodyParts = append(bodyParts, strings.Join(parts, sep))

	gap := strings.Repeat("\n", max(1, th.Spacing.SM))
	body := strings.Join(bodyParts, gap)
	return ui.Dialog(th, ui.DialogOpts{
		Title: "workflow start",
		Hint:  dotJoin(th, "←/→ select", "enter confirm", "esc cancel"),
		Width: width,
		Tone:  ui.ToneWarning,
	}, body)
}

func workflowPhaseGrantsView(th theme.Theme, w host.WorkflowSummary, inner int) string {
	th = th.Resolve()
	st := th.S()
	if len(w.Phases) == 0 {
		return wrapToWidth(st.Muted.Render("No phases - cannot activate."), inner)
	}
	p0 := w.Phases[0]
	var b strings.Builder
	fmt.Fprintf(&b, "Phase 0 %s", sanitizeDisplayData(p0.Name))
	if p0.Agent != "" {
		fmt.Fprintf(&b, " @%s", sanitizeDisplayData(p0.Agent))
	}
	gate := p0.Gate
	if gate == "" {
		gate = "agent"
	}
	fmt.Fprintf(&b, " / exit %s", gate)
	if p0.GateCommand != "" {
		fmt.Fprintf(&b, " (%s)", sanitizeDisplayData(p0.GateCommand))
	}
	header := wrapToWidth(st.Accent.Render(b.String()), inner)

	if len(p0.Permissions) == 0 {
		return header + "\n" + wrapToWidth(st.Muted.Render(
			"No phase permission overrides (config/agent rules unchanged).",
		), inner)
	}

	lines := []string{header, wrapToWidth(st.Text.Render("Pending effective permission grants:"), inner)}
	for _, r := range p0.Permissions {
		pat := r.Pattern
		if pat == "" {
			pat = "*"
		}
		line := fmt.Sprintf("  %s %s %s", r.Action, r.Permission, pat)
		tone := st.Text
		switch strings.ToLower(r.Action) {
		case "deny":
			tone = st.Error
		case "allow":
			tone = st.Success
		case "ask":
			tone = st.Warning
		}
		lines = append(lines, wrapToWidth(tone.Render(sanitizeDisplayData(line)), inner))
	}
	return strings.Join(lines, "\n")
}
