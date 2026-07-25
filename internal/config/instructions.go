package config

import (
	"os"
	"path/filepath"
	"strings"
)

// instructionNames are discovered like opencode: first match wins per scope.
var instructionNames = []string{"AGENTS.md", "CLAUDE.md"}

// LoadInstructions returns instruction blocks for the system prompt, matching
// opencode's Instruction.system layering:
//  1. first existing global file among ~/.strike/AGENTS.md, ~/.claude/CLAUDE.md
//  2. first project match walking from workDir up through projectRoot
//
// Each non-empty file becomes one block prefixed with its path.
func LoadInstructions(workDir, projectRoot string) []string {
	var out []string
	if block := readFirstInstruction(globalInstructionCandidates()); block != "" {
		out = append(out, block)
	}
	if workDir == "" {
		return out
	}
	if projectRoot == "" {
		projectRoot = workDir
	}
	if block := findProjectInstruction(workDir, projectRoot); block != "" {
		out = append(out, block)
	}
	return out
}

func globalInstructionCandidates() []string {
	var paths []string
	if root := GlobalRoot(); root != "" {
		paths = append(paths, filepath.Join(root, "AGENTS.md"))
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		paths = append(paths, filepath.Join(home, ".claude", "CLAUDE.md"))
	}
	return paths
}

func readFirstInstruction(paths []string) string {
	for _, path := range paths {
		if block := readInstruction(path); block != "" {
			return block
		}
	}
	return ""
}

// findProjectInstruction walks from start up to stop (inclusive), returning the
// first AGENTS.md or CLAUDE.md found. When start is inside stop, it does not
// walk above stop.
func findProjectInstruction(start, stop string) string {
	start = filepath.Clean(start)
	stop = filepath.Clean(stop)
	bounded := stop != "" && isWithin(start, stop)
	dir := start
	for {
		for _, name := range instructionNames {
			if block := readInstruction(filepath.Join(dir, name)); block != "" {
				return block
			}
		}
		if bounded && dir == stop {
			return ""
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		if bounded && !isWithin(parent, stop) && parent != stop {
			return ""
		}
		dir = parent
	}
}

func isWithin(path, root string) bool {
	path = filepath.Clean(path)
	root = filepath.Clean(root)
	if path == root {
		return true
	}
	prefix := root + string(filepath.Separator)
	return strings.HasPrefix(path, prefix)
}

func readInstruction(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	body := strings.TrimSpace(string(data))
	if body == "" {
		return ""
	}
	return "Instructions from: " + path + "\n" + body
}
