package container

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectProjectDepsEmpty(t *testing.T) {
	d := DetectProjectDeps(t.TempDir())
	if len(d.Markers) != 0 || d.Go || d.Node || d.Python {
		t.Fatalf("%+v", d)
	}
}

func TestDetectProjectDepsGoMod(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/x\n\ngo 1.22.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Makefile"), []byte("all:\n\t@true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := DetectProjectDeps(dir)
	if !d.Go || !d.NeedsGo || d.GoVersion != "1.22.0" {
		t.Fatalf("go: %+v", d)
	}
	if !d.Make {
		t.Fatal("make")
	}
	joined := strings.Join(d.AptPackages, ",")
	if !strings.Contains(joined, "make") || !strings.Contains(joined, "gcc") {
		t.Fatalf("apt: %v", d.AptPackages)
	}
	if len(d.Markers) < 2 {
		t.Fatalf("markers: %v", d.Markers)
	}
}

func TestDetectProjectDepsNodeAndPython(t *testing.T) {
	dir := t.TempDir()
	pkg := `{"name":"app","engines":{"node":">=20"},"volta":{"node":"20.11.0"}}`
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkg), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "requirements.txt"), []byte("requests==2.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := DetectProjectDeps(dir)
	if !d.Node || !d.NeedsNode || d.NodeVersion != "20" {
		t.Fatalf("node: %+v", d)
	}
	if !d.Python || !d.NeedsPython || d.PythonVersion != "3" {
		t.Fatalf("python: %+v", d)
	}
}

func TestDetectProjectDepsRustNixCargo(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"Cargo.toml", "flake.nix"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("# stub\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	d := DetectProjectDeps(dir)
	if !d.Rust || !d.NeedsRust || !d.Nix {
		t.Fatalf("%+v", d)
	}
}

func TestApplyDetected(t *testing.T) {
	cfg := DefaultConfig()
	cfg.AptPackages = []string{"curl"}
	cfg.NodeVersion = "18" // user pin wins
	d := DetectedDeps{
		NeedsNode:   true,
		NodeVersion: "22",
		NeedsPython: true,
		AptPackages: []string{"curl", "make", "python3"},
	}
	got := ApplyDetected(cfg, d)
	if !got.NeedsNode || !got.NeedsPython {
		t.Fatalf("%+v", got)
	}
	if got.NodeVersion != "18" {
		t.Fatalf("user pin lost: %q", got.NodeVersion)
	}
	// python3 skipped when NeedsPython; make added; curl not duplicated
	joined := strings.Join(got.AptPackages, ",")
	if strings.Count(joined, "curl") != 1 || !strings.Contains(joined, "make") {
		t.Fatalf("apt: %v", got.AptPackages)
	}
	if strings.Contains(joined, "python3") {
		t.Fatalf("python3 should be skipped when NeedsPython: %v", got.AptPackages)
	}
}

func TestSuggestedContainerJSON(t *testing.T) {
	d := DetectedDeps{
		NeedsNode:   true,
		NodeVersion: "22",
		NeedsGo:     true,
		GoVersion:   "1.22",
		AptPackages: []string{"make", "gcc", "golang-go"},
	}
	m := d.SuggestedContainerJSON()
	if m["needsNode"] != true || m["nodeVersion"] != "22" {
		t.Fatalf("%+v", m)
	}
	if m["needsGo"] != true {
		t.Fatalf("%+v", m)
	}
	pkgs, _ := m["packages"].([]string)
	for _, p := range pkgs {
		if p == "golang-go" {
			t.Fatalf("golang-go should be filtered: %v", pkgs)
		}
	}
}

func TestDetectNodeVersionFallback(t *testing.T) {
	if got := detectNodeVersion(`not json`); got != "22" {
		t.Fatalf("%q", got)
	}
	if got := detectNodeVersion(`{"engines":{"node":"18.x"}}`); got != "18" {
		t.Fatalf("%q", got)
	}
}
