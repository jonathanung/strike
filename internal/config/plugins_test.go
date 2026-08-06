package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPluginPassiveLoad_Surfaces(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	t.Setenv("HOME", home)
	ResetPluginCache()

	// Global plugin: agent + skill
	gPlug := filepath.Join(home, ".strike", "plugins", "acme.global")
	writeTree(t, gPlug, map[string]string{
		"plugin.json": `{
  "schemaVersion": 1,
  "id": "acme.global",
  "version": "1.0.0",
  "name": "Global",
  "strike": { "min": "0.1.0" },
  "contributions": {
    "agents": [{ "path": "agents/g-agent.md" }],
    "skills": [{ "path": "skills/g-skill.md" }]
  }
}`,
		"agents/g-agent.md": "---\ndescription: g\n---\nGlobal agent.\n",
		"skills/g-skill.md": "---\ndescription: gs\n---\nGlobal skill $ARGUMENTS\n",
	})

	// Project plugin: workflow + provider + theme (theme via theme package)
	pPlug := filepath.Join(work, ".strike", "plugins", "acme.project")
	writeTree(t, pPlug, map[string]string{
		"plugin.json": `{
  "schemaVersion": 1,
  "id": "acme.project",
  "version": "1.0.0",
  "name": "Project",
  "strike": { "min": "0.1.0" },
  "contributions": {
    "workflows": [{ "path": "workflows/p-flow.json" }],
    "providers": [{ "path": "providers/proxy.json", "profileName": "acme-proxy" }],
    "agents": [{ "path": "agents/p-agent.md" }]
  }
}`,
		"workflows/p-flow.json": `{
  "schemaVersion": 1,
  "name": "p-flow",
  "phases": [{ "name": "one", "agent": "build", "exit": { "type": "agent" } }]
}`,
		"providers/proxy.json": `{
  "acme-proxy": {
    "api": "openai",
    "baseURL": "https://proxy.example.com/v1",
    "apiKeyEnv": "ACME_KEY",
    "models": ["m1"]
  }
}`,
		"agents/p-agent.md": "---\ndescription: p\n---\nProject agent.\n",
	})

	agents, err := LoadAgentsWithError(work)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := lookupAgent(agents, "g-agent"); !ok {
		t.Fatal("missing global plugin agent")
	}
	if _, ok := lookupAgent(agents, "p-agent"); !ok {
		t.Fatal("missing project plugin agent")
	}

	skills, err := LoadSkillsWithError(work)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := lookupSkill(skills, "g-skill"); !ok {
		t.Fatal("missing global plugin skill")
	}

	workflows, err := LoadWorkflows(work)
	if err != nil {
		t.Fatal(err)
	}
	w, ok := FindWorkflow(workflows, "p-flow")
	if !ok {
		t.Fatal("missing plugin workflow")
	}
	if w.Source != WorkflowSourcePlugin {
		t.Fatalf("workflow source=%q", w.Source)
	}

	cfg, err := Load(work)
	if err != nil {
		t.Fatal(err)
	}
	var foundProv bool
	for _, p := range cfg.Providers {
		if p.Name == "acme-proxy" {
			foundProv = true
			if p.API != WireOpenAI || !strings.Contains(p.BaseURL, "proxy.example.com") {
				t.Fatalf("provider: %+v", p)
			}
		}
	}
	if !foundProv {
		t.Fatalf("missing plugin provider in %#v", cfg.Providers)
	}
}

func TestPluginPassiveLoad_PrecedenceProjectOverGlobalPlugin(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	t.Setenv("HOME", home)
	ResetPluginCache()

	gPlug := filepath.Join(home, ".strike", "plugins", "acme.g")
	writeTree(t, gPlug, map[string]string{
		"plugin.json": `{
  "schemaVersion": 1, "id": "acme.g", "version": "1.0.0", "name": "G",
  "strike": { "min": "0.1.0" },
  "contributions": { "agents": [{ "path": "agents/shared.md" }] }
}`,
		"agents/shared.md": "---\ndescription: from-global-plugin\n---\nGlobal plugin body.\n",
	})
	// Project non-plugin agent wins over global plugin.
	writeTree(t, filepath.Join(work, ".strike", "agents"), map[string]string{
		"shared.md": "---\ndescription: from-project\n---\nProject body.\n",
	})

	agents, err := LoadAgentsWithError(work)
	if err != nil {
		t.Fatal(err)
	}
	a, ok := lookupAgent(agents, "shared")
	if !ok {
		t.Fatal("missing shared agent")
	}
	if !strings.Contains(a.Prompt, "Project body") {
		t.Fatalf("project should win: %q", a.Prompt)
	}
}

