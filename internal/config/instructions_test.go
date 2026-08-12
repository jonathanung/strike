package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadInstructions(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	type files map[string]string // relative path → body
	tests := []struct {
		name       string
		files      files
		workRel    string // relative to project root; empty means project root
		outerFile  string // AGENTS.md body written above projectRoot
		globalFile string // ~/.strike/AGENTS.md body
		wantBodies []string
		wantRels   []string // project-relative paths expected in block order (skip globals)
		wantNoneOf []string
	}{
		{
			name:       "root-only",
			files:      files{"AGENTS.md": "root rules"},
			wantBodies: []string{"root rules"},
			wantRels:   []string{"AGENTS.md"},
		},
		{
			name:       "nested-only",
			files:      files{"a/b/AGENTS.md": "nested rules"},
			workRel:    "a/b",
			wantBodies: []string{"nested rules"},
			wantRels:   []string{"a/b/AGENTS.md"},
		},
		{
			name: "root+nested concatenation order",
			files: files{
				"AGENTS.md":     "root rules",
				"a/b/AGENTS.md": "nested rules",
			},
			workRel:    "a/b",
			wantBodies: []string{"root rules", "nested rules"},
			wantRels:   []string{"AGENTS.md", "a/b/AGENTS.md"},
		},
		{
			name: "three-level chain",
			files: files{
				"AGENTS.md":       "root",
				"a/AGENTS.md":     "mid",
				"a/b/c/AGENTS.md": "leaf",
			},
			workRel:    "a/b/c",
			wantBodies: []string{"root", "mid", "leaf"},
			wantRels:   []string{"AGENTS.md", "a/AGENTS.md", "a/b/c/AGENTS.md"},
		},
		{
			name: "CLAUDE.md fallback per dir",
			files: files{
				"CLAUDE.md":     "root claude",
				"a/b/CLAUDE.md": "nested claude",
			},
			workRel:    "a/b",
			wantBodies: []string{"root claude", "nested claude"},
			wantRels:   []string{"CLAUDE.md", "a/b/CLAUDE.md"},
		},
		{
			name: "prefers AGENTS.md over CLAUDE.md in same dir",
			files: files{
				"AGENTS.md": "agents",
				"CLAUDE.md": "claude",
			},
			wantBodies: []string{"agents"},
			wantRels:   []string{"AGENTS.md"},
			wantNoneOf: []string{"claude"},
		},
		{
			name: "nested AGENTS.md with root CLAUDE.md",
			files: files{
				"CLAUDE.md":     "root claude",
				"pkg/AGENTS.md": "pkg agents",
			},
			workRel:    "pkg",
			wantBodies: []string{"root claude", "pkg agents"},
			wantRels:   []string{"CLAUDE.md", "pkg/AGENTS.md"},
		},
		{
			name:       "does not escape projectRoot",
			files:      files{},
			outerFile:  "outside",
			wantBodies: nil,
			wantNoneOf: []string{"outside"},
		},
		{
			name: "does not escape projectRoot with nested workDir",
			files: files{
				"a/AGENTS.md": "inside",
			},
			workRel:    "a",
			outerFile:  "outside",
			wantBodies: []string{"inside"},
			wantRels:   []string{"a/AGENTS.md"},
			wantNoneOf: []string{"outside"},
		},
		{
			name: "empty skipped at root",
			files: files{
				"AGENTS.md":     "  \n",
				"a/b/AGENTS.md": "nested rules",
			},
			workRel:    "a/b",
			wantBodies: []string{"nested rules"},
			wantRels:   []string{"a/b/AGENTS.md"},
		},
		{
			name: "empty skipped at nested",
			files: files{
				"AGENTS.md":     "root rules",
				"a/b/AGENTS.md": "  \n",
			},
			workRel:    "a/b",
			wantBodies: []string{"root rules"},
			wantRels:   []string{"AGENTS.md"},
		},
		{
			name: "empty skipped both",
			files: files{
				"AGENTS.md":     "  \n",
				"a/b/CLAUDE.md": "\t",
			},
			workRel:    "a/b",
			wantBodies: nil,
		},
		{
			name:       "global still first",
			files:      files{"AGENTS.md": "project rules"},
			globalFile: "global rules",
			wantBodies: []string{"global rules", "project rules"},
			wantRels:   []string{"AGENTS.md"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.globalFile != "" {
				home := t.TempDir()
				t.Setenv("HOME", home)
				strike := filepath.Join(home, ".strike")
				if err := os.MkdirAll(strike, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(strike, "AGENTS.md"), []byte(tt.globalFile), 0o644); err != nil {
					t.Fatal(err)
				}
			} else {
				t.Setenv("HOME", t.TempDir())
			}

			outer := t.TempDir()
			root := filepath.Join(outer, "repo")
			if err := os.MkdirAll(root, 0o755); err != nil {
				t.Fatal(err)
			}
			if tt.outerFile != "" {
				if err := os.WriteFile(filepath.Join(outer, "AGENTS.md"), []byte(tt.outerFile), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			for rel, body := range tt.files {
				path := filepath.Join(root, filepath.FromSlash(rel))
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			workDir := root
			if tt.workRel != "" {
				workDir = filepath.Join(root, filepath.FromSlash(tt.workRel))
				if err := os.MkdirAll(workDir, 0o755); err != nil {
					t.Fatal(err)
				}
			}

			got := LoadInstructions(workDir, root)
			if len(got) != len(tt.wantBodies) {
				t.Fatalf("LoadInstructions returned %d blocks, want %d: %#v", len(got), len(tt.wantBodies), got)
			}
			for i, body := range tt.wantBodies {
				if !strings.Contains(got[i], body) {
					t.Errorf("block %d = %q, want to contain %q", i, got[i], body)
				}
			}
			offset := 0
			if tt.globalFile != "" {
				offset = 1
			}
			for i, rel := range tt.wantRels {
				wantPath := filepath.Join(root, filepath.FromSlash(rel))
				block := got[i+offset]
				if !strings.Contains(block, wantPath) {
					t.Errorf("block %d missing path %s: %q", i+offset, wantPath, block)
				}
			}
			for _, n := range tt.wantNoneOf {
				for _, block := range got {
					if strings.Contains(block, n) {
						t.Errorf("unexpected %q in %#v", n, got)
					}
				}
			}
		})
	}
}

func TestLoadInstructionsEmptyWorkDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".strike"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".strike", "AGENTS.md"), []byte("global only"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := LoadInstructions("", "")
	if len(got) != 1 || !strings.Contains(got[0], "global only") {
		t.Fatalf("empty workDir should return global only, got %#v", got)
	}
}
