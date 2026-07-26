package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

func (m Model) handleMCPCommand() (tea.Model, tea.Cmd) {
	m.resetComposer()
	if m.services.MCP == nil {
		m.setNotice("no MCP servers configured (add mcp.servers in config)", false)
		return m, nil
	}
	statuses := m.services.MCP.Statuses()
	if len(statuses) == 0 {
		m.setNotice("no MCP servers configured (add mcp.servers in config)", false)
		return m, nil
	}
	var b strings.Builder
	for i, st := range statuses {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%s  %s  tools=%d", st.Name, st.State, st.ToolCount)
		if st.Command != "" {
			fmt.Fprintf(&b, "  %s", st.Command)
		}
		if st.Error != "" {
			fmt.Fprintf(&b, "  (%s)", st.Error)
		}
		if len(st.Tools) > 0 {
			b.WriteByte('\n')
			b.WriteString("  ")
			b.WriteString(strings.Join(st.Tools, ", "))
		}
	}
	// Multi-line status via notice lines path: collapse to readable single block.
	m.setNotice(b.String(), false)
	return m, nil
}
