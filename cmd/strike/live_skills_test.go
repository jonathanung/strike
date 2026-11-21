package main

import (
	"errors"
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

func TestLiveSkillsReloadsOnUse(t *testing.T) {
	a := config.Skill{Name: "alpha", Template: "A"}
	b := config.Skill{Name: "beta", Template: "B $ARGUMENTS"}
	tests := []struct {
		name    string
		initial []config.Skill
		next    []config.Skill
		err     error
		want    []string
	}{
		{name: "skill installed after start appears", initial: []config.Skill{a}, next: []config.Skill{a, b}, want: []string{"alpha", "beta"}},
		{name: "removed skill disappears", initial: []config.Skill{a, b}, next: []config.Skill{b}, want: []string{"beta"}},
		{name: "failed read keeps last good catalog", initial: []config.Skill{a}, err: errors.New("disk"), want: []string{"alpha"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := newLiveSkills(tt.initial, func() ([]config.Skill, error) { return tt.next, tt.err })
			got := skillNames(l.Current())
			if len(got) != len(tt.want) {
				t.Fatalf("Current = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("Current = %v, want %v", got, tt.want)
				}
			}
			if err := l.Reload(); !errors.Is(err, tt.err) {
				t.Fatalf("Reload err = %v, want %v", err, tt.err)
			}
			infos := l.Skills()
			if len(infos) != len(tt.want) {
				t.Fatalf("Skills = %v, want %v", infos, tt.want)
			}
			for i := range infos {
				if infos[i].Name != tt.want[i] {
					t.Fatalf("Skills = %v, want %v", infos, tt.want)
				}
			}
		})
	}
}

func TestLiveSkillsCopiesCatalog(t *testing.T) {
	l := newLiveSkills([]config.Skill{{Name: "alpha"}}, nil)
	got := l.Current()
	got[0].Name = "mutated"
	if l.Current()[0].Name != "alpha" {
		t.Fatal("caller mutation leaked into the catalog")
	}
}
