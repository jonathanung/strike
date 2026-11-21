package config

import (
	"testing"

	"github.com/jonathanung/strike/harness/permission"
)

// The Git panel's "Review this branch" (#1487) spawns this profile and
// relies on its denies — enforced by the engine for children
// (permission.ChildAgentRules keeps every child Deny) — for its read-only
// posture. Loosening any of these makes the reviewer able to change the repo,
// run commands, or reach the network.
func TestBranchReviewerAgentIsReadOnly(t *testing.T) {
	var got *Agent
	for _, a := range BuiltinAgents() {
		if a.Name == "branch-reviewer" {
			a := a
			got = &a
		}
	}
	if got == nil {
		t.Fatal("builtin branch-reviewer agent missing")
	}
	if got.Description == "" || got.Prompt == "" {
		t.Fatalf("branch-reviewer needs a description and prompt: %+v", got)
	}
	for _, perm := range []string{
		"write", "edit", "bash", "task", "delegate", "team_task", "patch_collab",
		"verify", "browser", "webfetch", "websearch", "mcp",
		"memory_write", "issue_write", "plan_write", "plan_delegate",
		"artifact_write", "ledger_write", "todowrite", "question",
	} {
		if !rulesetHas(got.Permissions, perm, permission.Deny) {
			t.Errorf("branch-reviewer must deny %s: %+v", perm, got.Permissions)
		}
		if permission.Evaluate(perm, "*", got.Permissions) != permission.Deny {
			t.Errorf("branch-reviewer rules evaluate %s to non-deny", perm)
		}
	}
	for _, perm := range []string{"read", "glob", "grep", "git"} {
		if rulesetHas(got.Permissions, perm, permission.Deny) {
			t.Errorf("branch-reviewer must keep read-only %s", perm)
		}
	}
	// The child's denies survive the parent's ceiling unchanged.
	child := permission.ChildAgentRules([]permission.Ruleset{permission.Defaults()}, got.Permissions)
	if permission.Evaluate("edit", "x.go", permission.Defaults(), child) != permission.Deny {
		t.Error("child edit is not denied under default parent rules")
	}
	if permission.Evaluate("bash", "git push", permission.Defaults(), child) != permission.Deny {
		t.Error("child bash is not denied under default parent rules")
	}
}
