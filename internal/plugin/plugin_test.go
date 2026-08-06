package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseManifest_PassiveExample(t *testing.T) {
	raw := `{
  "schemaVersion": 1,
  "id": "community.nord-extras",
  "version": "0.3.1",
  "name": "Nord extras",
  "description": "Additional Nord-family theme and a read-only explore agent.",
  "strike": { "min": "0.2.0" },
  "capabilities": ["themes", "agents"],
  "contributions": {
    "themes": [{ "path": "themes/nord-soft.json" }],
    "agents": [{ "path": "agents/explore-nord.md" }]
  }
}`
	m, err := ParseManifest([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != "community.nord-extras" || m.Version != "0.3.1" {
		t.Fatalf("got id=%s ver=%s", m.ID, m.Version)
	}
	if len(m.Contributions.Themes) != 1 || len(m.Contributions.Agents) != 1 {
		t.Fatalf("contributions: %+v", m.Contributions)
	}
}

func TestParseManifest_RejectsUnknownField(t *testing.T) {
	raw := `{
  "schemaVersion": 1,
  "id": "acme.pack",
  "version": "1.0.0",
  "name": "Pack",
  "strike": { "min": "0.2.0" },
  "contributions": { "agents": [{ "path": "agents/a.md" }] },
  "extra": true
}`
	if _, err := ParseManifest([]byte(raw)); err == nil {
		t.Fatal("expected unknown field error")
	}
}

func TestParseManifest_RejectsPathTraversal(t *testing.T) {
	raw := `{
  "schemaVersion": 1,
  "id": "acme.pack",
  "version": "1.0.0",
  "name": "Pack",
  "strike": { "min": "0.2.0" },
  "contributions": { "agents": [{ "path": "../escape.md" }] }
}`
	if _, err := ParseManifest([]byte(raw)); err == nil || !strings.Contains(err.Error(), "..") {
		t.Fatalf("want path error, got %v", err)
	}
}

func TestParseManifest_RejectsDuplicatePaths(t *testing.T) {
	raw := `{
  "schemaVersion": 1,
  "id": "acme.pack",
  "version": "1.0.0",
  "name": "Pack",
  "strike": { "min": "0.2.0" },
  "contributions": {
    "agents": [
      { "path": "agents/a.md" },
      { "path": "agents/a.md" }
    ]
  }
}`
	if _, err := ParseManifest([]byte(raw)); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("want duplicate error, got %v", err)
	}
}

func TestValidatePluginID(t *testing.T) {
	ok := []string{"acme.review-pack", "community.nord-extras", "myplugin"}
	for _, id := range ok {
		if err := ValidatePluginID(id); err != nil {
			t.Errorf("%s: %v", id, err)
		}
	}
	bad := []string{"", "Acme.Pack", ".hidden", "a", "has spaces"}
	for _, id := range bad {
		if err := ValidatePluginID(id); err == nil {
			t.Errorf("%q: expected error", id)
		}
	}
}

func TestResolveUnderRoot_RejectsEscape(t *testing.T) {
	root := t.TempDir()
	if _, err := ResolveUnderRoot(root, "../outside"); err == nil {
		t.Fatal("expected escape error")
	}
	if _, err := ResolveUnderRoot(root, "/etc/passwd"); err == nil {
		t.Fatal("expected absolute path error")
	}
	// Valid nested path (file need not exist for clean join when missing).
	got, err := ResolveUnderRoot(root, "agents/x.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, root) {
		t.Fatalf("got %s not under %s", got, root)
	}
}

func TestResolveUnderRoot_SymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skip("symlinks not supported")
	}
	if _, err := ResolveUnderRoot(root, "link/secret.md"); err == nil {
		t.Fatal("expected symlink escape rejection")
	}
}

func writePlugin(t *testing.T, root, id string, files map[string]string) {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	for rel, body := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_ = id
}

func validAgentMD(name string) string {
	return "---\ndescription: test agent " + name + "\n---\nYou are " + name + ".\n"
}

func validSkillMD(name string) string {
	return "---\ndescription: skill " + name + "\n---\nDo " + name + " $ARGUMENTS\n"
}

func validWorkflowJSON(name string) string {
	return `{
  "schemaVersion": 1,
  "name": "` + name + `",
  "phases": [{ "name": "one", "agent": "build", "exit": { "type": "agent" } }]
}
`
}

func validThemeJSON(id string) string {
	return `{
  "id": "` + id + `",
  "name": "` + id + `",
  "colors": { "accent": "#aabbcc" }
}
`
}

