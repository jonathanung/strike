package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/permission"
)

func TestBuiltinPlanImplement(t *testing.T) {
	w := BuiltinPlanImplement()
	if err := ValidateWorkflow(w); err != nil {
		t.Fatal(err)
	}
	if w.Name != "plan-implement" {
		t.Fatalf("name = %q", w.Name)
	}
	if len(w.Phases) != 2 {
		t.Fatalf("phases = %d", len(w.Phases))
	}
	plan := w.Phases[0]
	if plan.Name != "plan" || plan.Exit.Type != GateUser {
		t.Fatalf("plan phase = %#v", plan)
	}
	var sawWriteDeny, sawEditDeny bool
	for _, r := range plan.Permissions {
		if r.Permission == "write" && r.Action == permission.Deny {
			sawWriteDeny = true
		}
		if r.Permission == "edit" && r.Action == permission.Deny {
			sawEditDeny = true
		}
	}
	if !sawWriteDeny || !sawEditDeny {
		t.Fatalf("plan permissions missing write/edit deny: %#v", plan.Permissions)
	}
	if w.Phases[1].Name != "implement" || w.Phases[1].Exit.Type != GateAgent {
		t.Fatalf("implement = %#v", w.Phases[1])
	}
}

func TestBuiltinReviewFix(t *testing.T) {
	w := BuiltinReviewFix()
	if err := ValidateWorkflow(w); err != nil {
		t.Fatal(err)
	}
	if w.Name != "review-fix" {
		t.Fatalf("name = %q", w.Name)
	}
	if len(w.Phases) != 2 {
		t.Fatalf("phases = %d", len(w.Phases))
	}
	review := w.Phases[0]
	if review.Name != "review" || review.Agent != "reviewer" || review.Exit.Type != GateUser {
		t.Fatalf("review phase = %#v", review)
	}
	var sawWriteDeny, sawEditDeny bool
	for _, r := range review.Permissions {
		if r.Permission == "write" && r.Action == permission.Deny {
			sawWriteDeny = true
		}
		if r.Permission == "edit" && r.Action == permission.Deny {
			sawEditDeny = true
		}
	}
	if !sawWriteDeny || !sawEditDeny {
		t.Fatalf("review permissions missing write/edit deny: %#v", review.Permissions)
	}
	fix := w.Phases[1]
	if fix.Name != "fix" || fix.Agent != "build" || fix.Exit.Type != GateCheck || fix.Exit.Command != "make test" {
		t.Fatalf("fix phase = %#v", fix)
	}
}

func TestBuiltinWorkflows(t *testing.T) {
	ws := BuiltinWorkflows()
	if len(ws) < 2 {
		t.Fatalf("builtins = %#v", ws)
	}
	byName := map[string]Workflow{}
	for _, w := range ws {
		if err := ValidateWorkflow(w); err != nil {
			t.Fatalf("%s: %v", w.Name, err)
		}
		if _, dup := byName[w.Name]; dup {
			t.Fatalf("duplicate builtin %q", w.Name)
		}
		byName[w.Name] = w
	}
	if _, ok := byName["plan-implement"]; !ok {
		t.Fatal("missing plan-implement")
	}
	if _, ok := byName["review-fix"]; !ok {
		t.Fatal("missing review-fix")
	}
}

