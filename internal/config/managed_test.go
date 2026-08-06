package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/permission"
	"github.com/jonathanung/strike-cli/internal/protocol"
)

func TestDefaultManagedRootByOS(t *testing.T) {
	// Clear override so we exercise platform defaults.
	t.Setenv(envManagedRoot, "")
	got := defaultManagedRoot()
	switch runtime.GOOS {
	case "darwin":
		if got != "/Library/Application Support/Strike" {
			t.Fatalf("darwin root = %q", got)
		}
	case "windows":
		if !strings.Contains(got, "Strike") {
			t.Fatalf("windows root = %q", got)
		}
	default:
		if got != "/etc/strike" {
			t.Fatalf("unix root = %q", got)
		}
	}
}

func TestManagedRootEnvOverride(t *testing.T) {
	root := t.TempDir()
	t.Setenv(envManagedRoot, root)
	if got := ManagedRoot(); got != root {
		t.Fatalf("ManagedRoot() = %q, want %q", got, root)
	}
	if got := ManagedConfigPath(); got != filepath.Join(root, "managed-config") {
		t.Fatalf("ManagedConfigPath() = %q", got)
	}
	if got := ManagedDropInDir(); got != filepath.Join(root, "managed-config.d") {
		t.Fatalf("ManagedDropInDir() = %q", got)
	}
}

func TestLoadManagedMissingIsEmpty(t *testing.T) {
	t.Setenv(envManagedRoot, t.TempDir())
	cfg, info, err := LoadManaged()
	if err != nil {
		t.Fatal(err)
	}
	if info.Active() {
		t.Fatalf("want inactive managed, got %+v", info)
	}
	if cfg.Sandbox != "" || cfg.PermissionMode != "" || len(cfg.Permissions) != 0 {
		t.Fatalf("want zero config, got sandbox=%q mode=%q perms=%d", cfg.Sandbox, cfg.PermissionMode, len(cfg.Permissions))
	}
}

