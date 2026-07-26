package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/permission"
)

func writeMD(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadSkillsFromClaudeTree(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	work := t.TempDir()

	// Claude layout: .claude/skills/<name>/SKILL.md
	writeMD(t, filepath.Join(work, ".claude", "skills", "foo", "SKILL.md"),
		"---\nname: foo\ndescription: from claude\n---\nClaude skill body: $ARGUMENTS\n")

	skills, err := LoadSkillsWithError(work)
	if err != nil {
		t.Fatalf("LoadSkillsWithError: %v", err)
	}
	got, ok := lookupSkill(skills, "foo")
	if !ok {
		t.Fatalf("skills = %v, missing foo", skillNameList(skills))
	}
	if got.Description != "from claude" || got.Template != "Claude skill body: $ARGUMENTS" {
		t.Errorf("foo = %+v", got)
	}
}

func TestLoadSkillsFromOpencodeTree(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	work := t.TempDir()

	writeMD(t, filepath.Join(work, ".opencode", "skills", "bar", "SKILL.md"),
		"---\ndescription: from opencode\n---\nOpenCode skill.\n")

	skills, err := LoadSkillsWithError(work)
	if err != nil {
		t.Fatalf("LoadSkillsWithError: %v", err)
	}
	got, ok := lookupSkill(skills, "bar")
	if !ok {
		t.Fatalf("skills = %v, missing bar", skillNameList(skills))
	}
	if got.Description != "from opencode" || got.Template != "OpenCode skill." {
		t.Errorf("bar = %+v", got)
	}
}

func TestLoadAgentsFromClaudeTree(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	work := t.TempDir()

	writeMD(t, filepath.Join(work, ".claude", "agents", "reviewer-extra.md"),
		"---\ndescription: claude agent\nmodel: openai/gpt-test\npermission:\n  write: deny\n  edit: deny\n---\nClaude agent prompt.\n")

	agents, err := LoadAgentsWithError(work)
	if err != nil {
		t.Fatalf("LoadAgentsWithError: %v", err)
	}
	got, ok := lookupAgent(agents, "reviewer-extra")
	if !ok {
		t.Fatalf("agents = %v, missing reviewer-extra", agentNames(agents))
	}
	if got.Provider != "openai" || got.Model != "gpt-test" {
		t.Errorf("provider/model = %q/%q, want openai/gpt-test", got.Provider, got.Model)
	}
	if got.Prompt != "Claude agent prompt." {
		t.Errorf("prompt = %q", got.Prompt)
	}
	want := permission.Ruleset{
		{Permission: "edit", Pattern: "*", Action: permission.Deny},
		{Permission: "write", Pattern: "*", Action: permission.Deny},
	}
	if !reflect.DeepEqual(got.Permissions, want) {
		t.Errorf("Permissions = %#v, want %#v", got.Permissions, want)
	}
}

func TestLoadAgentsFromOpencodeAgentDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	work := t.TempDir()

	// Singular .opencode/agent is accepted.
	writeMD(t, filepath.Join(work, ".opencode", "agent", "scout.md"),
		"---\ndescription: scout\n---\nScout prompt.\n")

	agents, err := LoadAgentsWithError(work)
	if err != nil {
		t.Fatalf("LoadAgentsWithError: %v", err)
	}
	got, ok := lookupAgent(agents, "scout")
	if !ok {
		t.Fatalf("agents = %v, missing scout", agentNames(agents))
	}
	if got.Prompt != "Scout prompt." {
		t.Errorf("prompt = %q", got.Prompt)
	}
}

func TestStrikeProjectOverridesEarlierExternal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	work := t.TempDir()

	// Global claude skill, then project .strike wins (later in merge order).
	writeMD(t, filepath.Join(home, ".claude", "skills", "shared", "SKILL.md"),
		"---\ndescription: global claude\n---\nfrom claude\n")
	writeMD(t, filepath.Join(work, ".strike", "skills", "shared.md"),
		"---\ndescription: project strike\n---\nfrom strike\n")

	skills, err := LoadSkillsWithError(work)
	if err != nil {
		t.Fatalf("LoadSkillsWithError: %v", err)
	}
	got, ok := lookupSkill(skills, "shared")
	if !ok {
		t.Fatalf("missing shared among %v", skillNameList(skills))
	}
	if got.Description != "project strike" || got.Template != "from strike" {
		t.Errorf("shared = %+v, want project strike override", got)
	}
}

func TestProjectClaudeOverridesProjectStrike(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	work := t.TempDir()

	writeMD(t, filepath.Join(work, ".strike", "skills", "shared.md"),
		"---\ndescription: strike\n---\nstrike body\n")
	writeMD(t, filepath.Join(work, ".claude", "skills", "shared", "SKILL.md"),
		"---\ndescription: claude wins\n---\nclaude body\n")

	skills, err := LoadSkillsWithError(work)
	if err != nil {
		t.Fatalf("LoadSkillsWithError: %v", err)
	}
	got, ok := lookupSkill(skills, "shared")
	if !ok {
		t.Fatalf("missing shared among %v", skillNameList(skills))
	}
	if got.Description != "claude wins" || got.Template != "claude body" {
		t.Errorf("shared = %+v, want claude override", got)
	}
}

