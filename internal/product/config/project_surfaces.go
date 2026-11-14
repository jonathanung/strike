package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jonathanung/strike/internal/integrate/plugin"
)

// ProjectScope returns the directory the project-layer loaders (Load,
// LoadAgentsWithError, LoadSkillsWithError, LoadWorkflows, LoadInstructions,
// DiscoverPlugins, plugin executable compilation) should receive for workDir.
// A trusted workspace loads from workDir; an untrusted one gets "" so only
// builtin + global layers apply and nothing repo-supplied is read or started
// (#1352). Stores keyed by project identity keep using workDir — this only
// governs configuration input.
func ProjectScope(workDir string, trusted bool) string {
	if !trusted {
		return ""
	}
	return workDir
}

// ProjectExecutable is one repo-supplied process definition (MCP server, LSP
// server, or external harness) as it would be launched. Env and headers are
// deliberately omitted — they may carry secrets and the user approves the
// command, not its environment.
type ProjectExecutable struct {
	Name    string   `json:"name"`
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
	// URL is set for HTTP MCP servers (no local process).
	URL string `json:"url,omitempty"`
}

// CommandLine renders the executable the way a shell would show it.
func (e ProjectExecutable) CommandLine() string {
	if e.Command == "" && e.URL != "" {
		return e.URL
	}
	parts := append([]string{e.Command}, e.Args...)
	return strings.TrimSpace(strings.Join(parts, " "))
}

// ProjectHook is one repo-supplied hook entry by lifecycle event.
type ProjectHook struct {
	Event string `json:"event"`
	// Command is the shell command for shell hooks; empty for declarative rules.
	Command string `json:"command,omitempty"`
	// Action is log|block|notify for declarative rules; empty for shell hooks.
	Action string `json:"action,omitempty"`
}

// ProjectFile is one repo-supplied definition file with its parsed name
// (agent, skill, workflow). Name falls back to the file stem when the file
// does not parse so a malformed entry is still listed.
type ProjectFile struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// ProjectSurfaces enumerates everything a workspace's own files would load
// into a session, computed without executing, registering, or prompting any
// of it. This is the list a user approves when trusting a workspace (#1352).
type ProjectSurfaces struct {
	// Root is the workspace root the enumeration was computed for.
	Root string `json:"root"`
	// ConfigFiles are project config/sidecar files that exist (.strike/config,
	// mcp.jsonc, providers.jsonc, keybinds.jsonc, container.jsonc).
	ConfigFiles []string `json:"configFiles,omitempty"`
	// ConfigKeys are the top-level keys set in .strike/config (sorted; "$schema"
	// excluded). Scalars here override the global file when trusted.
	ConfigKeys []string `json:"configKeys,omitempty"`
	// PermissionRules counts permissions[] entries in .strike/config.
	PermissionRules int                 `json:"permissionRules,omitempty"`
	MCP             []ProjectExecutable `json:"mcp,omitempty"`
	LSP             []ProjectExecutable `json:"lsp,omitempty"`
	Harnesses       []ProjectExecutable `json:"harnesses,omitempty"`
	Hooks           []ProjectHook       `json:"hooks,omitempty"`
	Agents          []ProjectFile       `json:"agents,omitempty"`
	Skills          []ProjectFile       `json:"skills,omitempty"`
	Workflows       []ProjectFile       `json:"workflows,omitempty"`
	// Plugins are project-scope plugin ids under .strike/plugins (their
	// lockfile, including any trust record, lives inside the repo).
	Plugins []string `json:"plugins,omitempty"`
	// Steering lists non-empty AGENTS.md / CLAUDE.md files from the project
	// root down to workDir, root first.
	Steering []string `json:"steering,omitempty"`
	// Errors are non-fatal enumeration problems (unparseable files). The
	// surface is still reported so the user sees that something is there.
	Errors []string `json:"errors,omitempty"`
}

