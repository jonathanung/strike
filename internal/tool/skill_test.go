package tool

import (
	"context"
	"errors"
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
	var saw AskRequest
	tc := &Context{
		WorkDir: t.TempDir(),
		Ask: func(_ context.Context, req AskRequest) error {
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
	tc := &Context{
		WorkDir: t.TempDir(),
		Ask:     func(context.Context, AskRequest) error { return errors.New("denied") },
	}
	_, err := sk.Execute(context.Background(), mustJSON(t, map[string]any{"name": "x"}), tc)
	if err == nil {
		t.Fatal("expected deny")
	}
}
