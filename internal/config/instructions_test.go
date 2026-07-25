package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadInstructionsProjectWalkUp(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	agents := filepath.Join(root, "AGENTS.md")
	if err := os.WriteFile(agents, []byte("project rules"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Nested CLAUDE.md should win when starting from nested (first match walking up).
	// Start at nested with only root AGENTS.md → finds root.
	got := LoadInstructions(nested, root)
	if len(got) != 1 || !strings.Contains(got[0], "project rules") || !strings.Contains(got[0], agents) {
		t.Fatalf("LoadInstructions = %#v", got)
	}

	claude := filepath.Join(nested, "CLAUDE.md")
	if err := os.WriteFile(claude, []byte("nested rules"), 0o644); err != nil {
		t.Fatal(err)
	}
	got = LoadInstructions(nested, root)
	if len(got) != 1 || !strings.Contains(got[0], "nested rules") {
		t.Fatalf("expected nested CLAUDE.md first, got %#v", got)
	}
}

func TestLoadInstructionsPrefersAGENTSOverCLAUDEInSameDir(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("agents"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("claude"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := LoadInstructions(root, root)
	if len(got) != 1 || !strings.Contains(got[0], "agents") || strings.Contains(got[0], "claude") {
		t.Fatalf("got %#v", got)
	}
}

func TestLoadInstructionsDoesNotWalkAboveProjectRoot(t *testing.T) {
	outer := t.TempDir()
	root := filepath.Join(outer, "repo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outer, "AGENTS.md"), []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := LoadInstructions(root, root)
	for _, block := range got {
		if strings.Contains(block, "outside") {
			t.Fatalf("leaked outer instruction: %#v", got)
		}
	}
}

func TestLoadInstructionsSkipsEmpty(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("  \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := LoadInstructions(root, root)
	// May still include a global file from the real home; none should be empty-only project.
	for _, block := range got {
		if strings.Contains(block, root) {
			t.Fatalf("empty project file should be skipped: %#v", got)
		}
	}
}