// Empty reports whether the workspace supplies nothing a trust decision
// would gate.
func (s ProjectSurfaces) Empty() bool {
	return len(s.ConfigFiles) == 0 && len(s.ConfigKeys) == 0 && s.PermissionRules == 0 &&
		len(s.MCP) == 0 && len(s.LSP) == 0 && len(s.Harnesses) == 0 && len(s.Hooks) == 0 &&
		len(s.Agents) == 0 && len(s.Skills) == 0 && len(s.Workflows) == 0 &&
		len(s.Plugins) == 0 && len(s.Steering) == 0 && len(s.Errors) == 0
}

// Summary is a one-line count of the gated surfaces, e.g.
// "2 MCP servers, 1 hook, 3 agents, 1 steering file".
func (s ProjectSurfaces) Summary() string {
	var parts []string
	add := func(n int, singular, plural string) {
		switch {
		case n == 1:
			parts = append(parts, "1 "+singular)
		case n > 1:
			parts = append(parts, fmt.Sprintf("%d %s", n, plural))
		}
	}
	add(len(s.ConfigKeys), "config override", "config overrides")
	add(s.PermissionRules, "permission rule", "permission rules")
	add(len(s.MCP), "MCP server", "MCP servers")
	add(len(s.LSP), "LSP server", "LSP servers")
	add(len(s.Harnesses), "harness", "harnesses")
	add(len(s.Hooks), "hook", "hooks")
	add(len(s.Agents), "agent", "agents")
	add(len(s.Skills), "skill", "skills")
	add(len(s.Workflows), "workflow", "workflows")
	add(len(s.Plugins), "plugin", "plugins")
	add(len(s.Steering), "steering file", "steering files")
	add(len(s.Errors), "unreadable file", "unreadable files")
	if len(parts) == 0 {
		return "no repo-supplied config"
	}
	return strings.Join(parts, ", ")
}

// EnumerateProjectSurfaces lists what workDir's own files would load without
// loading them. root bounds the steering walk (git top level when known; ""
// means workDir). Only read errors on existing files are reported, via
// Errors — the function never fails on a hostile or malformed repo, so a
// caller can always show the user what is there.
func EnumerateProjectSurfaces(workDir, root string) ProjectSurfaces {
	out := ProjectSurfaces{Root: root}
	if workDir == "" {
		return out
	}
	if root == "" {
		root = workDir
		out.Root = workDir
	}
	dir := projectRoot(workDir)
	warn := func(path string, err error) {
		out.Errors = append(out.Errors, fmt.Sprintf("%s: %v", path, err))
	}

	// Project config: files present, top-level keys, permissions, hooks,
	// legacy inline mcp/lsp/harnesses.
	cfgPath := filepath.Join(dir, "config")
	if data, err := os.ReadFile(cfgPath); err == nil {
		out.ConfigFiles = append(out.ConfigFiles, cfgPath)
		out.ConfigKeys = topLevelKeys(data)
		layer, err := read(cfgPath)
		if err != nil {
			warn(cfgPath, err)
		} else {
			out.PermissionRules = len(layer.Permissions)
			for _, h := range layer.Hooks {
				out.Hooks = append(out.Hooks, ProjectHook{Event: h.Event, Command: h.Command, Action: h.Action})
			}
			out.MCP = append(out.MCP, mcpExecutables(layer.MCP.Servers)...)
			out.LSP = append(out.LSP, lspExecutables(layer.LSP.Servers)...)
			out.Harnesses = append(out.Harnesses, harnessExecutables(layer.Harnesses)...)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		warn(cfgPath, err)
	}
	for _, p := range mcpFileCandidates(dir) {
		mc, err := ReadMCPFile(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		out.ConfigFiles = append(out.ConfigFiles, p)
		if err != nil {
			warn(p, err)
		} else {
			out.MCP = append(out.MCP, mcpExecutables(mc.Servers)...)
		}
		break // same first-existing rule as loadMCPFileLayer
	}
	for _, p := range firstPresent(providersFileCandidates(dir), keybindsFileCandidates(dir), containerFileCandidates(dir)) {
		out.ConfigFiles = append(out.ConfigFiles, p)
	}

	// Agents, skills, workflows from project-scoped roots only.
	_, agentRoots := splitAgentRoots(workDir)
	for _, src := range agentRoots {
		for _, path := range markdownFiles(src.dir) {
			name := fileStem(path)
			if a, err := parseAgentFile(path); err != nil {
				warn(path, err)
			} else if a != nil {
				name = a.Name
			} else {
				continue
			}
			out.Agents = append(out.Agents, ProjectFile{Name: name, Path: path})
		}
	}
	_, skillRoots := splitSkillRoots(workDir)
	for _, src := range skillRoots {
		for _, path := range markdownSkillFiles(src.dir) {
			name := fileStem(path)
			if sk, err := parseSkillFile(path); err != nil {
				warn(path, err)
			} else if sk != nil {
				name = sk.Name
			} else {
				continue
			}
			out.Skills = append(out.Skills, ProjectFile{Name: name, Path: path})
		}
	}
	wfDir := filepath.Join(dir, "workflows")
	if entries, err := os.ReadDir(wfDir); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			path := filepath.Join(wfDir, e.Name())
			name := fileStem(path)
			if w, err := LoadWorkflowFileSource(path, WorkflowSourceProject); err != nil {
				warn(path, err)
			} else {
				name = w.Name
			}
			out.Workflows = append(out.Workflows, ProjectFile{Name: name, Path: path})
		}
	}

	// Project-scope plugins (ids only; their executables are digest-gated
	// separately but the lockfile is repo-controlled).
	for _, p := range DiscoverPlugins(workDir).Plugins {
		if p.Source == plugin.ScopeProject {
			out.Plugins = append(out.Plugins, p.ID)
		}
	}
	sort.Strings(out.Plugins)

	out.Steering = projectInstructionPaths(workDir, root)
	return out
}

