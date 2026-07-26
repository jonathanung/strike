package local

import (
	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/mcp"
)

// NewMCP adapts an mcp.Manager to host.MCP. A nil manager yields a nil host.MCP.
func NewMCP(mgr *mcp.Manager) host.MCP {
	if mgr == nil {
		return nil
	}
	return mcpAdapter{mgr: mgr}
}

type mcpAdapter struct {
	mgr *mcp.Manager
}

func (a mcpAdapter) Statuses() []host.MCPServerStatus {
	raw := a.mgr.Statuses()
	out := make([]host.MCPServerStatus, len(raw))
	for i, s := range raw {
		out[i] = host.MCPServerStatus{
			Name:      s.Name,
			Command:   s.Command,
			State:     s.State,
			ToolCount: s.ToolCount,
			Error:     s.Error,
			Tools:     append([]string(nil), s.Tools...),
		}
	}
	return out
}
