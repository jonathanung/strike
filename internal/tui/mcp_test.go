package tui

import (
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/host"
)

type fakeMCP struct {
	statuses []host.MCPServerStatus
}

func (f fakeMCP) Statuses() []host.MCPServerStatus { return f.statuses }

func TestMCPCommandListsServers(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.services.MCP = fakeMCP{statuses: []host.MCPServerStatus{
		{Name: "github", State: "up", ToolCount: 2, Command: "npx", Tools: []string{"mcp_github_a", "mcp_github_b"}},
	}}
	next, _ := m.handleCommand("/mcp")
	nm := next.(Model)
	if !strings.Contains(nm.notice, "github") || !strings.Contains(nm.notice, "up") {
		t.Fatalf("notice = %q", nm.notice)
	}
	if !strings.Contains(nm.notice, "mcp_github_a") {
		t.Fatalf("notice missing tools: %q", nm.notice)
	}
}

func TestMCPCommandEmpty(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.services.MCP = nil
	next, _ := m.handleCommand("/mcp")
	nm := next.(Model)
	if !strings.Contains(nm.notice, "no MCP") {
		t.Fatalf("notice = %q", nm.notice)
	}
}