// topLevelKeys returns sorted top-level object keys of a JSON/JSONC document,
// excluding "$schema". Unparseable input yields nil (reported by the caller
// through read()).
func topLevelKeys(data []byte) []string {
	stripped, err := stripJSONC(data)
	if err != nil {
		return nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(stripped, &raw); err != nil {
		return nil
	}
	keys := make([]string, 0, len(raw))
	for k := range raw {
		if k == "$schema" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func mcpExecutables(servers map[string]MCPServer) []ProjectExecutable {
	out := make([]ProjectExecutable, 0, len(servers))
	for _, name := range sortedServerNames(servers) {
		s := servers[name]
		out = append(out, ProjectExecutable{Name: name, Command: s.Command, Args: append([]string(nil), s.Args...), URL: s.URL})
	}
	return out
}

func lspExecutables(servers map[string]LSPServer) []ProjectExecutable {
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]ProjectExecutable, 0, len(names))
	for _, name := range names {
		s := servers[name]
		out = append(out, ProjectExecutable{Name: name, Command: s.Command, Args: append([]string(nil), s.Args...)})
	}
	return out
}

func harnessExecutables(hs map[string]HarnessConfig) []ProjectExecutable {
	names := make([]string, 0, len(hs))
	for name := range hs {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]ProjectExecutable, 0, len(names))
	for _, name := range names {
		h := hs[name]
		out = append(out, ProjectExecutable{Name: name, Command: h.Command, Args: append([]string(nil), h.Args...)})
	}
	return out
}

func sortedServerNames(servers map[string]MCPServer) []string {
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// firstPresent returns, for each candidate group, the first path that exists.
func firstPresent(groups ...[]string) []string {
	var out []string
	for _, group := range groups {
		for _, p := range group {
			if _, err := os.Stat(p); err == nil {
				out = append(out, p)
				break
			}
		}
	}
	return out
}

func fileStem(path string) string {
	base := filepath.Base(path)
	if base == "SKILL.md" {
		return filepath.Base(filepath.Dir(path))
	}
	return strings.TrimSuffix(base, filepath.Ext(base))
}
