package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/protocol"
)

const workflowUsage = `usage: /workflow [list|inspect <name>|start <name>|stop]
/workflow list                 # catalog (source, phases, valid)
/workflow inspect <name>       # phases, gates, fingerprint, grants
/workflow start <name>         # preview phase grants, then activate
/workflow stop                 # clear active phase (session history kept)`

func (m Model) handleWorkflowCommand(args []string) (tea.Model, tea.Cmd) {
	m.resetComposer()
	if m.services.Workflows == nil {
		m.setNotice("workflows are unavailable", true)
		return m, nil
	}
	if len(args) == 0 {
		args = []string{"list"}
	}
	switch strings.ToLower(args[0]) {
	case "list", "ls":
		return m.workflowList()
	case "inspect", "show", "info":
		if len(args) < 2 {
			m.setNotice("workflow: inspect requires a name - "+workflowUsage, true)
			return m, nil
		}
		return m.workflowInspect(strings.Join(args[1:], " "))
	case "start", "run":
		if len(args) < 2 {
			m.setNotice("workflow: start requires a name - "+workflowUsage, true)
			return m, nil
		}
		return m.workflowStart(strings.Join(args[1:], " "))
	case "stop", "clear", "end":
		return m.workflowStop()
	default:
		// Bare name: treat as inspect when it matches a catalog entry,
		// otherwise show usage (avoids accidental activation).
		name := strings.Join(args, " ")
		if _, ok := m.services.Workflows.Get(name); ok {
			return m.workflowInspect(name)
		}
		m.setNotice(workflowUsage, true)
		return m, nil
	}
}

func (m Model) workflowList() (tea.Model, tea.Cmd) {
	list := m.services.Workflows.List()
	if len(list) == 0 {
		m.setNotice("workflow: no workflows loaded", false)
		return m, nil
	}
	var b strings.Builder
	b.WriteString("workflow: ")
	for i, w := range list {
		if i > 0 {
			b.WriteString(" | ")
		}
		status := "ok"
		if !w.Valid {
			status = "invalid"
		}
		fmt.Fprintf(&b, "%s [%s/%s/%dph]",
			sanitizeDisplayData(w.Name),
			sanitizeDisplayData(w.Source),
			status,
			len(w.Phases),
		)
	}
	if m.phaseWorkflow != "" {
		fmt.Fprintf(&b, " || active %s", sanitizeDisplayData(m.phaseWorkflow))
		if m.phaseName != "" {
			fmt.Fprintf(&b, "/%s", sanitizeDisplayData(m.phaseName))
		}
	}
	m.setNotice(b.String(), false)
	return m, nil
}

func (m Model) workflowInspect(name string) (tea.Model, tea.Cmd) {
	name = strings.TrimSpace(name)
	w, ok := m.services.Workflows.Get(name)
	if !ok {
		m.setNotice(fmt.Sprintf("workflow: unknown %q", name), true)
		return m, nil
	}
	m.setNotice(formatWorkflowInspect(w), false)
	return m, nil
}

func formatWorkflowInspect(w host.WorkflowSummary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "workflow %s [%s]", sanitizeDisplayData(w.Name), sanitizeDisplayData(w.Source))
	if w.Valid {
		b.WriteString(" valid")
	} else {
		b.WriteString(" INVALID")
		if w.ValidationError != "" {
			fmt.Fprintf(&b, " (%s)", sanitizeDisplayData(w.ValidationError))
		}
	}
	if w.Description != "" {
		fmt.Fprintf(&b, " - %s", sanitizeDisplayData(truncateRunes(w.Description, 60)))
	}
	if w.Fingerprint != "" {
		fp := w.Fingerprint
		if len(fp) > 12 {
			fp = fp[:12]
		}
		fmt.Fprintf(&b, " fp=%s", fp)
	}
	if len(w.Phases) == 0 {
		b.WriteString(" | (no phases)")
		return b.String()
	}
	b.WriteString(" |")
	for i, p := range w.Phases {
		if i > 0 {
			b.WriteString(" ->")
		}
		fmt.Fprintf(&b, " %s", sanitizeDisplayData(p.Name))
		if p.Agent != "" {
			fmt.Fprintf(&b, "@%s", sanitizeDisplayData(p.Agent))
		}
		gate := p.Gate
		if gate == "" {
			gate = "agent"
		}
		fmt.Fprintf(&b, "(%s", gate)
		if p.GateCommand != "" {
			fmt.Fprintf(&b, ":%s", sanitizeDisplayData(truncateRunes(p.GateCommand, 24)))
		}
		b.WriteString(")")
		if grants := formatPhaseGrantsShort(p.Permissions); grants != "" {
			fmt.Fprintf(&b, "{%s}", grants)
		}
	}
	return b.String()
}

func formatPhaseGrantsShort(perms []host.WorkflowPermission) string {
	if len(perms) == 0 {
		return ""
	}
	parts := make([]string, 0, len(perms))
	for _, r := range perms {
		pat := r.Pattern
		if pat == "" {
			pat = "*"
		}
		parts = append(parts, fmt.Sprintf("%s %s %s", r.Action, r.Permission, pat))
		if len(parts) >= 4 {
			parts = append(parts, "...")
			break
		}
	}
	return strings.Join(parts, ", ")
}

func (m Model) workflowStart(name string) (tea.Model, tea.Cmd) {
	name = strings.TrimSpace(name)
	if m.turnRunning {
		m.setNotice("workflow: cannot start while a turn is running", true)
		return m, nil
	}
	w, ok := m.services.Workflows.Get(name)
	if !ok {
		m.setNotice(fmt.Sprintf("workflow: unknown %q - /workflow list", name), true)
		return m, nil
	}
	if !w.Valid {
		msg := fmt.Sprintf("workflow: %q is invalid and cannot be activated", name)
		if w.ValidationError != "" {
			msg += " - " + sanitizeDisplayData(w.ValidationError)
		}
		m.setNotice(msg, true)
		return m, nil
	}
	// Always show phase-0 permission grants before mutating engine state.
	m.clearNotice()
	m.modal = newWorkflowStartModal(w, m.ops, m.autonomy)
	return m, nil
}

func (m Model) workflowStop() (tea.Model, tea.Cmd) {
	if m.turnRunning {
		m.setNotice("workflow: cannot stop while a turn is running", true)
		return m, nil
	}
	if m.phaseWorkflow == "" && m.phaseName == "" {
		m.setNotice("workflow: no active workflow", false)
		return m, nil
	}
	m.clearNotice()
	ops := m.ops
	return m, func() tea.Msg {
		ops <- protocol.StopWorkflow{}
		return nil
	}
}

// workflowStartResultMsg is delivered after the start-confirm modal decides.
type workflowStartResultMsg struct {
	name     string
	canceled bool
	started  bool
}

func (m Model) handleWorkflowStartResult(msg workflowStartResultMsg) (tea.Model, tea.Cmd) {
	if msg.canceled {
		m.setNotice("workflow: start canceled", false)
		return m, nil
	}
	if msg.started {
		m.setNotice(fmt.Sprintf("workflow: starting %s...", sanitizeDisplayData(msg.name)), false)
	}
	return m, nil
}
