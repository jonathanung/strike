package local

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/host"
)

func TestPanesListStaticAndCollision(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	global := filepath.Join(home, ".strike")
	plugins := filepath.Join(global, "plugins")
	if err := os.MkdirAll(plugins, 0o755); err != nil {
		t.Fatal(err)
	}

	writePlugin := func(id, paneID, title string) {
		t.Helper()
		root := filepath.Join(plugins, id)
		if err := os.MkdirAll(filepath.Join(root, "panes"), 0o755); err != nil {
			t.Fatal(err)
		}
		manifest := `{
  "schemaVersion": 1,
  "id": "` + id + `",
  "version": "1.0.0",
  "name": "` + id + `",
  "strike": {"min": "0.1.0"},
  "capabilities": ["panes"],
  "contributions": {
    "panes": [{"id": "` + paneID + `", "path": "panes/p.json", "abi": "pane/1"}]
  }
}`
		if err := os.WriteFile(filepath.Join(root, "plugin.json"), []byte(manifest), 0o644); err != nil {
			t.Fatal(err)
		}
		def := `{
  "schemaVersion": 1,
  "id": "` + paneID + `",
  "title": "` + title + `",
  "mode": "static",
  "permissions": {"host": ["session.summary"], "fs": "none", "network": "none", "command": "none"},
  "subscriptions": ["session.summary"],
  "view": {
    "type": "column",
    "gap": 1,
    "children": [
      {"type": "text", "text": "Hello", "style": "title"},
      {"type": "kv", "entries": [{"key": "cwd", "valueFrom": "session.summary.cwd"}]}
    ]
  }
}`
		if err := os.WriteFile(filepath.Join(root, "panes", "p.json"), []byte(def), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	writePlugin("acme.one", "acme.status", "Status One")
	svc := NewPanesForTest("", global, "")
	list, err := svc.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("list = %d %+v", len(list), list)
	}
	p := list[0]
	if p.ID != "acme.status" || p.Mode != host.PaneModeStatic || !p.Trusted {
		t.Fatalf("pane = %+v", p)
	}
	if p.Title != "Status One" || p.PluginID != "acme.one" {
		t.Fatalf("meta = %+v", p)
	}
	if len(p.DefinitionJSON) == 0 {
		t.Fatal("missing definition")
	}
	if !strings.Contains(p.Provenance(), "plugin=acme.one@1.0.0") {
		t.Fatalf("provenance = %q", p.Provenance())
	}

	// Collision: second plugin claims same pane id → fail closed (neither mounts).
	writePlugin("acme.two", "acme.status", "Status Two")
	list, err = svc.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("collision should drop both, got %d %+v", len(list), list)
	}
}

func TestResolvePaneEnvSecretRefs(t *testing.T) {
	t.Setenv("STRIKE_PANE_TEST_SECRET", "s3cr3t-value")
	out, err := resolvePaneEnv(map[string]string{
		"TOK":   "secret://env/STRIKE_PANE_TEST_SECRET",
		"PLAIN": "ok",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out["TOK"] != "s3cr3t-value" || out["PLAIN"] != "ok" {
		t.Fatalf("out=%v", out)
	}
	if _, err := resolvePaneEnv(map[string]string{"X": "secret://env/MISSING_PANE_SECRET_XYZ"}); err == nil {
		t.Fatal("expected missing secret fail-closed")
	}
}

func TestPanesListSkipsDisabled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	global := filepath.Join(home, ".strike")
	root := filepath.Join(global, "plugins", "acme.off")
	if err := os.MkdirAll(filepath.Join(root, "panes"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{
  "schemaVersion": 1, "id": "acme.off", "version": "1.0.0", "name": "Off",
  "strike": {"min": "0.1.0"},
  "contributions": {"panes": [{"id": "acme.x", "path": "panes/p.json", "abi": "pane/1"}]}
}`
	if err := os.WriteFile(filepath.Join(root, "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	def := `{
  "schemaVersion":1,"id":"acme.x","title":"X","mode":"static",
  "permissions":{"host":[],"fs":"none","network":"none","command":"none"},
  "view":{"type":"text","text":"x"}
}`
	if err := os.WriteFile(filepath.Join(root, "panes", "p.json"), []byte(def), 0o644); err != nil {
		t.Fatal(err)
	}
	lock := `{"schemaVersion":1,"plugins":{"acme.off":{"enabled":false}}}`
	if err := os.WriteFile(filepath.Join(global, "plugins.lock.json"), []byte(lock), 0o644); err != nil {
		t.Fatal(err)
	}
	list, err := NewPanesForTest("", global, "").List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("disabled plugin contributed panes: %+v", list)
	}
}

func TestPanesList_APSStrikeCLI(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	global := filepath.Join(home, ".strike")
	root := filepath.Join(global, "plugins", "acme.aps")
	if err := os.MkdirAll(filepath.Join(root, "com.strike.cli", "panes"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{
  "$schema": "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
  "name": "acme.aps",
  "version": "1.0.0"
}`
	if err := os.WriteFile(filepath.Join(root, "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	static := `{
  "schemaVersion": 1,
  "id": "acme.status",
  "title": "APS Status",
  "mode": "static",
  "permissions": {"host": [], "fs": "none", "network": "none", "command": "none"},
  "view": {"type": "text", "text": "hi"}
}`
	proc := `{
  "schemaVersion": 1,
  "id": "acme.board",
  "title": "APS Board",
  "mode": "process",
  "command": "com.strike.cli/bin/board",
  "permissions": {"host": [], "fs": "none", "network": "none", "command": "none"}
}`
	if err := os.WriteFile(filepath.Join(root, "com.strike.cli", "panes", "status.json"), []byte(static), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "com.strike.cli", "panes", "board.json"), []byte(proc), 0o644); err != nil {
		t.Fatal(err)
	}
	svc := NewPanesForTest("", global, "")
	list, err := svc.List()
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]host.PaneInfo{}
	for _, p := range list {
		byID[p.ID] = p
	}
	st, ok := byID["acme.status"]
	if !ok || st.Mode != host.PaneModeStatic || !st.Trusted {
		t.Fatalf("static = %+v list=%+v", st, list)
	}
	if st.PluginID != "acme.aps" {
		t.Fatalf("plugin id = %s", st.PluginID)
	}
	pr, ok := byID["acme.board"]
	if !ok || pr.Mode != host.PaneModeProcess {
		t.Fatalf("process = %+v", pr)
	}
	if pr.Trusted {
		t.Fatal("process pane must stay untrusted until trust is granted")
	}
	if pr.LoadError == "" || !strings.Contains(pr.LoadError, "trust") {
		t.Fatalf("want trust load error, got %q", pr.LoadError)
	}
}
