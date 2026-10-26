package config

import (
	"path/filepath"
	"strings"
)

// LocalRead is one path strike serve consults while loading config,
// instructions, agents, skills, workflows, themes, and plugins.
// State files the process creates (sessions, memory, auth) are added by the
// server from the stores it actually opened. This list must stay on the same
// helpers Load uses. It does not include ~/.strike/onboarding.json: that file
// is the TUI wizard, and serve does not read it.
type LocalRead struct {
	Path   string
	Group  string // config, instructions, catalog
	Access string // read-if-present
	Detail string
}

// ServeReads returns the paths config.Load and the startup catalogs consult
// for workDir. instructionRoot is the project root used for AGENTS.md /
// CLAUDE.md (the git root when known). An empty instructionRoot uses workDir.
func ServeReads(workDir, instructionRoot string) []LocalRead {
	if instructionRoot == "" {
		instructionRoot = workDir
	}
	var out []LocalRead
	add := func(path, group, detail string) {
		if path == "" {
			return
		}
		out = append(out, LocalRead{
			Path:   path,
			Group:  group,
			Access: "read-if-present",
			Detail: detail,
		})
	}

	add(GlobalPath(), "config", "Global config (JSON or JSONC). Theme, model, permission mode, and notify are written here.")
	for _, p := range mcpFileCandidates(GlobalRoot()) {
		add(p, "config", "Global MCP servers. The first file that exists is the one that is read.")
	}
	for _, p := range containerFileCandidates(GlobalRoot()) {
		add(p, "config", "Global container config. The first file that exists is the one that is read.")
	}
	for _, p := range providersFileCandidates(GlobalRoot()) {
		add(p, "config", "Global provider overlays. The first file that exists is the one that is read.")
	}
	for _, p := range keybindsFileCandidates(GlobalRoot()) {
		add(p, "config", "Global keybinds. The first file that exists overrides keybinds in the config file.")
	}
	if root := GlobalRoot(); root != "" {
		add(filepath.Join(root, "plugins"), "catalog", "Global plugins. Manifests are read at startup. plugins.lock.json can disable one.")
		add(filepath.Join(root, "plugins.lock.json"), "catalog", "Global plugin lockfile.")
		add(filepath.Join(root, "themes"), "catalog", "User theme JSON. The stock Strike palette is built in and is not a file.")
		add(filepath.Join(root, "workflows"), "catalog", "Global workflow JSON.")
	}
	for _, p := range globalInstructionCandidates() {
		add(p, "instructions", "Global instructions. The first non-empty file is read.")
	}
	for _, src := range agentDiscoveryRoots(workDir) {
		add(src.dir, "catalog", agentDetail(src))
	}
	for _, src := range skillDiscoveryRoots(workDir) {
		add(src.dir, "catalog", skillDetail(src))
	}

	if workDir != "" {
		dir := projectRoot(workDir)
		add(filepath.Join(dir, "config"), "config", "Project config. Merged over the global file.")
		for _, p := range mcpFileCandidates(dir) {
			add(p, "config", "Project MCP servers. The first file that exists is the one that is read.")
		}
		for _, p := range containerFileCandidates(dir) {
			add(p, "config", "Project container config. The first file that exists is the one that is read.")
		}
		for _, p := range providersFileCandidates(dir) {
			add(p, "config", "Project provider overlays. The first file that exists is the one that is read.")
		}
		for _, p := range keybindsFileCandidates(dir) {
			add(p, "config", "Project keybinds. The first file that exists overrides the project config keybinds.")
		}
		add(filepath.Join(dir, "plugins"), "catalog", "Project plugins. A project plugin shadows a global plugin with the same id.")
		add(filepath.Join(dir, "plugins.lock.json"), "catalog", "Project plugin lockfile. It can also disable a global plugin.")
		add(filepath.Join(dir, "themes"), "catalog", "Project theme JSON. Later sources override earlier ones with the same id.")
		add(filepath.Join(dir, "workflows"), "catalog", "Project workflow JSON. Project entries override global ones with the same name.")
		for _, d := range instructionDirs(workDir, instructionRoot) {
			add(filepath.Join(d, "AGENTS.md"), "instructions", "Project instructions in this directory. A non-empty AGENTS.md wins over CLAUDE.md.")
			add(filepath.Join(d, "CLAUDE.md"), "instructions", "Project instructions in this directory, read when AGENTS.md is missing or empty.")
		}
	}

	for _, p := range managedFileCandidates(ManagedConfigPath()) {
		add(p, "config", "Managed policy. Applied last, over global and project. A missing file is skipped.")
	}
	add(ManagedDropInDir(), "config", "Managed policy drop-ins. JSON and JSONC files here are merged after the base file.")

	return dedupeReads(out)
}

func agentDetail(src agentSource) string {
	if src.strict == strictLoad {
		return "Agent markdown. Invalid files fail the load."
	}
	return "Agent markdown from a compatible tree. Invalid files are skipped."
}

func skillDetail(src skillSource) string {
	if strings.HasSuffix(src.dir, string(filepath.Separator)+"commands") {
		return "Markdown commands read as skills when they match the skill shape."
	}
	if src.strict == strictLoad {
		return "Skill markdown and SKILL.md directories. Invalid names fail the load."
	}
	return "Skill markdown from a compatible tree. Invalid files are skipped."
}

// instructionDirs lists directories from instructionRoot down to workDir,
// root first. The walk matches LoadInstructions: it never goes above the root.
func instructionDirs(workDir, instructionRoot string) []string {
	start := filepath.Clean(workDir)
	stop := filepath.Clean(instructionRoot)
	if start == "" || stop == "" || !isWithin(start, stop) {
		return nil
	}
	var deepest []string
	dir := start
	for {
		deepest = append(deepest, dir)
		if dir == stop {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir || (!isWithin(parent, stop) && parent != stop) {
			break
		}
		dir = parent
	}
	for i, j := 0, len(deepest)-1; i < j; i, j = i+1, j-1 {
		deepest[i], deepest[j] = deepest[j], deepest[i]
	}
	return deepest
}

func dedupeReads(in []LocalRead) []LocalRead {
	seen := make(map[string]struct{}, len(in))
	out := make([]LocalRead, 0, len(in))
	for _, f := range in {
		if f.Path == "" {
			continue
		}
		if _, ok := seen[f.Path]; ok {
			continue
		}
		seen[f.Path] = struct{}{}
		out = append(out, f)
	}
	return out
}