func validProviderJSON(name string) string {
	return `{
  "` + name + `": {
    "api": "openai",
    "baseURL": "https://example.com/v1",
    "apiKeyEnv": "EXAMPLE_KEY",
    "models": ["m1"]
  }
}
`
}

func manifest(id string, contrib string) string {
	return `{
  "schemaVersion": 1,
  "id": "` + id + `",
  "version": "1.0.0",
  "name": "` + id + `",
  "strike": { "min": "0.1.0" },
  "contributions": ` + contrib + `
}
`
}

func TestDiscover_LoadsPassiveContributions(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	t.Setenv("HOME", home)

	gRoot := filepath.Join(home, ".strike", "plugins", "acme.global")
	writePlugin(t, gRoot, "acme.global", map[string]string{
		"plugin.json": manifest("acme.global", `{
      "agents": [{ "path": "agents/g-agent.md" }],
      "skills": [{ "path": "skills/g-skill.md" }]
    }`),
		"agents/g-agent.md": validAgentMD("g-agent"),
		"skills/g-skill.md": validSkillMD("g-skill"),
	})

	pRoot := filepath.Join(work, ".strike", "plugins", "acme.project")
	writePlugin(t, pRoot, "acme.project", map[string]string{
		"plugin.json": manifest("acme.project", `{
      "workflows": [{ "path": "workflows/p-flow.json" }],
      "themes": [{ "path": "themes/p-theme.json" }],
      "providers": [{ "path": "providers/p.json", "profileName": "acme-proxy" }]
    }`),
		"workflows/p-flow.json": validWorkflowJSON("p-flow"),
		"themes/p-theme.json":   validThemeJSON("p-theme"),
		"providers/p.json":      validProviderJSON("ignored"),
	})

	res := Discover(Options{
		WorkDir:       work,
		GlobalRoot:    filepath.Join(home, ".strike"),
		ProjectRoot:   filepath.Join(work, ".strike"),
		StrikeVersion: "0.2.0",
	})
	if len(res.Plugins) != 2 {
		t.Fatalf("plugins=%d diags=%v", len(res.Plugins), res.Diagnostics)
	}
	// Global first (id order within scope), then project.
	if res.Plugins[0].ID != "acme.global" || res.Plugins[1].ID != "acme.project" {
		t.Fatalf("order: %s, %s", res.Plugins[0].ID, res.Plugins[1].ID)
	}
	if len(res.Plugins[0].Agents) != 1 || len(res.Plugins[0].Skills) != 1 {
		t.Fatalf("global passive: %+v", res.Plugins[0])
	}
	if len(res.Plugins[1].Workflows) != 1 || len(res.Plugins[1].Themes) != 1 || len(res.Plugins[1].Providers) != 1 {
		t.Fatalf("project passive: %+v", res.Plugins[1])
	}
	if res.Plugins[1].Providers[0].ProfileName != "acme-proxy" {
		t.Fatalf("profileName=%q", res.Plugins[1].Providers[0].ProfileName)
	}
}

func TestDiscover_ProjectShadowsGlobalSameID(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	gRoot := filepath.Join(home, ".strike", "plugins", "acme.pack")
	writePlugin(t, gRoot, "acme.pack", map[string]string{
		"plugin.json": manifest("acme.pack", `{"agents":[{"path":"agents/a.md"}]}`),
		"agents/a.md": validAgentMD("from-global"),
	})
	pRoot := filepath.Join(work, ".strike", "plugins", "acme.pack")
	writePlugin(t, pRoot, "acme.pack", map[string]string{
		"plugin.json": manifest("acme.pack", `{"agents":[{"path":"agents/a.md"}]}`),
		"agents/a.md": validAgentMD("from-project"),
	})
	res := Discover(Options{
		WorkDir:       work,
		GlobalRoot:    filepath.Join(home, ".strike"),
		ProjectRoot:   filepath.Join(work, ".strike"),
		StrikeVersion: "0.2.0",
	})
	if len(res.Plugins) != 1 || res.Plugins[0].Source != ScopeProject {
		t.Fatalf("want single project plugin, got %+v", res.Plugins)
	}
	var shadowed bool
	for _, d := range res.Diagnostics {
		if d.Code == "shadowed" {
			shadowed = true
		}
	}
	if !shadowed {
		t.Fatalf("expected shadowed diagnostic: %v", res.Diagnostics)
	}
}

