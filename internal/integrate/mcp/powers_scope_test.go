package mcp

import (
	"context"
	"testing"
)

// ScopedTool names the Powers-attached server a registry tool belongs to
// (#1943). Borrowed (configured) servers are never scoped, and a tool that
// appears before the manager records it falls back to the name prefix.
func TestPackConnectorScopedTool(t *testing.T) {
	recorded := map[string]string{"mcp_docs_search": "docs", "mcp_global_read": "global"}
	var c *PackConnector
	var midAttach map[string]bool
	c = newPackConnector(
		func(s string) (ServerConfig, bool) { return ServerConfig{Name: s}, true },
		func(_ context.Context, cfg ServerConfig) (bool, error) {
			if cfg.Name == "slow" {
				// During attach the tool is registered but not recorded.
				_, midAttach["slow"] = c.ScopedTool("mcp_slow_ping")
			}
			return cfg.Name != "global", nil
		},
		func(string) {},
	)
	c.toolServer = func(tool string) (string, bool) {
		srv, ok := recorded[tool]
		return srv, ok
	}
	midAttach = map[string]bool{}

	if _, ok := c.ScopedTool("mcp_docs_search"); ok {
		t.Fatal("nothing attached yet, but a tool was scoped")
	}
	for _, srv := range []string{"docs", "global", "slow"} {
		if err := c.Connect("s1", srv); err != nil {
			t.Fatal(err)
		}
	}
	if !midAttach["slow"] {
		t.Error("tool of a server mid-attach was not scoped (would leak to every session)")
	}

	tests := []struct {
		tool    string
		want    string
		wantHit bool
	}{
		{tool: "mcp_docs_search", want: "docs", wantHit: true},
		{tool: "mcp_global_read", wantHit: false}, // borrowed: configured outside Powers
		{tool: "mcp_slow_ping", want: "slow", wantHit: true},
		{tool: "read", wantHit: false},
	}
	for _, tt := range tests {
		srv, ok := c.ScopedTool(tt.tool)
		if ok != tt.wantHit || srv != tt.want {
			t.Errorf("ScopedTool(%q) = %q,%v want %q,%v", tt.tool, srv, ok, tt.want, tt.wantHit)
		}
	}

	c.Disconnect("s1", "docs")
	if _, ok := c.ScopedTool("mcp_docs_search"); ok {
		t.Error("tool still scoped after the last session released its server")
	}
}

func TestManagerToolServer(t *testing.T) {
	m := NewManager()
	m.tools["docs"] = []string{"mcp_docs_search"}
	if srv, ok := m.ToolServer("mcp_docs_search"); !ok || srv != "docs" {
		t.Fatalf("ToolServer = %q,%v", srv, ok)
	}
	if _, ok := m.ToolServer("mcp_other_x"); ok {
		t.Fatal("unknown tool mapped to a server")
	}
	var nilMgr *Manager
	if _, ok := nilMgr.ToolServer("x"); ok {
		t.Fatal("nil manager mapped a tool")
	}
}
