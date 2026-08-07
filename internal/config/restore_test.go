package config_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/config"
)

func TestRestoreCreatesGlobalLayout(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".strike")
	res, err := config.Restore(config.RestoreOptions{Root: root, Kind: config.RestoreGlobal})
	if err != nil {
		t.Fatal(err)
	}
	if res.Root != root {
		// Abs may normalize; compare base existence.
		if _, err := os.Stat(root); err != nil {
			t.Fatalf("root missing: %v", err)
		}
	}
	for _, name := range []string{
		"agents", "skills", "sessions", "checkpoints", "history", "memory", "issues",
		"goals", "cache", "themes", "workflows", "bin",
	} {
		fi, err := os.Stat(filepath.Join(root, name))
		if err != nil || !fi.IsDir() {
			t.Fatalf("dir %s: err=%v fi=%v", name, err, fi)
		}
	}
	cfg, err := os.ReadFile(filepath.Join(root, "config"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(cfg, &m); err != nil {
		t.Fatalf("config JSON: %v\n%s", err, cfg)
	}
	if m["provider"] != "anthropic" || m["defaultAgent"] != "build" {
		t.Fatalf("config = %#v", m)
	}
	if _, err := os.Stat(filepath.Join(root, "skills", "commit.md")); err != nil {
		t.Fatalf("commit skill: %v", err)
	}
	// Optional sidecars must not be invented.
	for _, name := range []string{"mcp.jsonc", "providers.jsonc", "keybinds.jsonc", "auth.json"} {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("%s should be absent, err=%v", name, err)
		}
	}
	report := config.FormatRestoreReport(res)
	if !strings.Contains(report, "created") {
		t.Fatalf("report = %q", report)
	}
}

