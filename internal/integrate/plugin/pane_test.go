package plugin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParsePaneEntry(t *testing.T) {
	raw := json.RawMessage(`{"id":"acme.status","path":"panes/status.json","abi":"pane/1"}`)
	e, err := ParsePaneEntry(raw)
	if err != nil {
		t.Fatal(err)
	}
	if e.ID != "acme.status" || e.Path != "panes/status.json" || e.ABI != PaneABI {
		t.Fatalf("entry = %+v", e)
	}
}

func TestParsePaneEntryRejectsReservedABIAndBuiltinID(t *testing.T) {
	_, err := ParsePaneEntry(json.RawMessage(`{"id":"acme.x","path":"p.json","abi":"reserved"}`))
	if err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("reserved abi: %v", err)
	}
	_, err = ParsePaneEntry(json.RawMessage(`{"id":"context","path":"p.json","abi":"pane/1"}`))
	if err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("builtin id: %v", err)
	}
}

func TestParsePaneDefinitionStatic(t *testing.T) {
	raw := `{
  "schemaVersion": 1,
  "id": "acme.status",
  "title": "Acme Status",
  "mode": "static",
  "permissions": {
    "host": ["session.summary", "usage"],
    "fs": "none",
    "network": "none",
    "command": "none"
  },
  "subscriptions": ["session.summary", "usage"],
  "view": {
    "type": "column",
    "children": [
      {"type": "text", "text": "hi", "style": "title"}
    ]
  }
}`
	d, err := ParsePaneDefinition([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if d.Mode != PaneModeStatic || d.Title != "Acme Status" {
		t.Fatalf("def = %+v", d)
	}
}

func TestParsePaneDefinitionRejectsStaticFSAndUnknownFeed(t *testing.T) {
	raw := `{
  "schemaVersion": 1,
  "id": "acme.bad",
  "title": "Bad",
  "mode": "static",
  "permissions": {"host": [], "fs": "read-workspace"},
  "view": {"type": "text", "text": "x"}
}`
	if _, err := ParsePaneDefinition([]byte(raw)); err == nil {
		t.Fatal("expected static fs reject")
	}
	raw2 := `{
  "schemaVersion": 1,
  "id": "acme.bad2",
  "title": "Bad",
  "mode": "static",
  "permissions": {"host": ["nope"]},
  "subscriptions": ["nope"],
  "view": {"type": "text", "text": "x"}
}`
	if _, err := ParsePaneDefinition([]byte(raw2)); err == nil {
		t.Fatal("expected unknown feed reject")
	}
}

func TestParsePaneDefinitionProcess(t *testing.T) {
	raw := `{
  "schemaVersion": 1,
  "id": "acme.board",
  "title": "Board",
  "mode": "process",
  "command": "bin/board",
  "permissions": {"host": ["session.summary"], "fs": "none", "network": "none", "command": "none"},
  "subscriptions": ["session.summary"]
}`
	d, err := ParsePaneDefinition([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if d.Mode != PaneModeProcess || d.Command != "bin/board" {
		t.Fatalf("def = %+v", d)
	}
}

func TestHasProcessPanes(t *testing.T) {
	root := t.TempDir()
	mustWrite := func(rel, body string) {
		t.Helper()
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("panes/static.json", `{
  "schemaVersion":1,"id":"acme.s","title":"S","mode":"static",
  "permissions":{"host":[],"fs":"none","network":"none","command":"none"},
  "view":{"type":"text","text":"x"}
}`)
	mustWrite("panes/proc.json", `{
  "schemaVersion":1,"id":"acme.p","title":"P","mode":"process","command":"bin/x",
  "permissions":{"host":[],"fs":"none","network":"none","command":"none"}
}`)
	m := Manifest{
		Contributions: Contributions{
			Panes: []json.RawMessage{
				json.RawMessage(`{"id":"acme.s","path":"panes/static.json","abi":"pane/1"}`),
				json.RawMessage(`{"id":"acme.p","path":"panes/proc.json","abi":"pane/1"}`),
			},
		},
	}
	if !HasProcessPanes(m, root) {
		t.Fatal("expected process pane")
	}
	m.Contributions.Panes = m.Contributions.Panes[:1]
	if HasProcessPanes(m, root) {
		t.Fatal("static-only should not report process")
	}
	if HasExecutableContributionsAt(m, root) {
		t.Fatal("static-only must not be executable")
	}
	m.Contributions.Panes = append(m.Contributions.Panes,
		json.RawMessage(`{"id":"acme.p","path":"panes/proc.json","abi":"pane/1"}`))
	if !HasExecutableContributionsAt(m, root) {
		t.Fatal("process pane is executable")
	}
	caps := InferCapabilitiesAt(m, root)
	joined := strings.Join(caps, ",")
	if !strings.Contains(joined, CapPanes) || !strings.Contains(joined, CapPanesProcess) {
		t.Fatalf("caps = %v", caps)
	}
}

func TestClampPaneSizingAndTimeouts(t *testing.T) {
	s := ClampPaneSizing(PaneSizing{})
	if s.MinWidth != 20 || s.MinHeight != 4 || s.PreferredHeight != 12 {
		t.Fatalf("defaults = %+v", s)
	}
	s = ClampPaneSizing(PaneSizing{MinWidth: 100, MinHeight: 1, PreferredHeight: 2})
	if s.MinWidth != 80 || s.MinHeight != 3 || s.PreferredHeight != 3 {
		t.Fatalf("clamped = %+v", s)
	}
	to := ClampPaneTimeouts(PaneTimeouts{StartMs: 99999, ShutdownMs: 99999})
	if to.StartMs != 15000 || to.ShutdownMs != 5000 {
		t.Fatalf("timeouts = %+v", to)
	}
}

func TestReadPaneDefinitionPathConfinement(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "ok.json"), []byte(`{
  "schemaVersion":1,"id":"acme.ok","title":"Ok","mode":"static",
  "permissions":{"host":[],"fs":"none","network":"none","command":"none"},
  "view":{"type":"text","text":"x"}
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadPaneDefinition(root, "ok.json"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadPaneDefinition(root, "../outside.json"); err == nil {
		t.Fatal("expected path escape reject")
	}
}
