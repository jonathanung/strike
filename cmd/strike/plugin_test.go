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

func TestRunCLIDispatchesPlugin(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := runCLI([]string{"plugin", "help"}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("code=%d err=%s", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "Manage local and Git plugin") {
		t.Fatalf("out=%s", out.String())
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
