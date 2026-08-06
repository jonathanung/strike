package local

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/host"
)

func TestConfigFilesListAndEnsure(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	t.Setenv("HOME", home)

	globalStrike := filepath.Join(home, ".strike")
	if err := os.MkdirAll(filepath.Join(globalStrike, "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(globalStrike, "config"), []byte(`{"provider":"echo"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(globalStrike, "agents", "review.md"), []byte("# a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Forbidden files must never be listed even if present.
	if err := os.WriteFile(filepath.Join(globalStrike, "auth.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(globalStrike, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}

	cf := configFilesAdapter{}
	refs := cf.List(work)

	var sawAuth, sawSessions bool
	var sawGlobalConfig, sawProjectConfig, sawAgent bool
	var mcpRef host.ConfigFileRef
	for _, r := range refs {
		if strings.Contains(r.Path, "auth.json") || strings.Contains(r.Label, "auth") {
			sawAuth = true
		}
		if strings.Contains(r.Path, "sessions") {
			sawSessions = true
		}
		if r.Slot == "config" && r.Scope == host.ConfigScopeGlobal && r.Exists {
			sawGlobalConfig = true
		}
		if r.Slot == "config" && r.Scope == host.ConfigScopeProject {
			sawProjectConfig = true
			if r.Exists {
				t.Fatal("project config should be missing")
			}
		}
		if r.Kind == "agents" && strings.Contains(r.Label, "review.md") {
			sawAgent = true
		}
		if r.Slot == "mcp" && r.Scope == host.ConfigScopeGlobal {
			mcpRef = r
		}
	}
	if sawAuth {
		t.Fatal("auth.json must not appear")
	}
	if sawSessions {
		t.Fatal("sessions must not appear")
	}
	if !sawGlobalConfig || !sawProjectConfig || !sawAgent {
		t.Fatalf("missing expected rows: globalCfg=%v projectCfg=%v agent=%v (n=%d)", sawGlobalConfig, sawProjectConfig, sawAgent, len(refs))
	}
	if mcpRef.Path == "" || mcpRef.Exists {
		t.Fatalf("mcp ref = %#v", mcpRef)
	}
	// Prefer .jsonc create path
	if !strings.HasSuffix(mcpRef.Path, "mcp.jsonc") {
		t.Fatalf("mcp path = %q, want mcp.jsonc", mcpRef.Path)
	}

	path, created, err := cf.Ensure(mcpRef)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("expected create")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != emptyJSONObjectStub {
		t.Fatalf("stub = %q", data)
	}
	// Second ensure is no-op
	_, created, err = cf.Ensure(mcpRef)
	if err != nil || created {
		t.Fatalf("second ensure created=%v err=%v", created, err)
	}

	// Project config creates empty object
	var proj host.ConfigFileRef
	for _, r := range refs {
		if r.Slot == "config" && r.Scope == host.ConfigScopeProject {
			proj = r
			break
		}
	}
	ppath, created, err := cf.Ensure(proj)
	if err != nil || !created {
		t.Fatalf("project ensure: created=%v err=%v", created, err)
	}
	pdata, _ := os.ReadFile(ppath)
	if string(pdata) != emptyJSONObjectStub {
		t.Fatalf("project stub = %q", pdata)
	}
}

func TestConfigFilesEnsureRejectsEscape(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cf := configFilesAdapter{}
	_, _, err := cf.Ensure(host.ConfigFileRef{
		Slot:      "config",
		Scope:     host.ConfigScopeGlobal,
		Path:      filepath.Join(home, "secret"),
		CanCreate: true,
	})
	if err == nil {
		t.Fatal("expected escape rejection")
	}
}

func TestConfigFilesLoadKeybinds(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".strike"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".strike", "keybinds.jsonc"), []byte(`{"nav.jump-bottom":["ctrl+b"]}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cf := configFilesAdapter{}
	got, err := cf.LoadKeybinds(work)
	if err != nil {
		t.Fatal(err)
	}
	if len(got["nav.jump-bottom"]) != 1 || got["nav.jump-bottom"][0] != "ctrl+b" {
		t.Fatalf("got %#v", got)
	}
}

func TestConfigFilesEnsureNoCreateExtras(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cf := configFilesAdapter{}
	_, _, err := cf.Ensure(host.ConfigFileRef{
		Kind:      "agents",
		Scope:     host.ConfigScopeGlobal,
		Path:      filepath.Join(home, ".strike", "agents", "x.md"),
		CanCreate: false,
	})
	if err == nil {
		t.Fatal("expected error for missing non-creatable")
	}
}