func TestDiscover_DisabledContributesNothing(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	gRoot := filepath.Join(home, ".strike", "plugins", "acme.pack")
	writePlugin(t, gRoot, "acme.pack", map[string]string{
		"plugin.json": manifest("acme.pack", `{"agents":[{"path":"agents/a.md"}]}`),
		"agents/a.md": validAgentMD("disabled-agent"),
	})
	lock := `{"schemaVersion":1,"plugins":{"acme.pack":{"enabled":false}}}`
	if err := os.WriteFile(filepath.Join(home, ".strike", "plugins.lock.json"), []byte(lock), 0o644); err != nil {
		t.Fatal(err)
	}
	res := Discover(Options{
		WorkDir:       work,
		GlobalRoot:    filepath.Join(home, ".strike"),
		ProjectRoot:   filepath.Join(work, ".strike"),
		StrikeVersion: "0.2.0",
	})
	if len(res.Plugins) != 0 {
		t.Fatalf("disabled plugin should not load: %+v", res.Plugins)
	}
	var disabled bool
	for _, d := range res.Diagnostics {
		if d.Code == "disabled" {
			disabled = true
		}
	}
	if !disabled {
		t.Fatalf("expected disabled diagnostic: %v", res.Diagnostics)
	}
}

func TestDiscover_UnsupportedSchemaVersion(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".strike", "plugins", "acme.future")
	writePlugin(t, root, "acme.future", map[string]string{
		"plugin.json": `{
  "schemaVersion": 99,
  "id": "acme.future",
  "version": "1.0.0",
  "name": "Future",
  "strike": { "min": "0.1.0" },
  "contributions": { "agents": [{ "path": "agents/a.md" }] }
}`,
		"agents/a.md": validAgentMD("x"),
	})
	res := Discover(Options{
		GlobalRoot:    filepath.Join(home, ".strike"),
		StrikeVersion: "0.2.0",
	})
	if len(res.Plugins) != 0 {
		t.Fatalf("want skip, got %+v", res.Plugins)
	}
	var found bool
	for _, d := range res.Diagnostics {
		if d.Code == "schema_version" {
			found = true
		}
	}
	if !found {
		t.Fatalf("diags: %v", res.Diagnostics)
	}
}

func TestDiscover_StrikeVersionTooOld(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".strike", "plugins", "acme.pack")
	writePlugin(t, root, "acme.pack", map[string]string{
		"plugin.json": `{
  "schemaVersion": 1,
  "id": "acme.pack",
  "version": "1.0.0",
  "name": "Pack",
  "strike": { "min": "9.0.0" },
  "contributions": { "agents": [{ "path": "agents/a.md" }] }
}`,
		"agents/a.md": validAgentMD("x"),
	})
	res := Discover(Options{
		GlobalRoot:    filepath.Join(home, ".strike"),
		StrikeVersion: "0.2.0",
	})
	if len(res.Plugins) != 0 {
		t.Fatal("expected skip for strike version")
	}
	var found bool
	for _, d := range res.Diagnostics {
		if d.Code == "strike_version" {
			found = true
		}
	}
	if !found {
		t.Fatalf("diags: %v", res.Diagnostics)
	}
}

func TestDiscover_MalformedManifestDoesNotShadow(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	// Good global agent via non-plugin is not in Discover; here two plugins:
	// malformed must not appear in Plugins list.
	bad := filepath.Join(home, ".strike", "plugins", "bad.pack")
	writePlugin(t, bad, "bad.pack", map[string]string{
		"plugin.json": `{"not":"valid"}`,
	})
	good := filepath.Join(home, ".strike", "plugins", "good.pack")
	writePlugin(t, good, "good.pack", map[string]string{
		"plugin.json": manifest("good.pack", `{"agents":[{"path":"agents/a.md"}]}`),
		"agents/a.md": validAgentMD("good-agent"),
	})
	res := Discover(Options{
		WorkDir:       work,
		GlobalRoot:    filepath.Join(home, ".strike"),
		ProjectRoot:   filepath.Join(work, ".strike"),
		StrikeVersion: "0.2.0",
	})
	if len(res.Plugins) != 1 || res.Plugins[0].ID != "good.pack" {
		t.Fatalf("got %+v diags=%v", res.Plugins, res.Diagnostics)
	}
	var malformed bool
	for _, d := range res.Diagnostics {
		if d.Code == "malformed" {
			malformed = true
		}
	}
	if !malformed {
		t.Fatal("expected malformed diagnostic for bad plugin")
	}
}

