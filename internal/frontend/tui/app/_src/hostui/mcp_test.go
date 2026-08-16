package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/frontend/host"
)

type fakeMCP struct {
	statuses   []host.MCPServerStatus
	retryErr   error
	disableErr error
	retried    []string
	disabled   []string
}

func (f *fakeMCP) Statuses() []host.MCPServerStatus { return f.statuses }

func (f *fakeMCP) Retry(name string) error {
	f.retried = append(f.retried, name)
	return f.retryErr
}

func (f *fakeMCP) Disable(name string) error {
	f.disabled = append(f.disabled, name)
	return f.disableErr
}

func TestMCPCommandListsServers(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.services.MCP = &fakeMCP{statuses: []host.MCPServerStatus{
		{Name: "github", State: "up", Transport: "stdio", ToolCount: 2, Command: "npx", Tools: []string{"mcp_github_a", "mcp_github_b"}},
		{Name: "remote", State: "down", Transport: "http", ToolCount: 0, Command: "https://example.com/mcp", Error: "connection refused"},
	}}
	next, _ := m.handleCommand("/mcp")
	nm := next.(Model)
	if !strings.Contains(nm.notice, "github") || !strings.Contains(nm.notice, "up") {
		t.Fatalf("notice = %q", nm.notice)
	}
	if !strings.Contains(nm.notice, "mcp_github_a") {
		t.Fatalf("notice missing tools: %q", nm.notice)
	}
	if !strings.Contains(nm.notice, "http") || !strings.Contains(nm.notice, "connection refused") {
		t.Fatalf("notice missing remote status: %q", nm.notice)
	}
	if !strings.Contains(nm.notice, "/mcp retry") {
		t.Fatalf("notice missing retry hint: %q", nm.notice)
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

func TestMCPCommandRetryDisable(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	fake := &fakeMCP{statuses: []host.MCPServerStatus{
		{Name: "github", State: "down", Transport: "stdio"},
	}}
	m.services.MCP = fake

	next, _ := m.handleCommand("/mcp retry github")
	nm := next.(Model)
	if len(fake.retried) != 1 || fake.retried[0] != "github" {
		t.Fatalf("retried = %v notice=%q", fake.retried, nm.notice)
	}
	if !strings.Contains(nm.notice, "retried github") {
		t.Fatalf("notice = %q", nm.notice)
	}

	next, _ = nm.handleCommand("/mcp disable github")
	nm = next.(Model)
	if len(fake.disabled) != 1 || fake.disabled[0] != "github" {
		t.Fatalf("disabled = %v", fake.disabled)
	}
	if !strings.Contains(nm.notice, "disabled github") {
		t.Fatalf("notice = %q", nm.notice)
	}

	fake.disableErr = fmt.Errorf("mcp: unknown server")
	next, _ = nm.handleCommand("/mcp disable missing")
	nm = next.(Model)
	if !nm.noticeErr || !strings.Contains(nm.notice, "unknown") {
		t.Fatalf("notice = %q err=%v", nm.notice, nm.noticeErr)
	}
}

func TestMCPCommandUsage(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.services.MCP = &fakeMCP{statuses: []host.MCPServerStatus{{Name: "a", State: "up"}}}
	next, _ := m.handleCommand("/mcp disable")
	nm := next.(Model)
	if !strings.Contains(nm.notice, "usage:") {
		t.Fatalf("notice = %q", nm.notice)
	}
}
