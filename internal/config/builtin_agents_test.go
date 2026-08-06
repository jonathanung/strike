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
	for _, want := range []string{"explore", "general", "commit", "reviewer", "tester", "debugger", "validator", "orchestrator", "pr-babysitter"} {
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
	v := byName["validator"]
	if !rulesetHas(v.Permissions, "write", permission.Deny) || !rulesetHas(v.Permissions, "edit", permission.Deny) {
		t.Errorf("validator missing write/edit deny: %+v", v.Permissions)
	}
	if !rulesetHas(v.Permissions, "task", permission.Deny) {
		t.Errorf("validator missing task deny: %+v", v.Permissions)
	}
	if !rulesetHas(v.Permissions, "bash", permission.Allow) {
		t.Errorf("validator missing bash allow: %+v", v.Permissions)
	}
	if !strings.Contains(v.Prompt, "PASS") || !strings.Contains(v.Prompt, "FAIL") {
		t.Errorf("validator prompt missing PASS/FAIL duties: %q", v.Prompt)
	}
	r := byName["reviewer"]
	if !strings.Contains(r.Prompt, "blocked: reviewer requires read-only git/gh access") || !strings.Contains(r.Prompt, "permission-denied") {
		t.Errorf("reviewer prompt missing actionable git/gh denial behavior: %q", r.Prompt)
	}
	o := byName["orchestrator"]
	if rulesetHas(o.Permissions, "task", permission.Deny) {
		t.Errorf("orchestrator must not deny task: %+v", o.Permissions)
	}
	if !rulesetHas(o.Permissions, "task", permission.Allow) {
		t.Errorf("orchestrator missing task allow: %+v", o.Permissions)
	}
	if !strings.Contains(strings.ToLower(o.Description), "subagent") && !strings.Contains(strings.ToLower(o.Description), "coordinate") {
		t.Errorf("orchestrator description should mention coordinate/subagents: %q", o.Description)
	}
	if !strings.Contains(o.Prompt, "task") || !strings.Contains(o.Prompt, "MaxChildDepth") {
		t.Errorf("orchestrator prompt missing delegate/MaxChildDepth duties: %q", o.Prompt)
	}
	for _, needle := range []string{
		"agent_message",
		"child.completed",
		"busy-poll",
		"mid-flight",
		"task_status",
		"chatty",
		"files_changed",
		"handoff",
	} {
		if !strings.Contains(o.Prompt, needle) {
			t.Errorf("orchestrator prompt missing coordination needle %q: %q", needle, o.Prompt)
		}
	}
	g := byName["general"]
	if !strings.Contains(g.Prompt, "agent_message") || !strings.Contains(strings.ToLower(g.Prompt), "block") {
		t.Errorf("general prompt should teach early blocker messaging: %q", g.Prompt)
	}
	if !strings.Contains(g.Prompt, "files_changed") || !strings.Contains(g.Prompt, "recommended_next_action") {
		t.Errorf("general prompt should teach structured completion handoff: %q", g.Prompt)
	}
	// general is a multi-step implementer (root or task child): bash must be
	// allow so it is not stuck on every shell prompt (#651).
	if !rulesetHas(g.Permissions, "bash", permission.Allow) {
		t.Errorf("general missing bash allow: %+v", g.Permissions)
	}
	if !rulesetHas(g.Permissions, "task", permission.Deny) {
		t.Errorf("general missing task deny: %+v", g.Permissions)
	}
	if got := permission.Evaluate("bash", "ls", permission.Defaults(), g.Permissions); got != permission.Allow {
		t.Errorf("Evaluate bash with general profile = %q, want allow", got)
	}
	if err := ValidateAgentName("pr-babysitter"); err != nil {
		t.Fatalf("ValidateAgentName(pr-babysitter) = %v", err)
	}
	pb := byName["pr-babysitter"]
	if !rulesetHas(pb.Permissions, "task", permission.Deny) {
		t.Errorf("pr-babysitter missing task deny: %+v", pb.Permissions)
	}
	if !rulesetHas(pb.Permissions, "bash", permission.Allow) {
		t.Errorf("pr-babysitter missing bash allow: %+v", pb.Permissions)
	}
	if !rulesetHas(pb.Permissions, "write", permission.Allow) || !rulesetHas(pb.Permissions, "edit", permission.Allow) {
		t.Errorf("pr-babysitter missing write/edit allow: %+v", pb.Permissions)
	}
	if !strings.Contains(pb.Prompt, "gh pr checks") || !strings.Contains(pb.Prompt, "--no-verify") {
		t.Errorf("pr-babysitter prompt missing CI watch / hard forbids: %q", pb.Prompt)
	}
	if !strings.Contains(pb.Prompt, "issue-handler") {
		t.Errorf("pr-babysitter prompt should note issue-handler skill overlap: %q", pb.Prompt)
	}
	if !strings.Contains(strings.ToLower(pb.Description), "pr") && !strings.Contains(strings.ToLower(pb.Description), "ci") {
		t.Errorf("pr-babysitter description should mention PR/CI: %q", pb.Description)
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
