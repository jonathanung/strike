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
