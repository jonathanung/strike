package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTestPluginBundle(t *testing.T, dir, id string) string {
	t.Helper()
	root := filepath.Join(dir, id)
	if err := os.MkdirAll(filepath.Join(root, "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	man := `{
  "schemaVersion": 1,
  "id": "` + id + `",
  "version": "1.0.0",
  "name": "Test Pack",
  "strike": { "min": "0.1.0" },
  "contributions": { "agents": [{ "path": "agents/a.md" }] }
}`
	if err := os.WriteFile(filepath.Join(root, "plugin.json"), []byte(man), 0o644); err != nil {
		t.Fatal(err)
	}
	agent := "---\ndescription: test agent " + id + "\n---\nYou are " + id + ".\n"
	if err := os.WriteFile(filepath.Join(root, "agents", "a.md"), []byte(agent), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestRunPluginCLIHelp(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := runPluginCLI([]string{"help"}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("code=%d err=%s", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "strike plugin install") {
		t.Fatalf("usage: %s", out.String())
	}
}

func TestRunPluginLifecycleCLI(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Ensure defaultGlobalRoot picks up HOME.
	work := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(work); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	src := writeTestPluginBundle(t, t.TempDir(), "acme.cli")

	var out, errBuf bytes.Buffer
	code := runPluginCLI([]string{"install", src, "--scope", "global"}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("install: code=%d err=%s out=%s", code, errBuf.String(), out.String())
	}
	if !strings.Contains(out.String(), "Installed acme.cli@1.0.0") {
		t.Fatalf("install out: %s", out.String())
	}

	out.Reset()
	errBuf.Reset()
	code = runPluginCLI([]string{"list"}, &out, &errBuf)
	if code != 0 || !strings.Contains(out.String(), "acme.cli") {
		t.Fatalf("list: code=%d out=%s err=%s", code, out.String(), errBuf.String())
	}

	out.Reset()
	errBuf.Reset()
	code = runPluginCLI([]string{"inspect", "acme.cli"}, &out, &errBuf)
	if code != 0 || !strings.Contains(out.String(), "digest:") {
		t.Fatalf("inspect: code=%d out=%s err=%s", code, out.String(), errBuf.String())
	}

	out.Reset()
	errBuf.Reset()
	code = runPluginCLI([]string{"disable", "acme.cli"}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("disable: %s", errBuf.String())
	}

	out.Reset()
	errBuf.Reset()
	code = runPluginCLI([]string{"enable", "acme.cli"}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("enable: %s", errBuf.String())
	}

	out.Reset()
	errBuf.Reset()
	code = runPluginCLI([]string{"doctor", "acme.cli"}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("doctor: code=%d err=%s out=%s", code, errBuf.String(), out.String())
	}
	if !strings.Contains(out.String(), "root:") {
		t.Fatalf("doctor out: %s", out.String())
	}

	out.Reset()
	errBuf.Reset()
	code = runPluginCLI([]string{"remove", "acme.cli"}, &out, &errBuf)
	if code == 0 {
		t.Fatal("remove without --yes should fail")
	}

	out.Reset()
	errBuf.Reset()
	code = runPluginCLI([]string{"remove", "acme.cli", "--yes"}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("remove: %s", errBuf.String())
	}
}

func TestRunPluginInspectAPS(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(work); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	src := filepath.Join(t.TempDir(), "acme.skills")
	if err := os.MkdirAll(filepath.Join(src, "skills", "foo"), 0o755); err != nil {
		t.Fatal(err)
	}
	man := `{
  "$schema": "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
  "name": "acme.skills",
  "version": "1.0.0"
}`
	if err := os.WriteFile(filepath.Join(src, "plugin.json"), []byte(man), 0o644); err != nil {
		t.Fatal(err)
	}
	skill := "---\ndescription: skill foo\n---\nDo foo $ARGUMENTS\n"
	if err := os.WriteFile(filepath.Join(src, "skills", "foo", "SKILL.md"), []byte(skill), 0o644); err != nil {
		t.Fatal(err)
	}
	mcp := `{
  "$schema": "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json",
  "mcpServers": {
    "lint": { "type": "stdio", "command": "echo" },
    "cloud": { "type": "streamable-http", "url": "https://mcp.example.com/acme" }
  }
}`
	if err := os.WriteFile(filepath.Join(src, "mcp.json"), []byte(mcp), 0o644); err != nil {
		t.Fatal(err)
	}

	var out, errBuf bytes.Buffer
	code := runPluginCLI([]string{"install", src, "--scope", "global"}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("install: code=%d err=%s out=%s", code, errBuf.String(), out.String())
	}

	out.Reset()
	errBuf.Reset()
	code = runPluginCLI([]string{"inspect", "acme.skills"}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("inspect: code=%d err=%s out=%s", code, errBuf.String(), out.String())
	}
	got := out.String()
	if !strings.Contains(got, "format:   aps") {
		t.Fatalf("missing APS format:\n%s", got)
	}
	if !strings.Contains(got, "skills=1") || !strings.Contains(got, "mcp=2") {
		t.Fatalf("want skills=1 mcp=2:\n%s", got)
	}
}

func TestRunPluginInspectAPSStrikeCLI(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(work); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	src := filepath.Join(t.TempDir(), "acme.ext")
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(src, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("plugin.json", `{
  "$schema": "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
  "name": "acme.ext",
  "version": "1.0.0"
}`)
	write("com.strike.cli/agents/reviewer.md", "---\ndescription: r\n---\nReview.\n")
	write("com.strike.cli/harnesses/choose.json", `{"name":"acme-choose","command":"com.strike.cli/bin/choose"}`)
	write("com.strike.cli/hooks/pre.json", `{"event":"pre_tool_use","type":"command","command":"com.strike.cli/bin/hook.sh"}`)
	write("com.strike.cli/panes/status.json", `{
  "schemaVersion": 1, "id": "acme.status", "title": "S", "mode": "static",
  "permissions": {"host": [], "fs": "none", "network": "none", "command": "none"},
  "view": {"type": "text", "text": "hi"}
}`)
	write("com.strike.cli/bin/choose", "#!/bin/sh\n")
	write("com.strike.cli/bin/hook.sh", "#!/bin/sh\n")

	var out, errBuf bytes.Buffer
	code := runPluginCLI([]string{"install", src, "--scope", "global"}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("install: code=%d err=%s out=%s", code, errBuf.String(), out.String())
	}
	out.Reset()
	errBuf.Reset()
	code = runPluginCLI([]string{"inspect", "acme.ext"}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("inspect: code=%d err=%s out=%s", code, errBuf.String(), out.String())
	}
	got := out.String()
	if !strings.Contains(got, "agents=1") || !strings.Contains(got, "harnesses=1") ||
		!strings.Contains(got, "hooks=1") || !strings.Contains(got, "panes=1") {
		t.Fatalf("want APS Strike-only contrib counts:\n%s", got)
	}
}

func TestRunPluginTrustCLI(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(work); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "bin", "mcp.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	man := `{
  "schemaVersion": 1,
  "id": "acme.exec",
  "version": "1.0.0",
  "name": "Exec",
  "strike": { "min": "0.1.0" },
  "contributions": {
    "mcp": [{ "name": "x", "transport": "stdio", "command": "bin/mcp.sh" }]
  }
}`
	if err := os.WriteFile(filepath.Join(src, "plugin.json"), []byte(man), 0o644); err != nil {
		t.Fatal(err)
	}

	var out, errBuf bytes.Buffer
	code := runPluginCLI([]string{"install", src, "--scope", "global"}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("install: %s %s", errBuf.String(), out.String())
	}

	// Passive-only trust should fail.
	out.Reset()
	errBuf.Reset()
	// Install a passive-only plugin and ensure trust fails.
	passive := writeTestPluginBundle(t, t.TempDir(), "acme.passive")
	code = runPluginCLI([]string{"install", passive, "--scope", "global"}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("install passive: %s", errBuf.String())
	}
	out.Reset()
	errBuf.Reset()
	code = runPluginCLI([]string{"trust", "acme.passive"}, &out, &errBuf)
	if code == 0 {
		t.Fatal("trust on passive-only should fail")
	}

	out.Reset()
	errBuf.Reset()
	code = runPluginCLI([]string{"trust", "acme.exec"}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("trust: code=%d err=%s out=%s", code, errBuf.String(), out.String())
	}
	if !strings.Contains(out.String(), "Trusted acme.exec") {
		t.Fatalf("trust out: %s", out.String())
	}

	out.Reset()
	errBuf.Reset()
	code = runPluginCLI([]string{"inspect", "acme.exec"}, &out, &errBuf)
	if code != 0 || !strings.Contains(out.String(), "trust:") || !strings.Contains(out.String(), "trusted") {
		t.Fatalf("inspect trust: out=%s err=%s", out.String(), errBuf.String())
	}

	out.Reset()
	errBuf.Reset()
	code = runPluginCLI([]string{"untrust", "acme.exec"}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("untrust: %s", errBuf.String())
	}
}

func TestRunCLIDispatchesPlugin(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := runCLI([]string{"plugin", "help"}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("code=%d err=%s", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "Manage plugin installs") {
		t.Fatalf("out=%s", out.String())
	}
}

func TestRunPluginSearchRequiresRegistry(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := runPluginCLI([]string{"search", "acme"}, &out, &errBuf)
	if code == 0 {
		t.Fatal("expected failure without --registry")
	}
}

func TestRunPluginInstallInvalid(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	bad := t.TempDir()
	if err := os.WriteFile(filepath.Join(bad, "plugin.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errBuf bytes.Buffer
	code := runPluginCLI([]string{"install", bad}, &out, &errBuf)
	if code == 0 {
		t.Fatal("expected failure")
	}
	// Nothing enabled under home plugins.
	plugins := filepath.Join(home, ".strike", "plugins")
	if entries, err := os.ReadDir(plugins); err == nil && len(entries) > 0 {
		for _, e := range entries {
			if !strings.HasPrefix(e.Name(), ".") {
				t.Fatalf("partial install left %s", e.Name())
			}
		}
	}
}

func chdirTemp(t *testing.T) {
	t.Helper()
	work := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(work); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
}

func TestRunPluginMigrateCLI(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	chdirTemp(t)
	src := writeTestPluginBundle(t, t.TempDir(), "acme.migrate")

	var out, errBuf bytes.Buffer
	code := runPluginCLI([]string{"install", src, "--scope", "global"}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("install: %s %s", errBuf.String(), out.String())
	}

	out.Reset()
	errBuf.Reset()
	code = runPluginCLI([]string{"migrate", "acme.migrate", "--dry-run"}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("dry-run: code=%d err=%s out=%s", code, errBuf.String(), out.String())
	}
	if !strings.Contains(out.String(), "legacy → Agent Plugins") || !strings.Contains(out.String(), "dry-run") {
		t.Fatalf("dry-run out: %s", out.String())
	}

	out.Reset()
	errBuf.Reset()
	code = runPluginCLI([]string{"inspect", "acme.migrate"}, &out, &errBuf)
	if code != 0 || !strings.Contains(out.String(), "format:   legacy") {
		t.Fatalf("dry-run must leave legacy: %s %s", out.String(), errBuf.String())
	}

	out.Reset()
	errBuf.Reset()
	code = runPluginCLI([]string{"migrate", "acme.migrate"}, &out, &errBuf)
	if code == 0 {
		t.Fatal("installed migrate without --yes should fail")
	}
	if !strings.Contains(errBuf.String(), "--yes") {
		t.Fatalf("want --yes hint, got %s / %s", errBuf.String(), out.String())
	}

	out.Reset()
	errBuf.Reset()
	code = runPluginCLI([]string{"migrate", "acme.migrate", "--yes"}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("migrate: code=%d err=%s out=%s", code, errBuf.String(), out.String())
	}
	if !strings.Contains(out.String(), "Migrated acme.migrate") {
		t.Fatalf("success out: %s", out.String())
	}

	out.Reset()
	errBuf.Reset()
	code = runPluginCLI([]string{"inspect", "acme.migrate"}, &out, &errBuf)
	if code != 0 || !strings.Contains(out.String(), "format:   aps") {
		t.Fatalf("inspect after migrate: %s %s", out.String(), errBuf.String())
	}

	out.Reset()
	errBuf.Reset()
	code = runPluginCLI([]string{"doctor", "acme.migrate"}, &out, &errBuf)
	if code != 0 || !strings.Contains(out.String(), "format:    aps") {
		t.Fatalf("doctor after migrate: %s %s", out.String(), errBuf.String())
	}

	out.Reset()
	errBuf.Reset()
	code = runPluginCLI([]string{"migrate", "acme.migrate", "--yes"}, &out, &errBuf)
	if code == 0 {
		t.Fatal("already-APS should refuse")
	}
	if !strings.Contains(errBuf.String(), "already an Agent Plugins") {
		t.Fatalf("already-APS message: %s / %s", errBuf.String(), out.String())
	}
}

func TestRunPluginMigrateCLIRollback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	chdirTemp(t)

	// Valid Strike id, too long for APS name — convert fails before replace.
	id := "acme." + strings.Repeat("z", 60)
	src := writeTestPluginBundle(t, t.TempDir(), id)
	var out, errBuf bytes.Buffer
	code := runPluginCLI([]string{"install", src, "--scope", "global"}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("install: %s %s", errBuf.String(), out.String())
	}

	out.Reset()
	errBuf.Reset()
	code = runPluginCLI([]string{"migrate", id, "--yes"}, &out, &errBuf)
	if code == 0 {
		t.Fatal("expected migrate failure")
	}

	out.Reset()
	errBuf.Reset()
	code = runPluginCLI([]string{"inspect", id}, &out, &errBuf)
	if code != 0 || !strings.Contains(out.String(), "format:   legacy") {
		t.Fatalf("failed migrate must leave legacy enabled: %s %s", out.String(), errBuf.String())
	}
}