func TestRestoreIdempotentKeepsValidFiles(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".strike")
	if _, err := config.Restore(config.RestoreOptions{Root: root, Kind: config.RestoreGlobal}); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(root, "config")
	custom := []byte(`{"provider":"echo","defaultAgent":"plan"}`)
	if err := os.WriteFile(cfgPath, custom, 0o644); err != nil {
		t.Fatal(err)
	}
	// Seed a valid optional sidecar.
	mcpPath := filepath.Join(root, "mcp.jsonc")
	if err := os.WriteFile(mcpPath, []byte(`{"servers":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Session data must survive.
	sess := filepath.Join(root, "sessions", "keep.jsonl")
	if err := os.WriteFile(sess, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := config.Restore(config.RestoreOptions{Root: root, Kind: config.RestoreGlobal})
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(custom) {
		t.Fatalf("config overwritten: %s", got)
	}
	gotMCP, _ := os.ReadFile(mcpPath)
	if string(gotMCP) != `{"servers":{}}` {
		t.Fatalf("mcp overwritten: %s", gotMCP)
	}
	if _, err := os.Stat(sess); err != nil {
		t.Fatalf("session lost: %v", err)
	}
	var quarantined int
	for _, a := range res.Actions {
		if a.Op == "quarantined" {
			quarantined++
		}
	}
	if quarantined != 0 {
		t.Fatalf("unexpected quarantine: %+v", res.Actions)
	}
}

func TestRestoreQuarantinesCorruptConfigAndSidecars(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".strike")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	fixed := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	now := func() time.Time { return fixed }

	cfgPath := filepath.Join(root, "config")
	if err := os.WriteFile(cfgPath, []byte(`{not json`), 0o644); err != nil {
		t.Fatal(err)
	}
	mcpPath := filepath.Join(root, "mcp.jsonc")
	if err := os.WriteFile(mcpPath, []byte("// broken\n{"), 0o644); err != nil {
		t.Fatal(err)
	}
	authPath := filepath.Join(root, "auth.json")
	if err := os.WriteFile(authPath, []byte(`not-json`), 0o600); err != nil {
		t.Fatal(err)
	}
	// Valid JSONC with comments must be kept.
	kbPath := filepath.Join(root, "keybinds.jsonc")
	if err := os.WriteFile(kbPath, []byte("{\n  // comment\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := config.Restore(config.RestoreOptions{
		Root: root,
		Kind: config.RestoreGlobal,
		Now:  now,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Config replaced with defaults.
	cfg, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(cfg, &m); err != nil {
		t.Fatalf("new config: %v", err)
	}
	if m["provider"] != "anthropic" {
		t.Fatalf("config = %#v", m)
	}
	backupCfg := cfgPath + ".corrupt-20260729-120000"
	if _, err := os.Stat(backupCfg); err != nil {
		t.Fatalf("config backup missing: %v\nactions=%+v", err, res.Actions)
	}
	// mcp quarantined, not recreated.
	if _, err := os.Stat(mcpPath); !os.IsNotExist(err) {
		t.Fatalf("corrupt mcp should be gone, err=%v", err)
	}
	if _, err := os.Stat(mcpPath + ".corrupt-20260729-120000"); err != nil {
		t.Fatalf("mcp backup: %v", err)
	}
	// auth quarantined, not recreated.
	if _, err := os.Stat(authPath); !os.IsNotExist(err) {
		t.Fatalf("corrupt auth should be gone, err=%v", err)
	}
	// keybinds kept.
	if _, err := os.Stat(kbPath); err != nil {
		t.Fatalf("valid keybinds lost: %v", err)
	}
}

func TestRestoreThenLoadSucceeds(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	realRoot := filepath.Join(home, ".strike")
	if err := os.MkdirAll(realRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realRoot, "config"), []byte(`{bad`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realRoot, "providers.jsonc"), []byte(`not-json`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := config.RestoreGlobalHome(); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(t.TempDir()); err != nil {
		t.Fatalf("Load after restore: %v", err)
	}
}

func TestRestoreProjectLayout(t *testing.T) {
	work := t.TempDir()
	res, err := config.RestoreProjectDir(work)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(work, ".strike")
	if res.Root != root {
		// Abs path may differ by symlink; check dirs under work/.strike.
		if _, err := os.Stat(root); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"agents", "skills", "themes", "workflows", "worktrees", "exports"} {
		if fi, err := os.Stat(filepath.Join(root, name)); err != nil || !fi.IsDir() {
			t.Fatalf("project dir %s: %v", name, err)
		}
	}
	// Project must not create global-only dirs or invent optional config.
	for _, name := range []string{"sessions", "history", "bin", "auth.json", "config"} {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("project should not have %s", name)
		}
	}
	// No starter skill on project.
	if _, err := os.Stat(filepath.Join(root, "skills", "commit.md")); !os.IsNotExist(err) {
		t.Fatal("project should not get commit skill")
	}
}

func TestRestoreQuarantinesEmptyConfig(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".strike")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(root, "config")
	if err := os.WriteFile(cfgPath, []byte("   \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fixed := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	if _, err := config.Restore(config.RestoreOptions{
		Root: root,
		Kind: config.RestoreGlobal,
		Now:  func() time.Time { return fixed },
	}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(data) {
		t.Fatalf("empty config not replaced: %q", data)
	}
	if _, err := os.Stat(cfgPath + ".corrupt-20260304-050607"); err != nil {
		t.Fatalf("empty config backup: %v", err)
	}
}

func TestRestoreProjectQuarantinesCorruptConfigWithoutRewrite(t *testing.T) {
	work := t.TempDir()
	root := filepath.Join(work, ".strike")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(root, "config")
	if err := os.WriteFile(cfgPath, []byte("{bad"), 0o644); err != nil {
		t.Fatal(err)
	}
	fixed := time.Date(2026, 4, 5, 6, 7, 8, 0, time.UTC)
	if _, err := config.Restore(config.RestoreOptions{
		Root: root,
		Kind: config.RestoreProject,
		Now:  func() time.Time { return fixed },
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cfgPath); !os.IsNotExist(err) {
		t.Fatalf("project config should stay absent after quarantine, err=%v", err)
	}
	if _, err := os.Stat(cfgPath + ".corrupt-20260405-060708"); err != nil {
		t.Fatalf("backup: %v", err)
	}
}

func TestRestoreQuarantinesFileWhereDirExpected(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".strike")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	fixed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	agentsFile := filepath.Join(root, "agents")
	if err := os.WriteFile(agentsFile, []byte("oops"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := config.Restore(config.RestoreOptions{
		Root: root,
		Kind: config.RestoreGlobal,
		Now:  func() time.Time { return fixed },
	})
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(agentsFile)
	if err != nil || !fi.IsDir() {
		t.Fatalf("agents should be dir: err=%v fi=%v", err, fi)
	}
	if _, err := os.Stat(agentsFile + ".corrupt-20260102-030405"); err != nil {
		t.Fatalf("backup: %v", err)
	}
}

func TestRestoreRejectsRootFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := config.Restore(config.RestoreOptions{Root: path, Kind: config.RestoreGlobal})
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("err = %v", err)
	}
}

func TestRestoreRequiresRoot(t *testing.T) {
	_, err := config.Restore(config.RestoreOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRestoreGlobalHomeUsesHOME(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	res, err := config.RestoreGlobalHome()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".strike")
	if _, err := os.Stat(filepath.Join(want, "config")); err != nil {
		t.Fatalf("global home restore: root=%s err=%v", res.Root, err)
	}
}

func TestFormatRestoreReportNothingToFix(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".strike")
	if _, err := config.Restore(config.RestoreOptions{Root: root, Kind: config.RestoreGlobal}); err != nil {
		t.Fatal(err)
	}
	res, err := config.Restore(config.RestoreOptions{Root: root, Kind: config.RestoreGlobal})
	if err != nil {
		t.Fatal(err)
	}
	// Second pass: only kept actions (and maybe no created).
	var created int
	for _, a := range res.Actions {
		if a.Op == "created" || a.Op == "quarantined" {
			created++
		}
	}
	report := config.FormatRestoreReport(res)
	if created == 0 && !strings.Contains(report, "nothing to fix") {
		t.Fatalf("report = %q", report)
	}
}