func TestLoadManagedBaseAndDropIns(t *testing.T) {
	root := t.TempDir()
	t.Setenv(envManagedRoot, root)

	base := `{"sandbox":"read-only","permissionMode":"plan","permissions":[
		{"permission":"bash","pattern":"rm *","action":"deny"},
		{"permission":"bash","pattern":"go *","action":"allow"}
	]}`
	if err := os.WriteFile(filepath.Join(root, "managed-config.json"), []byte(base), 0o644); err != nil {
		t.Fatal(err)
	}
	dropDir := filepath.Join(root, "managed-config.d")
	if err := os.MkdirAll(dropDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Later drop-in overrides sandbox scalar; permissions concatenate.
	if err := os.WriteFile(filepath.Join(dropDir, "10-net.json"), []byte(`{
		"sandbox":"workspace-write",
		"permissions":[{"permission":"webfetch","pattern":"*","action":"deny"}]
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Hidden and non-json ignored.
	if err := os.WriteFile(filepath.Join(dropDir, ".hidden.json"), []byte(`{"sandbox":"off"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dropDir, "readme.txt"), []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, info, err := LoadManaged()
	if err != nil {
		t.Fatal(err)
	}
	if !info.Active() || !info.Sandbox || !info.PermissionMode || !info.Permissions {
		t.Fatalf("locks: %+v", info)
	}
	if cfg.Sandbox != "workspace-write" {
		t.Errorf("sandbox = %q (drop-in should win)", cfg.Sandbox)
	}
	if cfg.PermissionMode != protocol.PermissionModePlan {
		t.Errorf("permissionMode = %q", cfg.PermissionMode)
	}
	if len(cfg.Permissions) != 3 {
		t.Fatalf("permissions = %d, want 3", len(cfg.Permissions))
	}
	if len(info.DenyRules) != 2 {
		t.Fatalf("DenyRules = %d, want 2 (bash rm + webfetch)", len(info.DenyRules))
	}
	for _, r := range info.DenyRules {
		if r.Action != permission.Deny {
			t.Errorf("DenyRules contains non-deny: %+v", r)
		}
	}
	if len(info.Sources) != 2 {
		t.Fatalf("Sources = %v", info.Sources)
	}
}

func TestLoadManagedJSONC(t *testing.T) {
	root := t.TempDir()
	t.Setenv(envManagedRoot, root)
	raw := `{
		// enterprise sandbox floor
		"sandbox": "read-only",
		"permissionMode": "default"
	}`
	if err := os.WriteFile(filepath.Join(root, "managed-config.jsonc"), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, info, err := LoadManaged()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Sandbox != "read-only" || !info.Sandbox {
		t.Fatalf("cfg=%+v info=%+v", cfg, info)
	}
}

func TestLoadManagedRejectsBadPermissionMode(t *testing.T) {
	root := t.TempDir()
	t.Setenv(envManagedRoot, root)
	if err := os.WriteFile(filepath.Join(root, "managed-config.json"), []byte(`{"permissionMode":"nope"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadManaged(); err == nil {
		t.Fatal("want error for unknown permissionMode")
	}
}

func TestLoadAppliesManagedOverProjectAndGlobal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()
	managedRoot := t.TempDir()
	t.Setenv(envManagedRoot, managedRoot)

	if err := os.MkdirAll(filepath.Join(home, ".strike"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".strike", "config"), []byte(`{
		"sandbox":"off",
		"permissionMode":"yolo",
		"permissions":[{"permission":"bash","pattern":"*","action":"allow"}]
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(work, ".strike"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, ".strike", "config"), []byte(`{
		"sandbox":"workspace-write",
		"permissionMode":"accept-edits",
		"permissions":[{"permission":"bash","pattern":"rm *","action":"allow"}]
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(managedRoot, "managed-config.json"), []byte(`{
		"sandbox":"read-only",
		"permissionMode":"plan",
		"permissions":[
			{"permission":"bash","pattern":"*","action":"deny"},
			{"permission":"write","pattern":"**/.env","action":"deny"}
		]
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(work)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Sandbox != "read-only" {
		t.Errorf("sandbox = %q, want managed read-only", cfg.Sandbox)
	}
	if cfg.PermissionMode != protocol.PermissionModePlan {
		t.Errorf("permissionMode = %q, want plan", cfg.PermissionMode)
	}
	if !cfg.Managed.Active() || !cfg.Managed.Sandbox || !cfg.Managed.PermissionMode {
		t.Fatalf("Managed locks: %+v", cfg.Managed)
	}
	// Global allow + project allow + managed denies concatenated; last-match
	// managed deny wins for bash *.
	if got := permission.Evaluate("bash", "echo hi", cfg.Permissions); got != permission.Deny {
		t.Errorf("effective bash via permissions[] = %s, want deny", got)
	}
	if len(cfg.Managed.DenyRules) != 2 {
		t.Fatalf("DenyRules = %#v", cfg.Managed.DenyRules)
	}
}

func TestLoadWithoutManagedLeavesInfoEmpty(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(envManagedRoot, t.TempDir())
	work := t.TempDir()
	if err := os.MkdirAll(filepath.Join(work, ".strike"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, ".strike", "config"), []byte(`{"sandbox":"off"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(work)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Managed.Active() {
		t.Fatalf("unexpected managed: %+v", cfg.Managed)
	}
	if cfg.Sandbox != "off" {
		t.Errorf("sandbox = %q", cfg.Sandbox)
	}
}

func TestManagedDenyOnly(t *testing.T) {
	rs := permission.Ruleset{
		{Permission: "bash", Pattern: "*", Action: permission.Allow},
		{Permission: "write", Pattern: "*", Action: permission.Deny},
		{Permission: "edit", Pattern: "*", Action: permission.Ask},
	}
	got := managedDenyOnly(rs)
	if len(got) != 1 || got[0].Permission != "write" {
		t.Fatalf("got %#v", got)
	}
	if managedDenyOnly(nil) != nil {
		t.Fatal("nil in → nil out")
	}
}
