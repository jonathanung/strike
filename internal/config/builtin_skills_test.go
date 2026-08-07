package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuiltinSkillsShippingCommands(t *testing.T) {
	skills := BuiltinSkills()
	byName := map[string]Skill{}
	for _, s := range skills {
		byName[s.Name] = s
	}
	for _, name := range []string{"commit", "push", "pr", "ship", "review", "learn", "deslop", "verify", "write-guards"} {
		s, ok := byName[name]
		if !ok {
			t.Fatalf("missing builtin skill %q among %+v", name, skills)
		}
		if strings.TrimSpace(s.Description) == "" {
			t.Errorf("%s: empty description", name)
		}
		if !strings.Contains(s.Template, "$ARGUMENTS") {
			t.Errorf("%s: template missing $ARGUMENTS", name)
		}
		if got := s.Render("fix auth"); !strings.Contains(got, "fix auth") {
			t.Errorf("%s render missing args: %q", name, got)
		}
	}
}

func TestLoadSkillsMergesBuiltinsAndProjectOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()

	// No disk skills: builtins only.
	skills, err := LoadSkillsWithError(work)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, s := range skills {
		names[s.Name] = true
	}
	for _, want := range []string{"commit", "push", "pr", "ship", "review", "learn", "deslop", "verify", "write-guards"} {
		if !names[want] {
			t.Errorf("missing %s in %+v", want, skills)
		}
	}

	// Project override replaces commit template.
	dir := filepath.Join(work, ".strike", "skills")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "commit.md"), []byte("---\ndescription: custom commit\n---\nCustom: $ARGUMENTS\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	skills, err = LoadSkillsWithError(work)
	if err != nil {
		t.Fatal(err)
	}
	var commit Skill
	for _, s := range skills {
		if s.Name == "commit" {
			commit = s
		}
	}
	if commit.Description != "custom commit" {
		t.Fatalf("commit description = %q", commit.Description)
	}
	if got := commit.Render("x"); got != "Custom: x" {
		t.Fatalf("commit render = %q", got)
	}
	// Other builtins still present.
	if !namesHas(skills, "ship") {
		t.Fatal("ship builtin dropped after override")
	}
}

func namesHas(skills []Skill, name string) bool {
	for _, s := range skills {
		if s.Name == name {
			return true
		}
	}
	return false
}

func TestMergeSkillsOrder(t *testing.T) {
	base := []Skill{{Name: "a", Template: "A"}, {Name: "b", Template: "B"}}
	overlay := []Skill{{Name: "b", Template: "B2"}, {Name: "c", Template: "C"}}
	got := mergeSkills(base, overlay)
	if len(got) != 3 || got[0].Name != "a" || got[1].Name != "b" || got[2].Name != "c" {
		t.Fatalf("order = %+v", got)
	}
	if got[1].Template != "B2" {
		t.Fatalf("overlay = %+v", got[1])
	}
}
