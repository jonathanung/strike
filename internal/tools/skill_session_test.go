package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/jonathanung/strike/harness/tool"
)

// Powers (#1943): skills a session's active packs supply are listed and
// loadable only in that session; a base skill of the same name wins.
func TestSkillSessionSkillsScopedToRootSession(t *testing.T) {
	sk := NewSkill([]SkillInfo{{Name: "base", Template: "base body"}})
	setter, ok := sk.(SessionSkillSetter)
	if !ok {
		t.Fatal("skill tool does not implement SessionSkillSetter")
	}
	describer, ok := sk.(tool.SessionDescriber)
	if !ok {
		t.Fatal("skill tool does not implement tool.SessionDescriber")
	}
	if d := describer.DescriptionFor("s1"); d != "" {
		t.Fatalf("DescriptionFor without a session source = %q, want empty (use Description)", d)
	}
	setter.SetSessionSkills(func(root string) []SkillInfo {
		if root != "s1" {
			return nil
		}
		return []SkillInfo{
			{Name: "pack-skill", Template: "pack body"},
			{Name: "base", Template: "pack override must lose"},
		}
	})

	if d := describer.DescriptionFor("s1"); !strings.Contains(d, "pack-skill") || !strings.Contains(d, "base") {
		t.Errorf("s1 description = %q", d)
	}
	if d := describer.DescriptionFor("s2"); strings.Contains(d, "pack-skill") {
		t.Errorf("s2 description lists s1's pack skill: %q", d)
	}
	if strings.Contains(sk.Description(), "pack-skill") {
		t.Errorf("process-wide description lists a pack skill: %q", sk.Description())
	}

	tests := []struct {
		name    string
		root    string
		session string
		skill   string
		want    string
		wantErr bool
	}{
		{name: "active session loads pack skill", root: "s1", skill: "pack-skill", want: "pack body"},
		{name: "child of active root loads it", root: "s1", session: "child", skill: "pack-skill", want: "pack body"},
		{name: "root falls back to session id", session: "s1", skill: "pack-skill", want: "pack body"},
		{name: "other session cannot load it", root: "s2", skill: "pack-skill", wantErr: true},
		{name: "base wins on clash", root: "s1", skill: "base", want: "base body"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tc := allowAll(t.TempDir())
			tc.RootSessionID, tc.SessionID = tt.root, tt.session
			res, err := sk.Execute(context.Background(), mustJSON(t, map[string]any{"name": tt.skill}), tc)
			if tt.wantErr {
				if err == nil || !strings.Contains(err.Error(), "unknown skill") {
					t.Fatalf("err = %v, want unknown skill", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(res.Output, tt.want) {
				t.Errorf("output = %q, want %q", res.Output, tt.want)
			}
		})
	}
}