func TestPluginPassiveLoad_ProjectPluginWinsOverProjectNonPlugin(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	t.Setenv("HOME", home)
	ResetPluginCache()

	writeTree(t, filepath.Join(work, ".strike", "agents"), map[string]string{
		"shared.md": "---\ndescription: non-plugin\n---\nNon-plugin body.\n",
	})
	pPlug := filepath.Join(work, ".strike", "plugins", "acme.p")
	writeTree(t, pPlug, map[string]string{
		"plugin.json": `{
  "schemaVersion": 1, "id": "acme.p", "version": "1.0.0", "name": "P",
  "strike": { "min": "0.1.0" },
  "contributions": { "agents": [{ "path": "agents/shared.md" }] }
}`,
		"agents/shared.md": "---\ndescription: plugin\n---\nPlugin body.\n",
	})

	agents, err := LoadAgentsWithError(work)
	if err != nil {
		t.Fatal(err)
	}
	a, ok := lookupAgent(agents, "shared")
	if !ok {
		t.Fatal("missing shared")
	}
	if !strings.Contains(a.Prompt, "Plugin body") {
		t.Fatalf("project plugin should win: %q", a.Prompt)
	}
}

func TestPluginPassiveLoad_Disabled(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	t.Setenv("HOME", home)
	ResetPluginCache()

	gPlug := filepath.Join(home, ".strike", "plugins", "acme.off")
	writeTree(t, gPlug, map[string]string{
		"plugin.json": `{
  "schemaVersion": 1, "id": "acme.off", "version": "1.0.0", "name": "Off",
  "strike": { "min": "0.1.0" },
  "contributions": { "agents": [{ "path": "agents/off.md" }] }
}`,
		"agents/off.md": "---\ndescription: off\n---\nShould not load.\n",
	})
	if err := os.WriteFile(filepath.Join(home, ".strike", "plugins.lock.json"), []byte(
		`{"schemaVersion":1,"plugins":{"acme.off":{"enabled":false}}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	agents, err := LoadAgentsWithError(work)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := lookupAgent(agents, "off"); ok {
		t.Fatal("disabled plugin agent must not appear")
	}
}

func TestPluginPassiveLoad_MalformedDoesNotShadow(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	t.Setenv("HOME", home)
	ResetPluginCache()

	// Valid global non-plugin agent
	writeTree(t, filepath.Join(home, ".strike", "agents"), map[string]string{
		"keeper.md": "---\ndescription: keep\n---\nKeep me.\n",
	})
	// Malformed plugin that claims same name — must not load / shadow
	bad := filepath.Join(home, ".strike", "plugins", "bad.pack")
	writeTree(t, bad, map[string]string{
		"plugin.json": `{"schemaVersion":1,"id":"bad.pack"}`, // missing required fields
	})

	agents, err := LoadAgentsWithError(work)
	if err != nil {
		t.Fatal(err)
	}
	a, ok := lookupAgent(agents, "keeper")
	if !ok {
		t.Fatal("keeper should remain")
	}
	if !strings.Contains(a.Prompt, "Keep me") {
		t.Fatalf("shadowed: %q", a.Prompt)
	}
}

func TestPluginPassiveLoad_ProviderRejectsSecretLiteral(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	t.Setenv("HOME", home)
	ResetPluginCache()

	pPlug := filepath.Join(work, ".strike", "plugins", "acme.sec")
	writeTree(t, pPlug, map[string]string{
		"plugin.json": `{
  "schemaVersion": 1, "id": "acme.sec", "version": "1.0.0", "name": "Sec",
  "strike": { "min": "0.1.0" },
  "contributions": { "providers": [{ "path": "providers/p.json", "profileName": "evil" }] }
}`,
		"providers/p.json": `{
  "evil": {
    "api": "openai",
    "baseURL": "https://example.com/v1",
    "headers": { "Authorization": "Bearer sk-this-is-a-literal-secret-key-value" },
    "models": ["m1"]
  }
}`,
	})

	cfg, err := Load(work)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range cfg.Providers {
		if p.Name == "evil" {
			t.Fatal("provider with secret literal must not register")
		}
	}
}

func TestPluginPassiveLoad_ProviderWireOnly(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	t.Setenv("HOME", home)
	ResetPluginCache()

	pPlug := filepath.Join(work, ".strike", "plugins", "acme.wire")
	writeTree(t, pPlug, map[string]string{
		"plugin.json": `{
  "schemaVersion": 1, "id": "acme.wire", "version": "1.0.0", "name": "Wire",
  "strike": { "min": "0.1.0" },
  "contributions": { "providers": [{ "path": "providers/p.json", "profileName": "ok-proxy" }] }
}`,
		"providers/p.json": `{
  "ok-proxy": {
    "npm": "@ai-sdk/openai-compatible",
    "api": "openai",
    "baseURL": "https://ok.example.com/v1",
    "apiKeyEnv": "OK_KEY",
    "models": ["m1"]
  }
}`,
	})
	cfg, err := Load(work)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, p := range cfg.Providers {
		if p.Name == "ok-proxy" {
			found = true
			if p.API != WireOpenAI {
				t.Fatalf("api=%s", p.API)
			}
		}
	}
	if !found {
		t.Fatal("expected ok-proxy")
	}
}

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
