package config

import (
	"os"
	"path/filepath"
	"strings"
)

// instructionNames are discovered per directory: first non-empty match wins.
var instructionNames = []string{"AGENTS.md", "CLAUDE.md"}

// LoadInstructions returns instruction blocks for the system prompt:
//  1. first existing global file among ~/.strike/AGENTS.md, ~/.claude/CLAUDE.md
//  2. every project match from projectRoot down the path to workDir (inclusive),
//     root first and deepest last so nested files specialize
//
// Each non-empty file becomes one block prefixed with its path. Discovery
// never walks above projectRoot. /init still writes only the workDir root
// AGENTS.md; this function is the load path.
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
	out = append(out, collectProjectInstructions(workDir, projectRoot)...)
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

// collectProjectInstructions walks from start up to stop (inclusive), then
// returns blocks in root-first, deepest-last order. When start is not inside
// stop, nothing is collected (the walk never escapes projectRoot).
func collectProjectInstructions(start, stop string) []string {
	start = filepath.Clean(start)
	stop = filepath.Clean(stop)
	if stop == "" || !isWithin(start, stop) {
		return nil
	}

	var deepestFirst []string
	dir := start
	for {
		if block := readDirInstruction(dir); block != "" {
			deepestFirst = append(deepestFirst, block)
		}
		if dir == stop {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		if !isWithin(parent, stop) && parent != stop {
			break
		}
		dir = parent
	}

	for i, j := 0, len(deepestFirst)-1; i < j; i, j = i+1, j-1 {
		deepestFirst[i], deepestFirst[j] = deepestFirst[j], deepestFirst[i]
	}
	return deepestFirst
}

func readDirInstruction(dir string) string {
	for _, name := range instructionNames {
		if block := readInstruction(filepath.Join(dir, name)); block != "" {
			return block
		}
	}
	return ""
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