func TestParseWorkflow(t *testing.T) {
	raw := `{
	  "name": "review",
	  "phases": [
	    {"name": "draft", "exit": {"type": "agent"}},
	    {"name": "check", "exit": {"type": "check", "command": "make test"}}
	  ]
	}`
	w, err := ParseWorkflow([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if w.Phases[1].Exit.Command != "make test" {
		t.Fatalf("command = %q", w.Phases[1].Exit.Command)
	}
}

func TestParseWorkflowDefaultsGateAgent(t *testing.T) {
	raw := `{"name":"x","phases":[{"name":"a","exit":{}}]}`
	w, err := ParseWorkflow([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if w.Phases[0].Exit.Type != GateAgent {
		t.Fatalf("gate = %q", w.Phases[0].Exit.Type)
	}
}

func TestValidateWorkflowErrors(t *testing.T) {
	cases := []struct {
		name string
		w    Workflow
		want string
	}{
		{"empty name", Workflow{Phases: []Phase{{Name: "a", Exit: ExitGate{Type: GateAgent}}}}, "name is empty"},
		{"no phases", Workflow{Name: "x"}, "no phases"},
		{"dup phase", Workflow{Name: "x", Phases: []Phase{
			{Name: "a", Exit: ExitGate{Type: GateAgent}},
			{Name: "a", Exit: ExitGate{Type: GateAgent}},
		}}, "duplicate"},
		{"bad check", Workflow{Name: "x", Phases: []Phase{
			{Name: "a", Exit: ExitGate{Type: GateCheck}},
		}}, "command"},
		{"bad gate", Workflow{Name: "x", Phases: []Phase{
			{Name: "a", Exit: ExitGate{Type: "maybe"}},
		}}, "unknown gate"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateWorkflow(tc.w)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestFindWorkflow(t *testing.T) {
	ws := []Workflow{
		{Name: "alpha", Phases: []Phase{{Name: "a", Exit: ExitGate{Type: GateAgent}}}},
		{Name: "beta", Phases: []Phase{{Name: "b", Exit: ExitGate{Type: GateUser}}}},
	}
	got, ok := FindWorkflow(ws, "beta")
	if !ok || got.Name != "beta" || len(got.Phases) != 1 || got.Phases[0].Name != "b" {
		t.Fatalf("FindWorkflow(beta) = %#v ok=%v", got, ok)
	}
	if _, ok := FindWorkflow(ws, "missing"); ok {
		t.Fatal("FindWorkflow(missing) should be false")
	}
	if _, ok := FindWorkflow(nil, "alpha"); ok {
		t.Fatal("FindWorkflow(nil) should be false")
	}
	if _, ok := FindWorkflow(ws, ""); ok {
		t.Fatal("FindWorkflow(empty name) should be false")
	}
}

func TestLoadWorkflowsIncludesBuiltinAndOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()
	dir := filepath.Join(work, ".strike", "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Override built-in name with a custom single-phase workflow.
	custom := `{"name":"plan-implement","description":"custom","phases":[{"name":"only","exit":{"type":"agent"}}]}`
	if err := os.WriteFile(filepath.Join(dir, "plan.json"), []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}
	extra := `{"name":"ship","phases":[{"name":"go","exit":{"type":"user"}}]}`
	if err := os.WriteFile(filepath.Join(dir, "ship.json"), []byte(extra), 0o644); err != nil {
		t.Fatal(err)
	}
	ws, err := LoadWorkflows(work)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Workflow{}
	for _, w := range ws {
		byName[w.Name] = w
	}
	if got := byName["plan-implement"]; len(got.Phases) != 1 || got.Phases[0].Name != "only" {
		t.Fatalf("override = %#v", got)
	}
	if _, ok := byName["ship"]; !ok {
		t.Fatalf("missing ship: %#v", byName)
	}
	if got := byName["review-fix"]; got.Name != "review-fix" || len(got.Phases) != 2 {
		t.Fatalf("builtin review-fix missing: %#v", got)
	}
}

func TestLoadWorkflowsEmptyWorkDirSkipsProjectLayer(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// A cwd-relative workflows/ or .strike/workflows must not be treated as
	// the project layer when workDir is empty.
	cwd := t.TempDir()
	t.Chdir(cwd)
	for _, dir := range []string{
		filepath.Join(cwd, "workflows"),
		filepath.Join(cwd, ".strike", "workflows"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		body := `{"name":"cwd-leak","phases":[{"name":"x","exit":{"type":"user"}}]}`
		if err := os.WriteFile(filepath.Join(dir, "leak.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ws, err := LoadWorkflows("")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := FindWorkflow(ws, "cwd-leak"); ok {
		t.Fatal("empty workDir loaded cwd workflow as project layer")
	}
}
