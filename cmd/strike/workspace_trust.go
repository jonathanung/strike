package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/jonathanung/strike/internal/product/config"
	"github.com/jonathanung/strike/internal/trust/workspacetrust"
)

// workspaceTrust is the assembly-time trust decision for one workspace
// (#1352). Untrusted workspaces run on builtin + global config: nothing under
// the repo (.strike/config, mcp.jsonc, LSP, harnesses, hooks, agents, skills,
// workflows, project plugins, AGENTS.md/CLAUDE.md) is read or started. The
// decision lives in ~/.strike/workspace-trust.json — never inside the repo.
type workspaceTrust struct {
	// root is the canonical workspace root the decision is keyed on (git top
	// level when known, else the launch directory).
	root    string
	trusted bool
	// surfaces is what the repo would load, enumerated without loading it.
	// Only populated when untrusted (that is when it has to be shown).
	surfaces config.ProjectSurfaces
}

// resolveWorkspaceTrust looks up root in store and, when untrusted,
// enumerates the repo's own config so the skip can be reported precisely.
func resolveWorkspaceTrust(store *workspacetrust.Store, workDir, root string) workspaceTrust {
	if root == "" {
		root = workDir
	}
	wt := workspaceTrust{root: root, trusted: store != nil && store.Trusted(root)}
	if !wt.trusted {
		wt.surfaces = config.EnumerateProjectSurfaces(workDir, root)
	}
	return wt
}

// scope returns the directory project-layer loaders receive for dir: dir when
// trusted, "" when untrusted (global + builtin only).
func (wt workspaceTrust) scope(dir string) string {
	return config.ProjectScope(dir, wt.trusted)
}

// inert reports that repo config exists and was skipped as untrusted.
func (wt workspaceTrust) inert() bool {
	return !wt.trusted && !wt.surfaces.Empty()
}

// reason is the one-line operator/transcript explanation of the skip.
func (wt workspaceTrust) reason() string {
	return fmt.Sprintf("untrusted workspace: repo config inert (%s); run `strike trust` in %s to enable it", wt.surfaces.Summary(), wt.root)
}

// notice is the startup message frontends show when repo config was gated;
// "" otherwise.
func (wt workspaceTrust) notice() string {
	if !wt.inert() {
		return ""
	}
	return "strike: " + wt.reason()
}

// writeProjectSurfaces renders the enumeration as the list a user approves:
// names plus command lines, never env or headers.
func writeProjectSurfaces(w io.Writer, s config.ProjectSurfaces) {
	if s.Empty() {
		fmt.Fprintln(w, "  (no repo-supplied config)")
		return
	}
	section := func(title string, lines []string) {
		if len(lines) == 0 {
			return
		}
		fmt.Fprintf(w, "  %s:\n", title)
		for _, l := range lines {
			fmt.Fprintf(w, "    %s\n", l)
		}
	}
	execLines := func(items []config.ProjectExecutable) []string {
		out := make([]string, 0, len(items))
		for _, e := range items {
			out = append(out, fmt.Sprintf("%s: %s", e.Name, e.CommandLine()))
		}
		return out
	}
	fileLines := func(items []config.ProjectFile) []string {
		out := make([]string, 0, len(items))
		for _, f := range items {
			out = append(out, fmt.Sprintf("%s (%s)", f.Name, f.Path))
		}
		return out
	}
	var cfgLines []string
	if len(s.ConfigKeys) > 0 {
		cfgLines = append(cfgLines, "overrides: "+strings.Join(s.ConfigKeys, ", "))
	}
	if s.PermissionRules > 0 {
		cfgLines = append(cfgLines, fmt.Sprintf("permission rules: %d", s.PermissionRules))
	}
	cfgLines = append(cfgLines, s.ConfigFiles...)
	section("Config", cfgLines)
	section("MCP servers", execLines(s.MCP))
	section("LSP servers", execLines(s.LSP))
	section("Harnesses", execLines(s.Harnesses))
	section("Providers", execLines(s.Providers))
	hookLines := make([]string, 0, len(s.Hooks))
	for _, h := range s.Hooks {
		if h.Command != "" {
			hookLines = append(hookLines, fmt.Sprintf("%s: %s", h.Event, h.Command))
		} else {
			hookLines = append(hookLines, fmt.Sprintf("%s: %s (rule)", h.Event, h.Action))
		}
	}
	section("Hooks", hookLines)
	section("Agents", fileLines(s.Agents))
	section("Skills", fileLines(s.Skills))
	section("Workflows", fileLines(s.Workflows))
	section("Plugins", s.Plugins)
	section("Steering", s.Steering)
	section("Unreadable", s.Errors)
}

// serveTrustAtStart returns the lookup strike serve hands the cockpit so it
// can say a grant or revoke takes effect on the next start (#1781). Only the
// workspace this daemon assembled is known; every other root reports
// known=false.
func serveTrustAtStart(root string, trusted bool) func(string) (bool, bool) {
	want, err := workspacetrust.CanonicalRoot(root)
	if err != nil {
		return nil
	}
	return func(r string) (bool, bool) {
		got, err := workspacetrust.CanonicalRoot(r)
		if err != nil || got != want {
			return false, false
		}
		return trusted, true
	}
}
