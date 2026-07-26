package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/permission"
)

func TestBuiltinAgentsCatalog(t *testing.T) {
	agents := BuiltinAgents()
	if len(agents) < 2 || agents[0].Name != "build" || agents[1].Name != "plan" {
		t.Fatalf("build/plan must lead: %+v", agents)
	}
	if agents[0].Prompt != "" || agents[1].Prompt != "" {
		t.Fatalf("build/plan must keep empty Prompt for engine overlays: build=%q plan=%q", agents[0].Prompt, agents[1].Prompt)
	}
	byName := map[string]Agent{}
	for _, a := range agents {
		byName[a.Name] = a
	}
	for _, want := range []string{"explore", "general", "commit", "reviewer", "tester", "debugger"} {
		a, ok := byName[want]
		if !ok {
			t.Fatalf("missing builtin agent %q among %+v", want, agentNames(agents))
		}
		if strings.TrimSpace(a.Description) == "" {
			t.Errorf("%s: empty description", want)
		}
		if strings.TrimSpace(a.Prompt) == "" {
			t.Errorf("%s: empty prompt body", want)
		}
	}
	// explore must hard-deny mutations.
	if !rulesetHas(byName["explore"].Permissions, "write", permission.Deny) {
		t.Errorf("explore missing write deny: %+v", byName["explore"].Permissions)
	}
	if !rulesetHas(byName["commit"].Permissions, "edit", permission.Deny) {
		t.Errorf("commit missing edit deny: %+v", byName["commit"].Permissions)
	}
}

func TestLoadAgentsMergesBuiltinsAndProjectOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()

	agents, err := LoadAgentsWithError(work)
	if err != nil {
		t.Fatal(err)
	}
	if agents[0].Name != "build" {
		t.Fatalf("first agent = %q, want build", agents[0].Name)
	}
	if !agentNamesHas(agents, "explore") || !agentNamesHas(agents, "commit") {
		t.Fatalf("missing explore/commit in %+v", agentNames(agents))
	}

	dir := filepath.Join(work, ".strike", "agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\ndescription: custom explore\npermission.write: deny\n---\nCustom explore body.\n"
	if err := os.WriteFile(filepath.Join(dir, "explore.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	agents, err = LoadAgentsWithError(work)
	if err != nil {
		t.Fatal(err)
	}
	var explore Agent
	for _, a := range agents {
		if a.Name == "explore" {
			explore = a
		}
	}
	if explore.Description != "custom explore" || explore.Prompt != "Custom explore body." {
		t.Fatalf("explore override = %+v", explore)
	}
	if !agentNamesHas(agents, "commit") {
		t.Fatal("commit builtin dropped after explore override")
	}
}

func TestMergeAgentsOrder(t *testing.T) {
	base := []Agent{{Name: "a", Prompt: "A"}, {Name: "b", Prompt: "B"}}
	overlay := []Agent{{Name: "b", Prompt: "B2"}, {Name: "c", Prompt: "C"}}
	got := mergeAgents(base, overlay)
	if len(got) != 3 || got[0].Name != "a" || got[1].Name != "b" || got[2].Name != "c" {
		t.Fatalf("order = %+v", got)
	}
	if got[1].Prompt != "B2" {
		t.Fatalf("overlay = %+v", got[1])
	}
}

func agentNames(agents []Agent) []string {
	out := make([]string, len(agents))
	for i, a := range agents {
		out[i] = a.Name
	}
	return out
}

func agentNamesHas(agents []Agent, name string) bool {
	for _, a := range agents {
		if a.Name == name {
			return true
		}
	}
	return false
}

func rulesetHas(rs permission.Ruleset, perm string, action permission.Action) bool {
	for _, r := range rs {
		if r.Permission == perm && r.Action == action {
			return true
		}
	}
	return false
}
