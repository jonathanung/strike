package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKeybindChordsUnmarshal(t *testing.T) {
	var obj struct {
		Keybinds map[string]KeybindChords `json:"keybinds"`
	}
	if err := json.Unmarshal([]byte(`{
		"keybinds": {
			"nav.jump-bottom": "ctrl+b",
			"global.palette": ["ctrl+p", "ctrl+k"]
		}
	}`), &obj); err != nil {
		t.Fatal(err)
	}
	if got := []string(obj.Keybinds["nav.jump-bottom"]); len(got) != 1 || got[0] != "ctrl+b" {
		t.Fatalf("jump-bottom = %#v", got)
	}
	if got := []string(obj.Keybinds["global.palette"]); len(got) != 2 || got[0] != "ctrl+p" || got[1] != "ctrl+k" {
		t.Fatalf("palette = %#v", got)
	}
}

func TestValidateKeybindsUnknownID(t *testing.T) {
	err := ValidateKeybinds(map[string]KeybindChords{
		"not.a.real.binding": {"ctrl+x"},
	})
	if err == nil || !strings.Contains(err.Error(), "unknown binding id") {
		t.Fatalf("err = %v, want unknown binding id", err)
	}
}

func TestValidateKeybindsInvalidChord(t *testing.T) {
	err := ValidateKeybinds(map[string]KeybindChords{
		"nav.jump-bottom": {"ctrl+"},
	})
	if err == nil {
		t.Fatal("expected invalid chord error")
	}
	err = ValidateKeybinds(map[string]KeybindChords{
		"nav.jump-bottom": {"not a key"},
	})
	if err == nil || !strings.Contains(err.Error(), "whitespace") {
		t.Fatalf("err = %v, want whitespace", err)
	}
}

func TestValidateKeybindsCriticalCannotClear(t *testing.T) {
	err := ValidateKeybinds(map[string]KeybindChords{
		"global.quit": {},
	})
	if err == nil || !strings.Contains(err.Error(), "critical") {
		t.Fatalf("err = %v, want critical", err)
	}
	err = ValidateKeybinds(map[string]KeybindChords{
		"global.interrupt": {""},
	})
	if err == nil {
		t.Fatal("expected empty chord error")
	}
}

func TestValidateKeybindsNormalizes(t *testing.T) {
	binds := map[string]KeybindChords{
		"nav.jump-bottom": {" Ctrl+B ", "ctrl+b"},
	}
	if err := ValidateKeybinds(binds); err != nil {
		t.Fatal(err)
	}
	got := binds["nav.jump-bottom"]
	if len(got) != 1 || got[0] != "ctrl+b" {
		t.Fatalf("normalized = %#v", got)
	}
}