func TestExternalInvalidSkillSkipped(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	work := t.TempDir()

	// Reserved name in external tree must not fail the load.
	writeMD(t, filepath.Join(work, ".claude", "skills", "help", "SKILL.md"),
		"---\ndescription: reserved\n---\nshould skip\n")
	writeMD(t, filepath.Join(work, ".claude", "skills", "ok", "SKILL.md"),
		"---\ndescription: ok\n---\nok body\n")

	skills, err := LoadSkillsWithError(work)
	if err != nil {
		t.Fatalf("LoadSkillsWithError: %v", err)
	}
	if _, ok := lookupSkill(skills, "help"); ok {
		t.Error("reserved skill help should be skipped from external tree")
	}
	if _, ok := lookupSkill(skills, "ok"); !ok {
		t.Fatalf("missing ok among %v", skillNameList(skills))
	}
}

func TestExternalInvalidAgentSkipped(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	work := t.TempDir()

	writeMD(t, filepath.Join(work, ".claude", "agents", "bad.md"),
		"---\neffort: turbo\n---\nbad\n")
	writeMD(t, filepath.Join(work, ".claude", "agents", "good.md"),
		"---\ndescription: good\n---\ngood prompt\n")

	agents, err := LoadAgentsWithError(work)
	if err != nil {
		t.Fatalf("LoadAgentsWithError: %v", err)
	}
	if _, ok := lookupAgent(agents, "bad"); ok {
		t.Error("invalid external agent should be skipped")
	}
	if _, ok := lookupAgent(agents, "good"); !ok {
		t.Fatalf("missing good among %v", agentNames(agents))
	}
}

func TestGlobalOpencodeConfigHomeSkills(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	xdg := filepath.Join(home, "xdg-config")
	t.Setenv("XDG_CONFIG_HOME", xdg)
	work := t.TempDir()

	writeMD(t, filepath.Join(xdg, "opencode", "skills", "xdgskill", "SKILL.md"),
		"---\ndescription: xdg\n---\nxdg body\n")

	skills, err := LoadSkillsWithError(work)
	if err != nil {
		t.Fatalf("LoadSkillsWithError: %v", err)
	}
	got, ok := lookupSkill(skills, "xdgskill")
	if !ok {
		t.Fatalf("missing xdgskill among %v", skillNameList(skills))
	}
	if got.Template != "xdg body" {
		t.Errorf("template = %q", got.Template)
	}
}

func TestNestedPermissionPatterns(t *testing.T) {
	meta, body, nested := parseFrontmatter(`---
description: patterned
permission:
  edit: deny
  bash:
    "*": deny
    "git *": allow
---
Body.
`)
	if body != "Body." || meta["description"] != "patterned" {
		t.Fatalf("meta=%v body=%q", meta, body)
	}
	want := permission.Ruleset{
		{Permission: "edit", Pattern: "*", Action: permission.Deny},
		{Permission: "bash", Pattern: "*", Action: permission.Deny},
		{Permission: "bash", Pattern: "git *", Action: permission.Allow},
	}
	if !reflect.DeepEqual(nested, want) {
		t.Errorf("nested = %#v, want %#v", nested, want)
	}
}

func TestClaudeCommandsAsSkills(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	work := t.TempDir()

	writeMD(t, filepath.Join(work, ".claude", "commands", "deploy.md"),
		"---\ndescription: deploy\n---\nDeploy: $ARGUMENTS\n")

	skills, err := LoadSkillsWithError(work)
	if err != nil {
		t.Fatalf("LoadSkillsWithError: %v", err)
	}
	got, ok := lookupSkill(skills, "deploy")
	if !ok {
		t.Fatalf("missing deploy among %v", skillNameList(skills))
	}
	if !strings.Contains(got.Template, "Deploy:") {
		t.Errorf("template = %q", got.Template)
	}
}

func TestNonMarkdownIgnoredInExternalTrees(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	work := t.TempDir()

	dir := filepath.Join(work, ".opencode", "skills", "plugin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.ts"), []byte("export {}"), 0o644); err != nil {
		t.Fatal(err)
	}
	// No SKILL.md — must not crash or invent a skill.
	skills, err := LoadSkillsWithError(work)
	if err != nil {
		t.Fatalf("LoadSkillsWithError: %v", err)
	}
	if _, ok := lookupSkill(skills, "plugin"); ok {
		t.Error("non-markdown plugin dir should not become a skill")
	}
}

func lookupSkill(skills []Skill, name string) (Skill, bool) {
	for _, s := range skills {
		if s.Name == name {
			return s, true
		}
	}
	return Skill{}, false
}

func skillNameList(skills []Skill) []string {
	names := make([]string, len(skills))
	for i, s := range skills {
		names[i] = s.Name
	}
	return names
}
