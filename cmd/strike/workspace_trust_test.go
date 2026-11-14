package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonathanung/strike/internal/product/config"
	"github.com/jonathanung/strike/internal/trust/workspacetrust"
)

// writeRepoConfig lays out a repo shipping one of every gated surface.
func writeRepoConfig(t *testing.T, repo string) {
	t.Helper()
	files := map[string]string{
		".strike/config":           `{"systemPrompt": "repo", "permissions": [{"permission": "bash", "pattern": "*", "action": "allow"}], "hooks": [{"event": "pre_tool_use", "command": "echo hook"}]}`,
		".strike/mcp.jsonc":        `{"evil": {"command": "npx", "args": ["-y", "evil"], "env": {"SECRET": "x"}}}`,
		".strike/agents/repo-a.md": "---\ndescription: a\n---\nAgent body.\n",
		"AGENTS.md":                "repo steering",
	}
	for rel, body := range files {
		p := filepath.Join(repo, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func openTestTrustStore(t *testing.T, home string) *workspacetrust.Store {
	t.Helper()
	store, err := workspacetrust.Open(workspacetrust.DefaultPath(filepath.Join(home, ".strike")))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestResolveWorkspaceTrustUntrustedGatesEverything(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	config.ResetPluginCache()
	repo := t.TempDir()
	writeRepoConfig(t, repo)
	store := openTestTrustStore(t, home)

	wt := resolveWorkspaceTrust(store, repo, repo)
	if wt.trusted {
		t.Fatal("fresh repo must be untrusted")
	}
	if !wt.inert() {
		t.Fatal("inert() = false with repo config present")
	}
	if wt.scope(repo) != "" {
		t.Fatalf("scope = %q, want empty for untrusted", wt.scope(repo))
	}
	if wt.scope(filepath.Join(repo, ".strike", "worktrees", "x")) != "" {
		t.Fatal("session worktree scope must also be empty when untrusted")
	}
	if len(wt.surfaces.MCP) != 1 || wt.surfaces.MCP[0].CommandLine() != "npx -y evil" {
		t.Errorf("surfaces.MCP = %+v", wt.surfaces.MCP)
	}
	for _, want := range []string{"untrusted workspace", "1 MCP server", "1 agent", "1 steering file", "strike trust", repo} {
		if !strings.Contains(wt.reason(), want) {
			t.Errorf("reason %q missing %q", wt.reason(), want)
		}
	}
	if strings.Contains(wt.reason(), "SECRET") {
		t.Error("reason must not leak env values")
	}
	if n := wt.notice(); !strings.HasPrefix(n, "strike: untrusted workspace") {
		t.Errorf("notice = %q", n)
	}

	// The scope the gate hands to the loaders yields global-only config.
	cfg, err := config.Load(wt.scope(repo))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SystemPrompt != "" || len(cfg.MCP.Servers) != 0 || len(cfg.Permissions) != 0 || len(cfg.Hooks) != 0 {
		t.Errorf("untrusted scope loaded repo config: %+v", cfg)
	}
	agents, err := config.LoadAgentsWithError(wt.scope(repo))
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range agents {
		if a.Name == "repo-a" {
			t.Error("repo agent loaded while untrusted")
		}
	}
	if blocks := config.LoadInstructions(wt.scope(repo), repo); len(blocks) != 0 {
		t.Errorf("steering loaded while untrusted: %v", blocks)
	}
}

func TestResolveWorkspaceTrustTrustedLoadsRepoConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	config.ResetPluginCache()
	repo := t.TempDir()
	writeRepoConfig(t, repo)
	store := openTestTrustStore(t, home)
	if err := store.Trust(repo); err != nil {
		t.Fatal(err)
	}

	wt := resolveWorkspaceTrust(store, repo, repo)
	if !wt.trusted || wt.inert() {
		t.Fatalf("trusted=%v inert=%v", wt.trusted, wt.inert())
	}
	if wt.scope(repo) != repo {
		t.Fatalf("scope = %q, want %q", wt.scope(repo), repo)
	}
	if n := wt.notice(); n != "" {
		t.Errorf("trusted workspace produced a notice: %q", n)
	}
	cfg, err := config.Load(wt.scope(repo))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SystemPrompt != "repo" || len(cfg.MCP.Servers) != 1 || len(cfg.Permissions) != 1 {
		t.Errorf("trusted scope lost repo config: systemPrompt=%q mcp=%d perms=%d", cfg.SystemPrompt, len(cfg.MCP.Servers), len(cfg.Permissions))
	}

	// Revoke → inert again, same process.
	if err := store.Revoke(repo); err != nil {
		t.Fatal(err)
	}
	wt = resolveWorkspaceTrust(store, repo, repo)
	if wt.trusted || !wt.inert() {
		t.Fatalf("after revoke trusted=%v inert=%v", wt.trusted, wt.inert())
	}
}

func TestResolveWorkspaceTrustUntrustedBareRepoIsNotInert(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	config.ResetPluginCache()
	repo := t.TempDir()
	store := openTestTrustStore(t, home)

	wt := resolveWorkspaceTrust(store, repo, "")
	if wt.trusted {
		t.Fatal("must be untrusted")
	}
	if wt.inert() {
		t.Fatal("nothing to gate → not inert (no notice, no transcript event)")
	}
	if wt.root != repo {
		t.Errorf("root = %q, want workDir fallback", wt.root)
	}
	if wt.scope(repo) != "" {
		t.Error("scope must still be empty: untrusted is the default even with nothing present")
	}
	if n := wt.notice(); n != "" {
		t.Errorf("notice for empty repo: %q", n)
	}
	// nil store is fail-closed too.
	if resolveWorkspaceTrust(nil, repo, repo).trusted {
		t.Fatal("nil store must not trust")
	}
}

func TestResolveWorkspaceTrustSubdirUsesRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	config.ResetPluginCache()
	root := t.TempDir()
	sub := filepath.Join(root, "pkg", "x")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	store := openTestTrustStore(t, home)
	if err := store.Trust(root); err != nil {
		t.Fatal(err)
	}
	// Launched from a subdirectory: trust is keyed by the project root.
	if wt := resolveWorkspaceTrust(store, sub, root); !wt.trusted {
		t.Fatal("trust on the project root must cover a sub-launch")
	}
	// A different repo (trusted root's sibling) stays untrusted.
	if wt := resolveWorkspaceTrust(store, t.TempDir(), ""); wt.trusted {
		t.Fatal("sibling repo inherited trust")
	}
}

func TestWriteProjectSurfacesRendersNamesAndCommandLines(t *testing.T) {
	var buf bytes.Buffer
	writeProjectSurfaces(&buf, config.ProjectSurfaces{})
	if !strings.Contains(buf.String(), "no repo-supplied config") {
		t.Errorf("empty render = %q", buf.String())
	}
	buf.Reset()
	writeProjectSurfaces(&buf, config.ProjectSurfaces{
		ConfigFiles:     []string{"/r/.strike/config"},
		ConfigKeys:      []string{"permissionMode", "systemPrompt"},
		PermissionRules: 2,
		MCP:             []config.ProjectExecutable{{Name: "evil", Command: "npx", Args: []string{"-y", "evil"}}, {Name: "remote", URL: "https://x/sse"}},
		LSP:             []config.ProjectExecutable{{Name: "gopls", Command: "gopls"}},
		Harnesses:       []config.ProjectExecutable{{Name: "runner", Command: "./run"}},
		Providers:       []config.ProjectExecutable{{Name: "acme", URL: "https://proxy.example/v1"}},
		Hooks:           []config.ProjectHook{{Event: "pre_tool_use", Command: "echo hi"}, {Event: "post_tool_use", Action: "log"}},
		Agents:          []config.ProjectFile{{Name: "reviewer", Path: "/r/.strike/agents/reviewer.md"}},
		Skills:          []config.ProjectFile{{Name: "deploy", Path: "/r/.strike/skills/deploy.md"}},
		Workflows:       []config.ProjectFile{{Name: "ship", Path: "/r/.strike/workflows/ship.json"}},
		Plugins:         []string{"acme.p"},
		Steering:        []string{"/r/AGENTS.md"},
		Errors:          []string{"/r/.strike/workflows/broken.json: parse"},
	})
	out := buf.String()
	for _, want := range []string{
		"Config:", "overrides: permissionMode, systemPrompt", "permission rules: 2", "/r/.strike/config",
		"MCP servers:", "evil: npx -y evil", "remote: https://x/sse",
		"LSP servers:", "gopls: gopls",
		"Harnesses:", "runner: ./run",
		"Providers:", "acme: https://proxy.example/v1",
		"Hooks:", "pre_tool_use: echo hi", "post_tool_use: log (rule)",
		"Agents:", "reviewer (/r/.strike/agents/reviewer.md)",
		"Skills:", "deploy (", "Workflows:", "ship (",
		"Plugins:", "acme.p", "Steering:", "/r/AGENTS.md",
		"Unreadable:", "broken.json",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q:\n%s", want, out)
		}
	}
}
