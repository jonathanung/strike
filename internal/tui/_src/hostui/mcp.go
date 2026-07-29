package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

func (m Model) handleMCPCommand(args []string) (tea.Model, tea.Cmd) {
	m.resetComposer()
	if m.services.MCP == nil {
		m.setNotice("no MCP servers configured (add servers in ~/.strike/mcp.jsonc)", false)
		return m, nil
	}
	if len(args) == 0 {
		return m.mcpStatusNotice()
	}
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "retry":
		name := ""
		if len(args) > 1 {
			name = strings.TrimSpace(args[1])
		}
		if err := m.services.MCP.Retry(name); err != nil {
			m.setNotice(err.Error(), true)
			return m, nil
		}
		if name == "" {
			m.setNotice("mcp: retry complete", false)
		} else {
			m.setNotice(fmt.Sprintf("mcp: retried %s", name), false)
		}
		return m, nil
	case "disable":
		if len(args) < 2 || strings.TrimSpace(args[1]) == "" {
			m.setNotice("usage: /mcp disable <name>", true)
			return m, nil
		}
		name := strings.TrimSpace(args[1])
		if err := m.services.MCP.Disable(name); err != nil {
			m.setNotice(err.Error(), true)
			return m, nil
		}
		m.setNotice(fmt.Sprintf("mcp: disabled %s", name), false)
		return m, nil
	case "status", "list":
		return m.mcpStatusNotice()
	default:
		m.setNotice("usage: /mcp [retry [name]|disable <name>]", true)
		return m, nil
	}
}

func (m Model) mcpStatusNotice() (tea.Model, tea.Cmd) {
	statuses := m.services.MCP.Statuses()
	if len(statuses) == 0 {
		m.setNotice("no MCP servers configured (add servers in ~/.strike/mcp.jsonc)", false)
		return m, nil
	}
	var b strings.Builder
	for i, st := range statuses {
		if i > 0 {
			b.WriteByte('\n')
		}
		transport := st.Transport
		if transport == "" {
			transport = "stdio"
		}
		fmt.Fprintf(&b, "%s  %s  %s  tools=%d", st.Name, st.State, transport, st.ToolCount)
		if st.Command != "" {
			fmt.Fprintf(&b, "  %s", st.Command)
		}
		if st.Error != "" && st.State != "disabled" {
			fmt.Fprintf(&b, "  (%s)", st.Error)
		}
		if len(st.Tools) > 0 {
			b.WriteByte('\n')
			b.WriteString("  ")
			b.WriteString(strings.Join(st.Tools, ", "))
		}
	}
	b.WriteString("\n(/mcp retry [name]  |  /mcp disable <name>)")
	m.setNotice(b.String(), false)
	return m, nil
}
