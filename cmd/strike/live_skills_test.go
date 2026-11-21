package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jonathanung/strike/internal/product/config"
)

func skillNames(skills []config.Skill) []string {
	out := make([]string, len(skills))
	for i, s := range skills {
		out[i] = s.Name
	}
	return out
}

func sameNames(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestLiveSkillsForInput(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "alpha.md")
	if err := os.WriteFile(present, []byte("A"), 0o600); err != nil {
		t.Fatal(err)
	}
	alpha := config.Skill{Name: "alpha", Template: "A", Path: present}
	gone := config.Skill{Name: "gone", Template: "G", Path: filepath.Join(dir, "gone.md")}
	builtin := config.Skill{Name: "ship", Template: "S", Builtin: true}
	beta := config.Skill{Name: "beta", Template: "B $ARGUMENTS"}
	tests := []struct {
		name      string
		initial   []config.Skill
		next      []config.Skill
		err       error
		input     string
		wantLoads int
		want      []string
	}{
		{name: "plain text reads nothing", initial: []config.Skill{alpha}, next: []config.Skill{alpha, beta}, input: "fix the build", want: []string{"alpha"}},
		{name: "cached skill reads nothing", initial: []config.Skill{alpha}, next: []config.Skill{alpha, beta}, input: "/alpha go", want: []string{"alpha"}},
		{name: "cached builtin reads nothing", initial: []config.Skill{builtin}, next: []config.Skill{builtin, beta}, input: "$ship", want: []string{"ship"}},
		{name: "skill installed after start reloads", initial: []config.Skill{alpha}, next: []config.Skill{alpha, beta}, input: "/beta now", wantLoads: 1, want: []string{"alpha", "beta"}},
		{name: "dollar form reloads too", initial: []config.Skill{alpha}, next: []config.Skill{alpha, beta}, input: "  $beta now", wantLoads: 1, want: []string{"alpha", "beta"}},
		{name: "deleted skill file reloads", initial: []config.Skill{alpha, gone}, next: []config.Skill{alpha}, input: "/gone", wantLoads: 1, want: []string{"alpha"}},
		{name: "failed read keeps last good catalog", initial: []config.Skill{alpha}, err: errors.New("disk"), input: "/beta", wantLoads: 1, want: []string{"alpha"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loads := 0
			l := newLiveSkills(tt.initial, func() ([]config.Skill, error) {
				loads++
				return tt.next, tt.err
			})
			got := skillNames(l.ForInput(tt.input))
			if loads != tt.wantLoads {
				t.Fatalf("loads = %d, want %d", loads, tt.wantLoads)
			}
			if !sameNames(got, tt.want) {
				t.Fatalf("ForInput = %v, want %v", got, tt.want)
			}
			if infos := l.Skills(); len(infos) != len(tt.want) {
				t.Fatalf("Skills = %v, want %v", infos, tt.want)
			}
		})
	}
}

func TestLiveSkillsReloadForSkillTool(t *testing.T) {
	a := config.Skill{Name: "alpha"}
	b := config.Skill{Name: "beta"}
	l := newLiveSkills([]config.Skill{a}, func() ([]config.Skill, error) { return []config.Skill{a, b}, nil })
	if err := l.Reload(); err != nil {
		t.Fatal(err)
	}
	infos := l.Skills()
	if len(infos) != 2 || infos[1].Name != "beta" {
		t.Fatalf("Skills after Reload = %v", infos)
	}
	failing := newLiveSkills([]config.Skill{a}, func() ([]config.Skill, error) { return nil, errors.New("disk") })
	if err := failing.Reload(); err == nil {
		t.Fatal("Reload error was swallowed")
	}
	if infos := failing.Skills(); len(infos) != 1 || infos[0].Name != "alpha" {
		t.Fatalf("failed Reload lost the catalog: %v", infos)
	}
}

func TestLiveSkillsCopiesCatalog(t *testing.T) {
	l := newLiveSkills([]config.Skill{{Name: "alpha"}}, nil)
	got := l.ForInput("hello")
	got[0].Name = "mutated"
	if l.ForInput("hello")[0].Name != "alpha" {
		t.Fatal("caller mutation leaked into the catalog")
	}
}

func TestInvokedSkill(t *testing.T) {
	for in, want := range map[string]string{
		"/beta now": "beta",
		"$beta":     "beta",
		"  /mode x": "mode",
		"hello":     "",
		"":          "",
		"/":         "",
	} {
		if got := invokedSkill(in); got != want {
			t.Errorf("invokedSkill(%q) = %q, want %q", in, got, want)
		}
	}
}
