package local

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/host"
)

func writeTestPlugin(t *testing.T, root, id string, withMCP bool) {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	man := `{
  "$schema": "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
  "name": "` + id + `",
  "version": "1.0.0",
  "extensions": {
    "com.strike.cli": {
      "displayName": "Test ` + id + `"
    }
  }
}`
	if err := os.WriteFile(filepath.Join(root, "plugin.json"), []byte(man), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "skills", "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	skill := "---\ndescription: skill demo\n---\nDo demo $ARGUMENTS\n"
	if err := os.WriteFile(filepath.Join(root, "skills", "demo", "SKILL.md"), []byte(skill), 0o644); err != nil {
		t.Fatal(err)
	}
	if withMCP {
		if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "bin", "server"), []byte("#!/bin/sh\necho ok\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		mcp := `{
  "$schema": "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json",
  "mcpServers": {
    "demo": {
      "type": "stdio",
      "command": "./bin/server",
      "env": {"SECRET_TOKEN": "should-not-leak"}
    }
  }
}`
		if err := os.WriteFile(filepath.Join(root, "mcp.json"), []byte(mcp), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPluginsListEnableDisableRemove(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	global := filepath.Join(home, ".strike")
	project := filepath.Join(work, ".strike")
	src := filepath.Join(t.TempDir(), "src")
	writeTestPlugin(t, src, "acme.ui", false)

	p := NewPluginsForTest(work, global, project)
	res, err := p.Install(context.Background(), src, host.PluginScopeGlobal, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.ID != "acme.ui" || !res.Enabled {
		t.Fatalf("install: %+v", res)
	}

	list, err := p.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != "acme.ui" {
		t.Fatalf("list: %+v", list)
	}
	if list[0].Status != "enabled" {
		t.Fatalf("status = %q", list[0].Status)
	}
	if list[0].Format != "agent-plugins" {
		t.Fatalf("format = %q", list[0].Format)
	}
	if list[0].Name != "acme.ui" {
		t.Fatalf("APS name = %q", list[0].Name)
	}
	if list[0].DisplayName != "Test acme.ui" {
		t.Fatalf("displayName = %q", list[0].DisplayName)
	}
	if list[0].Skills != 1 {
		t.Fatalf("skills = %d", list[0].Skills)
	}

	if err := p.Disable("acme.ui", host.PluginScopeGlobal); err != nil {
		t.Fatal(err)
	}
	info, err := p.Inspect("acme.ui", host.PluginScopeGlobal)
	if err != nil {
		t.Fatal(err)
	}
	if info.Enabled || info.Status != "disabled" {
		t.Fatalf("after disable: %+v", info)
	}
	// Files preserved.
	if _, err := os.Stat(filepath.Join(global, "plugins", "acme.ui", "plugin.json")); err != nil {
		t.Fatal("disable removed files:", err)
	}

	if err := p.Enable("acme.ui", host.PluginScopeGlobal); err != nil {
		t.Fatal(err)
	}
	if err := p.Remove("acme.ui", host.PluginScopeGlobal, false); err == nil {
		t.Fatal("remove without confirm should fail")
	}
	if err := p.Remove("acme.ui", host.PluginScopeGlobal, true); err != nil {
		t.Fatal(err)
	}
	list, err = p.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("after remove: %+v", list)
	}
}

func TestPluginsTrustPreviewNoSecrets(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	global := filepath.Join(home, ".strike")
	project := filepath.Join(work, ".strike")
	src := filepath.Join(t.TempDir(), "src")
	writeTestPlugin(t, src, "acme.exec", true)

	p := NewPluginsForTest(work, global, project)
	if _, err := p.Install(context.Background(), src, host.PluginScopeGlobal, ""); err != nil {
		t.Fatal(err)
	}

	prev, err := p.TrustPreview("acme.exec", host.PluginScopeGlobal)
	if err != nil {
		t.Fatal(err)
	}
	if prev.ID != "acme.exec" || len(prev.MCP) != 1 {
		t.Fatalf("preview: %+v", prev)
	}
	if prev.MCP[0].Name != "demo" || prev.MCP[0].Command != "./bin/server" {
		t.Fatalf("mcp: %+v", prev.MCP[0])
	}
	// Env values must never appear — keys only.
	joined := strings.Join(prev.ReviewLines, "\n")
	if strings.Contains(joined, "should-not-leak") {
		t.Fatalf("secret leaked in review:\n%s", joined)
	}
	if !strings.Contains(joined, "SECRET_TOKEN") {
		t.Fatalf("expected env key in review:\n%s", joined)
	}
	if !strings.Contains(joined, "command: ./bin/server") {
		t.Fatalf("expected command in review:\n%s", joined)
	}
	if !strings.Contains(joined, "mcp") {
		t.Fatalf("expected contribution type mcp:\n%s", joined)
	}

	if err := p.Trust("acme.exec", host.PluginScopeGlobal); err != nil {
		t.Fatal(err)
	}
	info, err := p.Inspect("acme.exec", host.PluginScopeGlobal)
	if err != nil {
		t.Fatal(err)
	}
	if info.TrustState != host.PluginTrustTrusted {
		t.Fatalf("trust state = %q", info.TrustState)
	}
	if err := p.Untrust("acme.exec", host.PluginScopeGlobal); err != nil {
		t.Fatal(err)
	}
}

func TestPluginsFailedInstallPreservesNothing(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	global := filepath.Join(home, ".strike")
	project := filepath.Join(work, ".strike")
	bad := filepath.Join(t.TempDir(), "bad")
	if err := os.MkdirAll(bad, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, "plugin.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}

	p := NewPluginsForTest(work, global, project)
	_, err := p.Install(context.Background(), bad, host.PluginScopeGlobal, "")
	if err == nil {
		t.Fatal("expected install failure")
	}
	list, err := p.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("failed install left plugins: %+v", list)
	}
}
