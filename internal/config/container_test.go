package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultContainerAndMerge(t *testing.T) {
	base := DefaultContainer()
	if base.BaseImage != "ubuntu:24.04" || base.Execution != "local" {
		t.Fatalf("%+v", base)
	}
	layer := ContainerConfig{
		BaseImage: "debian:bookworm",
		Packages:  []string{"make"},
		Execution: "container",
		Resources: ContainerResources{Memory: "1g", CPUs: "2"},
		Network:   ContainerNetwork{Mode: "none", Allow: []string{"api.github.com"}},
	}
	got := mergeContainer(base, layer)
	if got.BaseImage != "debian:bookworm" || got.Execution != "container" {
		t.Fatalf("%+v", got)
	}
	if len(got.Packages) != 1 || got.Packages[0] != "make" {
		t.Fatalf("packages: %v", got.Packages)
	}
	if got.Resources.Memory != "1g" || got.Resources.CPUs != "2" {
		t.Fatalf("resources: %+v", got.Resources)
	}
	if got.Network.Mode != "none" || len(got.Network.Allow) != 1 {
		t.Fatalf("network: %+v", got.Network)
	}
	// empty layer does not wipe
	got2 := mergeContainer(got, ContainerConfig{})
	if got2.BaseImage != "debian:bookworm" {
		t.Fatalf("wiped: %+v", got2)
	}
}

func TestNormalizeContainer(t *testing.T) {
	c, err := NormalizeContainer(ContainerConfig{Execution: "docker", Network: ContainerNetwork{Mode: "off"}})
	if err != nil {
		t.Fatal(err)
	}
	if c.Execution != "container" || c.Network.Mode != "none" {
		t.Fatalf("%+v", c)
	}
	if _, err := NormalizeContainer(ContainerConfig{Execution: "vm"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestContainerToRuntime(t *testing.T) {
	c := DefaultContainer()
	c.Packages = []string{"git"}
	c.Resources.Memory = "512m"
	rt := c.ToRuntime("vtest")
	if rt.BaseImage != c.BaseImage || rt.TemplateVersion != "vtest" {
		t.Fatalf("%+v", rt)
	}
	if len(rt.AptPackages) != 1 || rt.Resources.Memory != "512m" {
		t.Fatalf("%+v", rt)
	}
}

func TestLoadContainerFromConfigAndFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// GlobalRoot uses HOME/.strike
	gStrike := filepath.Join(home, ".strike")
	if err := os.MkdirAll(gStrike, 0o755); err != nil {
		t.Fatal(err)
	}
	// main config with container block
	if err := os.WriteFile(filepath.Join(gStrike, "config"), []byte(`{
  "container": {
    "baseImage": "ubuntu:22.04",
    "resources": { "memory": "256m" }
  }
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// project overlay via container.jsonc
	work := t.TempDir()
	pStrike := filepath.Join(work, ".strike")
	if err := os.MkdirAll(pStrike, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pStrike, "container.jsonc"), []byte(`{
  // project tightens
  "execution": "container",
  "network": { "mode": "none" },
  "packages": ["curl"]
}`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(work)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Container.BaseImage != "ubuntu:22.04" {
		t.Fatalf("base from global config: %q", cfg.Container.BaseImage)
	}
	if cfg.Container.Resources.Memory != "256m" {
		t.Fatalf("memory: %q", cfg.Container.Resources.Memory)
	}
	if cfg.Container.Execution != "container" {
		t.Fatalf("execution: %q", cfg.Container.Execution)
	}
	if cfg.Container.Network.Mode != "none" {
		t.Fatalf("mode: %q", cfg.Container.Network.Mode)
	}
	if len(cfg.Container.Packages) != 1 || cfg.Container.Packages[0] != "curl" {
		t.Fatalf("packages: %v", cfg.Container.Packages)
	}
	// defaults preserved
	if cfg.Container.Shell != "/bin/bash" {
		t.Fatalf("shell default lost: %q", cfg.Container.Shell)
	}
}

func TestParseContainerFile(t *testing.T) {
	cc, err := ParseContainerFile([]byte(`{"baseImage":"alpine:3","execution":"local"}`))
	if err != nil || cc.BaseImage != "alpine:3" {
		t.Fatalf("%+v %v", cc, err)
	}
}
