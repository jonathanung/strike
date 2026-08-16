package tools

import (
	"context"
	"errors"
	"github.com/jonathanung/strike-cli/internal/tool"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSkillLoadKnown(t *testing.T) {
	sk := NewSkill([]SkillInfo{
		{Name: "greet", Description: "say hi", Template: "Hello from skill."},
		{Name: "other", Description: "x", Template: "other body"},
	})
	res, err := sk.Execute(context.Background(), mustJSON(t, map[string]any{
		"name": "greet",
	}), allowAll(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "# Skill: greet") {
		t.Errorf("output = %q", res.Output)
	}
	if !strings.Contains(res.Output, "Hello from skill.") {
		t.Errorf("output = %q", res.Output)
	}
	if res.Title != "skill greet" {
		t.Errorf("title = %q", res.Title)
	}
	if !strings.Contains(sk.Description(), "greet") || !strings.Contains(sk.Description(), "other") {
		t.Errorf("description should list skills: %s", sk.Description())
	}
}

func TestSkillArgumentsSubstitution(t *testing.T) {
	sk := NewSkill([]SkillInfo{
		{Name: "run", Template: "Do this: $ARGUMENTS\nThen done."},
	})
	res, err := sk.Execute(context.Background(), mustJSON(t, map[string]any{
		"name":      "run",
		"arguments": "step one",
	}), allowAll(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "Do this: step one") {
		t.Errorf("output = %q", res.Output)
	}
	if strings.Contains(res.Output, "$ARGUMENTS") {
		t.Errorf("placeholder not replaced: %q", res.Output)
	}

	// No placeholder: arguments appended.
	sk2 := NewSkill([]SkillInfo{
		{Name: "plain", Template: "Body only."},
	})
	res, err = sk2.Execute(context.Background(), mustJSON(t, map[string]any{
		"name":      "plain",
		"arguments": "extra",
	}), allowAll(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "Body only.") || !strings.Contains(res.Output, "Arguments: extra") {
		t.Errorf("output = %q", res.Output)
	}
}

func TestSkillUnknownListsAvailable(t *testing.T) {
	sk := NewSkill([]SkillInfo{
		{Name: "alpha", Template: "a"},
		{Name: "beta", Template: "b"},
	})
	_, err := sk.Execute(context.Background(), mustJSON(t, map[string]any{
		"name": "gamma",
	}), allowAll(t.TempDir()))
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "unknown skill") {
		t.Errorf("got %v", err)
	}
	if !strings.Contains(msg, "alpha") || !strings.Contains(msg, "beta") {
		t.Errorf("should list available: %v", err)
	}
}

func TestSkillEmptyCatalog(t *testing.T) {
	sk := NewSkill(nil)
	_, err := sk.Execute(context.Background(), mustJSON(t, map[string]any{
		"name": "anything",
	}), allowAll(t.TempDir()))
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "no skills loaded") {
		t.Errorf("got %v", err)
	}
	if !strings.Contains(sk.Description(), "none loaded") {
		t.Errorf("description = %s", sk.Description())
	}
}

func TestSkillPermissionPatternIsName(t *testing.T) {
	sk := NewSkill([]SkillInfo{
		{Name: "deploy", Template: "ship it"},
	})
	var saw tool.AskRequest
	tc := &tool.Context{
		WorkDir: t.TempDir(),
		Ask: func(_ context.Context, req tool.AskRequest) error {
			saw = req
			return nil
		},
	}
	_, err := sk.Execute(context.Background(), mustJSON(t, map[string]any{
		"name": "deploy",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if saw.Permission != "skill" {
		t.Errorf("permission = %q", saw.Permission)
	}
	if len(saw.Patterns) != 1 || saw.Patterns[0] != "deploy" {
		t.Errorf("patterns = %#v", saw.Patterns)
	}
	if len(saw.Always) != 1 || saw.Always[0] != "deploy" {
		t.Errorf("always = %#v", saw.Always)
	}
}

func TestSkillPermissionDenied(t *testing.T) {
	sk := NewSkill([]SkillInfo{{Name: "x", Template: "y"}})
	tc := &tool.Context{
		WorkDir: t.TempDir(),
		Ask:     func(context.Context, tool.AskRequest) error { return errors.New("denied") },
	}
	_, err := sk.Execute(context.Background(), mustJSON(t, map[string]any{"name": "x"}), tc)
	if err == nil {
		t.Fatal("expected deny")
	}
}

func TestSkillAdjacentResource(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "demo")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	skillPath := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(skillPath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "extra.md"), []byte("adjacent-body"), 0o644); err != nil {
		t.Fatal(err)
	}
	sk := NewSkill([]SkillInfo{{
		Name: "demo", Template: "main", Path: skillPath,
	}})
	res, err := sk.Execute(context.Background(), mustJSON(t, map[string]any{
		"name": "demo", "resource": "extra.md",
	}), &tool.Context{Ask: func(context.Context, tool.AskRequest) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "adjacent-body") {
		t.Fatalf("%q", res.Output)
	}
	_, err = sk.Execute(context.Background(), mustJSON(t, map[string]any{
		"name": "demo", "resource": "../nope",
	}), &tool.Context{Ask: func(context.Context, tool.AskRequest) error { return nil }})
	if err == nil {
		t.Fatal("want traversal error")
	}
}
