package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

func (m Model) handleLSPCommand(args []string) (tea.Model, tea.Cmd) {
	m.resetComposer()
	if m.services.LSP == nil {
		m.setNotice("no language servers configured (add lsp.servers in config)", false)
		return m, nil
	}
	if len(args) == 0 {
		return m.lspStatusNotice()
	}
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "retry":
		name := ""
		if len(args) > 1 {
			name = strings.TrimSpace(args[1])
		}
		if err := m.services.LSP.Retry(name); err != nil {
			m.setNotice(err.Error(), true)
			return m, nil
		}
		if name == "" {
			m.setNotice("lsp: retry complete", false)
		} else {
			m.setNotice(fmt.Sprintf("lsp: retried %s", name), false)
		}
		return m, nil
	case "disable":
		if len(args) < 2 || strings.TrimSpace(args[1]) == "" {
			m.setNotice("usage: /lsp disable <name>", true)
			return m, nil
		}
		name := strings.TrimSpace(args[1])
		if err := m.services.LSP.Disable(name); err != nil {
			m.setNotice(err.Error(), true)
			return m, nil
		}
		m.setNotice(fmt.Sprintf("lsp: disabled %s", name), false)
		return m, nil
	case "status", "list":
		return m.lspStatusNotice()
	default:
		m.setNotice("usage: /lsp [retry [name]|disable <name>]", true)
		return m, nil
	}
}

func (m Model) lspStatusNotice() (tea.Model, tea.Cmd) {
	statuses := m.services.LSP.Statuses()
	if len(statuses) == 0 {
		m.setNotice("no language servers configured (add lsp.servers in config)", false)
		return m, nil
	}
	var b strings.Builder
	for i, st := range statuses {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%s  %s  %s", st.Name, st.State, st.Command)
		if len(st.Extensions) > 0 {
			fmt.Fprintf(&b, "  %s", strings.Join(st.Extensions, ","))
		}
		if st.OpenDocs > 0 {
			fmt.Fprintf(&b, "  docs=%d", st.OpenDocs)
		}
		if st.Error != "" && st.State != "disabled" {
			fmt.Fprintf(&b, "  (%s)", st.Error)
		}
	}
	b.WriteString("\n(/lsp retry [name]  |  /lsp disable <name>  |  /diagnostics)")
	m.setNotice(b.String(), false)
	return m, nil
}
