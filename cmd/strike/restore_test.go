package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunRestoreCLIGlobal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	var stdout, stderr bytes.Buffer
	code := runRestoreCLI(nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q stdout=%q", code, stderr.String(), stdout.String())
	}
	cfg := filepath.Join(home, ".strike", "config")
	if _, err := os.Stat(cfg); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "strike restore:") {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunRestoreCLIProject(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()

	// Corrupt project config.
	pRoot := filepath.Join(work, ".strike")
	if err := os.MkdirAll(pRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pRoot, "config"), []byte("{bad"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := runRestoreCLI([]string{"--project-dir", work}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	// Global + project reports.
	if strings.Count(stdout.String(), "strike restore:") != 2 {
		t.Fatalf("stdout = %q", stdout.String())
	}
	// Corrupt project config is quarantined, not rewritten (optional file).
	if _, err := os.Stat(filepath.Join(pRoot, "config")); !os.IsNotExist(err) {
		t.Fatalf("project config should be absent after quarantine, err=%v", err)
	}
	matches, _ := filepath.Glob(filepath.Join(pRoot, "config.corrupt-*"))
	if len(matches) != 1 {
		t.Fatalf("expected one config backup, got %v", matches)
	}
	if fi, err := os.Stat(filepath.Join(pRoot, "worktrees")); err != nil || !fi.IsDir() {
		t.Fatalf("worktrees: %v", err)
	}
}

func TestRunRestoreCLIHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runRestoreCLI([]string{"--help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	if !strings.Contains(stdout.String(), "strike restore") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestRunRestoreCLIUnexpectedArg(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runRestoreCLI([]string{"nope"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("code=%d", code)
	}
	if !strings.Contains(stderr.String(), "unexpected argument") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunCLIDispatchesRestore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	var stdout, stderr bytes.Buffer
	code := runCLI([]string{"restore"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".strike", "sessions")); err != nil {
		t.Fatal(err)
	}
}
