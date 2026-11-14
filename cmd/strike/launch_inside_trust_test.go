package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jonathanung/strike/internal/product/config"
	"github.com/jonathanung/strike/internal/trust/workspacetrust"
)

// TestMaybeLaunchInsideContainerIgnoresUntrustedRepoConfig: a repo that flips
// execution to "container" and points the engine at its own script must not
// reach the engine before the workspace is trusted (#1352). The script would
// touch a marker if it ran.
func TestMaybeLaunchInsideContainerIgnoresUntrustedRepoConfig(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("/bin/sh unavailable")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	config.ResetPluginCache()
	repo := t.TempDir()
	marker := filepath.Join(t.TempDir(), "engine-ran")
	engine := filepath.Join(repo, "scripts", "docker")
	if err := os.MkdirAll(filepath.Dir(engine), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(engine, []byte("#!/bin/sh\ntouch '"+marker+"'\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".strike"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".strike", "container.jsonc"), []byte(`{"execution": "container", "engine": "`+engine+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)

	scope, err := launchConfigScope(repo)
	if err != nil {
		t.Fatal(err)
	}
	if scope != "" {
		t.Fatalf("launchConfigScope = %q, want empty for untrusted repo", scope)
	}

	var stderr bytes.Buffer
	if err := maybeLaunchInsideContainer(cliOptions{}, &stderr); err != nil {
		t.Fatalf("maybeLaunchInsideContainer: %v\nstderr=%s", err, stderr.String())
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("untrusted repo engine was executed (marker stat err = %v)", err)
	}

	// Trusted: the repo config is in scope again (the launch itself is not
	// exercised here — it would re-exec the process).
	store, err := workspacetrust.Open(workspacetrust.DefaultPath(filepath.Join(home, ".strike")))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Trust(repo); err != nil {
		t.Fatal(err)
	}
	scope, err = launchConfigScope(repo)
	if err != nil {
		t.Fatal(err)
	}
	if scope != repo {
		t.Fatalf("launchConfigScope (trusted) = %q, want %q", scope, repo)
	}
	cfg, err := config.Load(scope)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Container.Execution != "container" || cfg.Container.Engine != engine {
		t.Fatalf("trusted scope lost container config: %+v", cfg.Container)
	}
}
