package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// #1855: deferred review nits from #1757.

func TestIsClaudeCommandsRoot(t *testing.T) {
	tests := []struct {
		root string
		want bool
	}{
		{filepath.Join("w", ".claude", "commands"), true},
		{filepath.Join("w", ".claude", "commands") + string(filepath.Separator), true},
		{filepath.Join("w", ".strike", "skills", "commands"), false},
		{filepath.Join("w", ".claude", "skills"), false},
		{filepath.Join("w", "commands"), false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isClaudeCommandsRoot(tt.root); got != tt.want {
			t.Errorf("isClaudeCommandsRoot(%q) = %v, want %v", tt.root, got, tt.want)
		}
	}
}

func TestMissingDescriptionExemptionIsTheCommandsRootOnly(t *testing.T) {
	tests := []struct {
		name     string
		rel      []string
		wantWarn bool
	}{
		{"claude commands file", []string{".claude", "commands", "deploy.md"}, false},
		{"strike skill dir named commands", []string{".strike", "skills", "commands", "SKILL.md"}, true},
		{"claude skill dir named commands", []string{".claude", "skills", "commands", "SKILL.md"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, work := isolateLoadHome(t)
			path := filepath.Join(append([]string{work}, tt.rel...)...)
			writeMD(t, path, "Body with no frontmatter.\n")
			_, diags, err := LoadSkillsReport(work)
			if err != nil {
				t.Fatal(err)
			}
			got := false
			for _, d := range diags {
				if d.Path == path && d.Code == DiagMissingDescription {
					got = true
				}
			}
			if got != tt.wantWarn {
				t.Fatalf("missing_description warned = %v, want %v (diags %v)", got, tt.wantWarn, diags)
			}
		})
	}
}

func TestPluginLayerPassesPerFileNotes(t *testing.T) {
	_, work := isolateLoadHome(t)
	writeTree(t, filepath.Join(work, ".strike", "plugins", "acme.notes"), map[string]string{
		"plugin.json": `{
  "schemaVersion": 1,
  "id": "acme.notes",
  "version": "1.0.0",
  "name": "Notes",
  "strike": { "min": "0.1.0" },
  "contributions": {
    "skills": [{ "path": "skills/nodesc.md" }, { "path": "skills/open.md" }, { "path": "skills/hollow.md" }],
    "agents": [{ "path": "agents/open-agent.md" }]
  }
}`,
		"skills/nodesc.md":     "---\nname: nodesc\n---\nBody.\n",
		"skills/open.md":       "---\nname: open\nNever closed.\n",
		"skills/hollow.md":     "---\ndescription: x\n---\n",
		"agents/open-agent.md": "---\ndescription: never closed\nPrompt.\n",
	})
	skills, skillDiags, err := LoadSkillsReport(work)
	if err != nil {
		t.Fatal(err)
	}
	agents, agentDiags, err := LoadAgentsReport(work)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		diags []LoadDiagnostic
		file  string
		code  string
	}{
		{skillDiags, "nodesc.md", DiagMissingDescription},
		{skillDiags, "open.md", DiagMalformedFrontmatter},
		{skillDiags, "hollow.md", DiagEmpty},
		{agentDiags, "open-agent.md", DiagMalformedFrontmatter},
	}
	for _, tt := range tests {
		d, ok := diagFor(tt.diags, tt.file, tt.code)
		if !ok {
			t.Errorf("%s: no %s note (diags %v)", tt.file, tt.code, tt.diags)
			continue
		}
		if !strings.Contains(d.Message, "plugin acme.notes: ") || d.Fatal {
			t.Errorf("%s: note = %s", tt.file, d)
		}
	}
	// One empty note for the bodiless file, not a second generic one.
	empties := 0
	for _, d := range skillDiags {
		if filepath.Base(d.Path) == "hollow.md" && d.Code == DiagEmpty {
			empties++
		}
	}
	if empties != 1 {
		t.Errorf("hollow.md empty diagnostics = %d, want 1", empties)
	}
	// No change to what loads.
	loaded := map[string]bool{}
	for _, s := range skills {
		loaded[s.Name] = true
	}
	if !loaded["nodesc"] || !loaded["open"] || loaded["hollow"] {
		t.Errorf("loaded skills = %v", loaded)
	}
	foundAgent := false
	for _, a := range agents {
		foundAgent = foundAgent || a.Name == "open-agent"
	}
	if !foundAgent {
		t.Error("plugin agent with malformed frontmatter must still load")
	}
}