func TestLoadKeybindsMergeAndUnknownFails(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()

	global := filepath.Join(home, ".strike", "config")
	if err := os.MkdirAll(filepath.Dir(global), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(global, []byte(`{
		"keybinds": {
			"nav.jump-bottom": "ctrl+g",
			"global.palette": "ctrl+p"
		}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(work, ".strike", "config")
	if err := os.MkdirAll(filepath.Dir(project), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(project, []byte(`{
		"keybinds": {
			"nav.jump-bottom": "ctrl+b"
		}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(work)
	if err != nil {
		t.Fatal(err)
	}
	if got := []string(cfg.Keybinds["nav.jump-bottom"]); len(got) != 1 || got[0] != "ctrl+b" {
		t.Fatalf("project should win: %#v", cfg.Keybinds["nav.jump-bottom"])
	}
	if got := []string(cfg.Keybinds["global.palette"]); len(got) != 1 || got[0] != "ctrl+p" {
		t.Fatalf("global palette kept: %#v", cfg.Keybinds["global.palette"])
	}

	home2 := t.TempDir()
	t.Setenv("HOME", home2)
	work2 := t.TempDir()
	p := filepath.Join(work2, ".strike", "config")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(`{"keybinds":{"nope.id":"x"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(work2); err == nil || !strings.Contains(err.Error(), "unknown binding id") {
		t.Fatalf("Load err = %v, want unknown binding id", err)
	}
}

func TestMergeKeybindsLastWins(t *testing.T) {
	base := map[string]KeybindChords{"nav.jump-bottom": {"ctrl+t"}, "global.quit": {"ctrl+c"}}
	layer := map[string]KeybindChords{"nav.jump-bottom": {"ctrl+b"}}
	got := MergeKeybinds(base, layer)
	if len(got["nav.jump-bottom"]) != 1 || got["nav.jump-bottom"][0] != "ctrl+b" {
		t.Fatalf("%#v", got)
	}
	if len(got["global.quit"]) != 1 || got["global.quit"][0] != "ctrl+c" {
		t.Fatalf("%#v", got)
	}
}

func TestParseKeybindsFileFlatAndWrapped(t *testing.T) {
	flat, err := ParseKeybindsFile([]byte(`{
		// jump
		"nav.jump-bottom": "ctrl+b",
		"global.palette": ["ctrl+k", "ctrl+p"]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := []string(flat["nav.jump-bottom"]); len(got) != 1 || got[0] != "ctrl+b" {
		t.Fatalf("flat jump = %#v", got)
	}
	if got := []string(flat["global.palette"]); len(got) != 2 || got[0] != "ctrl+k" {
		t.Fatalf("flat palette = %#v", got)
	}

	wrapped, err := ParseKeybindsFile([]byte(`{
		"keybinds": {
			"nav.jump-bottom": "Ctrl+G"
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := []string(wrapped["nav.jump-bottom"]); len(got) != 1 || got[0] != "ctrl+g" {
		t.Fatalf("wrapped = %#v", got)
	}

	empty, err := ParseKeybindsFile([]byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(empty) != 0 {
		t.Fatalf("empty = %#v", empty)
	}

	if _, err := ParseKeybindsFile([]byte(`{"nope.id":"x"}`)); err == nil || !strings.Contains(err.Error(), "unknown binding id") {
		t.Fatalf("err = %v, want unknown binding id", err)
	}
	if _, err := ParseKeybindsFile([]byte(`[]`)); err == nil || !strings.Contains(err.Error(), "JSON object") {
		t.Fatalf("err = %v, want JSON object", err)
	}
}

func TestLoadKeybindsJSONCLayers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()

	globalCfg := filepath.Join(home, ".strike", "config")
	if err := os.MkdirAll(filepath.Dir(globalCfg), 0o755); err != nil {
		t.Fatal(err)
	}
	// Config sets palette; dedicated file should override same-root config.
	if err := os.WriteFile(globalCfg, []byte(`{
		"keybinds": {
			"global.palette": "ctrl+p",
			"nav.jump-bottom": "ctrl+g"
		}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	globalKB := filepath.Join(home, ".strike", "keybinds.jsonc")
	if err := os.WriteFile(globalKB, []byte(`{
		// overrides config in same root
		"global.palette": "ctrl+k",
		"composer.newline": "ctrl+j"
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	projectKB := filepath.Join(work, ".strike", "keybinds.json")
	if err := os.MkdirAll(filepath.Dir(projectKB), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(projectKB, []byte(`{
		"nav.jump-bottom": "ctrl+b"
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(work)
	if err != nil {
		t.Fatal(err)
	}
	if got := []string(cfg.Keybinds["global.palette"]); len(got) != 1 || got[0] != "ctrl+k" {
		t.Fatalf("global jsonc should override config palette: %#v", got)
	}
	if got := []string(cfg.Keybinds["composer.newline"]); len(got) != 1 || got[0] != "ctrl+j" {
		t.Fatalf("global jsonc newline: %#v", got)
	}
	if got := []string(cfg.Keybinds["nav.jump-bottom"]); len(got) != 1 || got[0] != "ctrl+b" {
		t.Fatalf("project file should win jump-bottom: %#v", got)
	}

	// Prefer .jsonc over .json when both exist.
	home2 := t.TempDir()
	t.Setenv("HOME", home2)
	root := filepath.Join(home2, ".strike")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "keybinds.json"), []byte(`{"nav.jump-bottom":"ctrl+j"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "keybinds.jsonc"), []byte(`{"nav.jump-bottom":"ctrl+c"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// ctrl+c is valid for jump-bottom (critical check only for quit/interrupt).
	cfg2, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got := []string(cfg2.Keybinds["nav.jump-bottom"]); len(got) != 1 || got[0] != "ctrl+c" {
		t.Fatalf("jsonc preferred: %#v", got)
	}

	// Bad dedicated file fails Load.
	home3 := t.TempDir()
	t.Setenv("HOME", home3)
	bad := filepath.Join(home3, ".strike", "keybinds.jsonc")
	if err := os.MkdirAll(filepath.Dir(bad), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, []byte(`{"nope.id":"x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(t.TempDir()); err == nil || !strings.Contains(err.Error(), "unknown binding id") {
		t.Fatalf("Load err = %v, want unknown binding id", err)
	}
}

func TestKeybindsFilePathHelpers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if p := GlobalKeybindsFilePath(); !strings.HasSuffix(p, "keybinds.jsonc") {
		t.Fatalf("missing path default = %q", p)
	}
	root := filepath.Join(home, ".strike")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	jsonPath := filepath.Join(root, "keybinds.json")
	if err := os.WriteFile(jsonPath, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if p := GlobalKeybindsFilePath(); p != jsonPath {
		t.Fatalf("existing json = %q want %q", p, jsonPath)
	}
	work := t.TempDir()
	if p := ProjectKeybindsFilePath(work); !strings.HasSuffix(p, filepath.Join(".strike", "keybinds.jsonc")) {
		t.Fatalf("project missing = %q", p)
	}
}
