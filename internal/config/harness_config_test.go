package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadHarnessesMergeOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	work := t.TempDir()
	global := filepath.Join(home, ".strike", "config")
	if err := os.MkdirAll(filepath.Dir(global), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(global, []byte(`{"harnesses":{"global":{"command":"global","args":["one"],"env":{"A":"1"}},"shared":{"command":"old","args":["old"],"env":{"A":"1"}}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(work, ".strike", "config")
	if err := os.MkdirAll(filepath.Dir(project), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(project, []byte(`{"harnesses":{"shared":{"command":"new","args":["two"],"env":{"B":"2"}},"project":{"command":"project"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(work)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Harnesses["global"].Command != "global" || cfg.Harnesses["project"].Command != "project" {
		t.Fatalf("harnesses = %#v", cfg.Harnesses)
	}
	shared := cfg.Harnesses["shared"]
	if shared.Command != "new" || shared.Args[0] != "two" || shared.Env["B"] != "2" {
		t.Fatalf("shared = %#v", shared)
	}
	if _, ok := shared.Env["A"]; ok {
		t.Fatalf("shared retained global env: %#v", shared)
	}

	base := map[string]HarnessConfig{"base": {Command: "run", Args: []string{"arg"}, Env: map[string]string{"KEY": "value"}}}
	layer := map[string]HarnessConfig{"layer": {Command: "run", Args: []string{"arg"}, Env: map[string]string{"KEY": "value"}}}
	merged := mergeHarnesses(base, layer)
	h := merged["base"]
	h.Args[0] = "changed"
	h.Env["KEY"] = "changed"
	if base["base"].Args[0] != "arg" || base["base"].Env["KEY"] != "value" {
		t.Fatal("mergeHarnesses aliased base slices or maps")
	}
	h = merged["layer"]
	h.Args[0] = "changed"
	h.Env["KEY"] = "changed"
	if layer["layer"].Args[0] != "arg" || layer["layer"].Env["KEY"] != "value" {
		t.Fatal("mergeHarnesses aliased layer slices or maps")
	}
}

func TestLoadRejectsInvalidHarness(t *testing.T) {
	tests := []struct {
		name   string
		config string
		want   string
	}{
		{name: "name", config: `{"harnesses":{"bad\u001bname":{"command":"run"}}}`, want: "control character"},
		{name: "command", config: `{"harnesses":{"custom":{"command":"  "}}}`, want: "command is empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			path := filepath.Join(home, ".strike", "config")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(tt.config), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(t.TempDir()); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Load error = %v, want %q", err, tt.want)
			}
		})
	}
}
