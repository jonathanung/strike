package config

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// discoveryStrict is true for strike-native trees: invalid names/effort/perms
// fail the whole load. External trees (.claude / .opencode) warn and skip.
type discoveryStrict bool

const (
	strictLoad discoveryStrict = true
	softLoad   discoveryStrict = false
)

// agentSource is one agents directory in the merge chain (later wins by name).
type agentSource struct {
	dir    string
	strict discoveryStrict
}

// skillSource is one skills directory in the merge chain (later wins by name).
type skillSource struct {
	dir    string
	strict discoveryStrict
}

// agentDiscoveryRoots returns agent markdown roots in merge order.
// Later entries override earlier ones with the same name:
//
//  1. ~/.strike/agents
//  2. ~/.claude/agents
//  3. ~/.config/opencode/agents (or $XDG_CONFIG_HOME/opencode/agents)
//  4. ~/.opencode/agents
//  5. <project>/.strike/agents
//  6. <project>/.claude/agents
//  7. <project>/.opencode/agent and .../agents (singular then plural)
//
// Built-ins are merged separately before disk layers.
func agentDiscoveryRoots(workDir string) []agentSource {
	var out []agentSource
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		out = append(out,
			agentSource{filepath.Join(home, ".strike", "agents"), strictLoad},
			agentSource{filepath.Join(home, ".claude", "agents"), softLoad},
			agentSource{filepath.Join(opencodeConfigHome(home), "agents"), softLoad},
			agentSource{filepath.Join(home, ".opencode", "agents"), softLoad},
		)
	}
	if workDir != "" {
		out = append(out,
			agentSource{filepath.Join(workDir, ".strike", "agents"), strictLoad},
			agentSource{filepath.Join(workDir, ".claude", "agents"), softLoad},
			agentSource{filepath.Join(workDir, ".opencode", "agent"), softLoad},
			agentSource{filepath.Join(workDir, ".opencode", "agents"), softLoad},
		)
	}
	return out
}

// skillDiscoveryRoots returns skill markdown roots in merge order.
// Later entries override earlier ones with the same name:
//
//  1. ~/.strike/skills
//  2. ~/.claude/skills
//  3. ~/.config/opencode/skills (or $XDG_CONFIG_HOME/opencode/skills)
//  4. ~/.opencode/skills
//  5. <project>/.strike/skills
//  6. <project>/.claude/skills
//  7. <project>/.claude/commands (markdown commands → skills when compatible)
//  8. <project>/.opencode/skills
//
// Built-ins are merged separately before disk layers.
// Each root accepts flat *.md files and <name>/SKILL.md directories.
func skillDiscoveryRoots(workDir string) []skillSource {
	var out []skillSource
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		out = append(out,
			skillSource{filepath.Join(home, ".strike", "skills"), strictLoad},
			skillSource{filepath.Join(home, ".claude", "skills"), softLoad},
			skillSource{filepath.Join(opencodeConfigHome(home), "skills"), softLoad},
			skillSource{filepath.Join(home, ".opencode", "skills"), softLoad},
		)
	}
	if workDir != "" {
		out = append(out,
			skillSource{filepath.Join(workDir, ".strike", "skills"), strictLoad},
			skillSource{filepath.Join(workDir, ".claude", "skills"), softLoad},
			skillSource{filepath.Join(workDir, ".claude", "commands"), softLoad},
			skillSource{filepath.Join(workDir, ".opencode", "skills"), softLoad},
		)
	}
	return out
}

func opencodeConfigHome(home string) string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "opencode")
	}
	return filepath.Join(home, ".config", "opencode")
}

// markdownSkillFiles lists skill definition paths under dir:
// flat *.md files and one level of <subdir>/SKILL.md (Claude/OpenCode layout).
// Non-markdown plugin code is never returned.
func markdownSkillFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var files []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			for _, skillName := range []string{"SKILL.md", "skill.md"} {
				p := filepath.Join(dir, name, skillName)
				if st, err := os.Stat(p); err == nil && !st.IsDir() {
					files = append(files, p)
					break
				}
			}
			continue
		}
		if strings.HasSuffix(name, ".md") {
			files = append(files, filepath.Join(dir, name))
		}
	}
	sort.Strings(files)
	return files
}

// skillNameFromPath picks a default skill name from path: directory name when
// the file is SKILL.md/skill.md, otherwise the basename without .md.
func skillNameFromPath(path string) string {
	base := filepath.Base(path)
	lower := strings.ToLower(base)
	if lower == "skill.md" {
		return filepath.Base(filepath.Dir(path))
	}
	return strings.TrimSuffix(base, ".md")
}