func TestDiscover_PathTraversalContribution(t *testing.T) {
	home := t.TempDir()
	// Manifest-level .. is rejected at parse; also test Resolve on sneaky path
	// that passes syntax if we only checked segments after join — ensure missing file diags.
	root := filepath.Join(home, ".strike", "plugins", "acme.pack")
	// Write a manifest that somehow has a path that fails resolve (absolute rejected at parse).
	writePlugin(t, root, "acme.pack", map[string]string{
		"plugin.json": manifest("acme.pack", `{"agents":[{"path":"agents/missing.md"}]}`),
	})
	res := Discover(Options{
		GlobalRoot:    filepath.Join(home, ".strike"),
		StrikeVersion: "0.2.0",
	})
	if len(res.Plugins) != 0 {
		t.Fatalf("missing contribution should skip plugin: %+v", res.Plugins)
	}
}

func TestDiscover_ExecutableInactiveNote(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".strike", "plugins", "acme.tools")
	writePlugin(t, root, "acme.tools", map[string]string{
		"plugin.json": `{
  "schemaVersion": 1,
  "id": "acme.tools",
  "version": "1.0.0",
  "name": "Tools",
  "strike": { "min": "0.1.0" },
  "contributions": {
    "agents": [{ "path": "agents/a.md" }],
    "mcp": [{ "name": "x", "transport": "stdio", "command": "bin/x" }]
  }
}`,
		"agents/a.md": validAgentMD("a"),
	})
	res := Discover(Options{
		GlobalRoot:    filepath.Join(home, ".strike"),
		StrikeVersion: "0.2.0",
	})
	if len(res.Plugins) != 1 {
		t.Fatalf("got %+v %v", res.Plugins, res.Diagnostics)
	}
	var inactive bool
	for _, d := range res.Diagnostics {
		if d.Code == "executable_inactive" {
			inactive = true
		}
	}
	if !inactive {
		t.Fatal("expected executable_inactive diagnostic")
	}
}

func TestComputeDigest_Stable(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "b.txt"), []byte("world"), 0o644); err != nil {
		t.Fatal(err)
	}
	d1, err := ComputeDigest(root)
	if err != nil {
		t.Fatal(err)
	}
	d2, err := ComputeDigest(root)
	if err != nil {
		t.Fatal(err)
	}
	if d1 != d2 || !strings.HasPrefix(d1, "sha256:") {
		t.Fatalf("d1=%s d2=%s", d1, d2)
	}
}

func TestDiscover_DigestMismatch(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".strike", "plugins", "acme.pack")
	writePlugin(t, root, "acme.pack", map[string]string{
		"plugin.json": `{
  "schemaVersion": 1,
  "id": "acme.pack",
  "version": "1.0.0",
  "name": "Pack",
  "strike": { "min": "0.1.0" },
  "digest": "sha256:0000000000000000000000000000000000000000000000000000000000000000",
  "contributions": { "agents": [{ "path": "agents/a.md" }] }
}`,
		"agents/a.md": validAgentMD("a"),
	})
	res := Discover(Options{
		GlobalRoot:    filepath.Join(home, ".strike"),
		StrikeVersion: "0.2.0",
	})
	if len(res.Plugins) != 0 {
		t.Fatal("digest mismatch should skip")
	}
	var found bool
	for _, d := range res.Diagnostics {
		if d.Code == "digest" {
			found = true
		}
	}
	if !found {
		t.Fatalf("diags: %v", res.Diagnostics)
	}
}

func TestCompareSemver(t *testing.T) {
	cmp := func(a, b string, want int) {
		t.Helper()
		got, err := compareSemver(a, b)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("%s vs %s: got %d want %d", a, b, got, want)
		}
	}
	cmp("1.0.0", "1.0.0", 0)
	cmp("1.0.0", "1.0.1", -1)
	cmp("2.0.0", "1.9.9", 1)
	cmp("1.0.0-alpha", "1.0.0", -1)
}

func TestFormatProvenance(t *testing.T) {
	s := FormatProvenance("acme.pack", "1.2.0", ScopeProject, "agents/a.md")
	if !strings.Contains(s, "plugin=acme.pack@1.2.0") || !strings.Contains(s, "source=project") {
		t.Fatal(s)
	}
}

func TestIsEnabled_ProjectOverridesGlobal(t *testing.T) {
	f := false
	tr := true
	global := Lockfile{Plugins: map[string]LockfileEntry{"acme.pack": {Enabled: &tr}}}
	project := Lockfile{Plugins: map[string]LockfileEntry{"acme.pack": {Enabled: &f}}}
	if IsEnabled("acme.pack", global, project) {
		t.Fatal("project disable should win")
	}
	if !IsEnabled("other", global, project) {
		t.Fatal("absent defaults enabled")
	}
}
