package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	if w.SchemaVersion != WorkflowSchemaVersion {
		t.Fatalf("schemaVersion = %d", w.SchemaVersion)
	}
	if w.Source != WorkflowSourceBuiltin {
		t.Fatalf("source = %q", w.Source)
	}
	if w.Fingerprint == "" {
		t.Fatal("missing fingerprint")
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
	  "schemaVersion": 1,
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
	if w.SchemaVersion != 1 {
		t.Fatalf("schemaVersion = %d", w.SchemaVersion)
	}
	if w.Fingerprint == "" {
		t.Fatal("missing fingerprint")
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
	if w.SchemaVersion != WorkflowSchemaVersion {
		t.Fatalf("schemaVersion default = %d", w.SchemaVersion)
	}
}

func TestParseWorkflowRejectsUnknownFields(t *testing.T) {
	raw := `{"name":"x","phases":[{"name":"a","exit":{"type":"agent"}}],"branching":true}`
	_, err := ParseWorkflow([]byte(raw))
	if err == nil {
		t.Fatal("expected unknown field error")
	}
	if !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseWorkflowRejectsUnknownPhaseFields(t *testing.T) {
	raw := `{"name":"x","phases":[{"name":"a","fork":true,"exit":{"type":"agent"}}]}`
	_, err := ParseWorkflow([]byte(raw))
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseWorkflowRejectsUnsupportedSchemaVersion(t *testing.T) {
	raw := `{"schemaVersion":99,"name":"x","phases":[{"name":"a","exit":{"type":"agent"}}]}`
	_, err := ParseWorkflow([]byte(raw))
	if err == nil || !strings.Contains(err.Error(), "unsupported version") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseWorkflowRejectsTrailingJunk(t *testing.T) {
	raw := `{"name":"x","phases":[{"name":"a","exit":{"type":"agent"}}]}{"extra":1}`
	_, err := ParseWorkflow([]byte(raw))
	if err == nil {
		t.Fatal("expected trailing data error")
	}
}

func TestParseWorkflowSourceLocations(t *testing.T) {
	raw := `{"name":"","phases":[]}`
	_, err := ParseWorkflowSource([]byte(raw), "/tmp/wf.json")
	if err == nil {
		t.Fatal("expected errors")
	}
	s := err.Error()
	if !strings.Contains(s, "/tmp/wf.json") {
		t.Fatalf("missing source in %q", s)
	}
	// Multi-error: empty name + no phases.
	if !strings.Contains(s, "name") || !strings.Contains(s, "no phases") {
		t.Fatalf("want multi-error, got %q", s)
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
		{"bad phase name control", Workflow{Name: "x", Phases: []Phase{
			{Name: "a\x00b", Exit: ExitGate{Type: GateAgent}},
		}}, "control"},
		{"bad agent whitespace", Workflow{Name: "x", Phases: []Phase{
			{Name: "a", Agent: " build", Exit: ExitGate{Type: GateAgent}},
		}}, "whitespace"},
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

func TestValidateWorkflowMultiError(t *testing.T) {
	w := Workflow{
		Name: "",
		Phases: []Phase{
			{Name: "", Exit: ExitGate{Type: "nope"}},
			{Name: "ok", Exit: ExitGate{Type: GateCheck}},
		},
	}
	err := ValidateWorkflow(w)
	if err == nil {
		t.Fatal("expected errors")
	}
	s := err.Error()
	for _, want := range []string{"name is empty", "phase name is empty", "unknown gate", "command"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in %q", want, s)
		}
	}
}

func TestValidateWorkflowAgents(t *testing.T) {
	w := Workflow{
		Name: "x",
		Phases: []Phase{
			{Name: "a", Agent: "build", Exit: ExitGate{Type: GateAgent}},
			{Name: "b", Agent: "missing", Exit: ExitGate{Type: GateAgent}},
			{Name: "c", Exit: ExitGate{Type: GateAgent}}, // empty agent ok
		},
	}
	known := map[string]struct{}{"build": {}}
	err := ValidateWorkflowAgents(w, known)
	if err == nil || !strings.Contains(err.Error(), `unknown agent "missing"`) {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "build") {
		t.Fatalf("should not flag known agent: %v", err)
	}
	// Nil known skips agent checks.
	if err := ValidateWorkflowAgents(w, nil); err != nil {
		t.Fatal(err)
	}
}

func TestAgentNameSet(t *testing.T) {
	set := AgentNameSet([]Agent{{Name: "build"}, {Name: "plan"}, {Name: ""}})
	if _, ok := set["build"]; !ok {
		t.Fatal("missing build")
	}
	if _, ok := set[""]; ok {
		t.Fatal("empty name should be skipped")
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

func TestFormatWorkflowRoundTrip(t *testing.T) {
	raw := `{
  "schemaVersion": 1,
  "name": "ship",
  "description": "Ship it",
  "phases": [
    {
      "name": "review",
      "agent": "reviewer",
      "context": "be careful",
      "permissions": [
        {
          "permission": "write",
          "pattern": "*",
          "action": "deny"
        }
      ],
      "exit": {
        "type": "user"
      }
    },
    {
      "name": "land",
      "agent": "build",
      "exit": {
        "type": "check",
        "command": "make test"
      }
    }
  ]
}
`
	w, err := ParseWorkflow([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	formatted, err := FormatWorkflow(w)
	if err != nil {
		t.Fatal(err)
	}
	// Deterministic: format twice is identical.
	again, err := FormatWorkflow(w)
	if err != nil {
		t.Fatal(err)
	}
	if string(formatted) != string(again) {
		t.Fatalf("format not deterministic:\n%s\n---\n%s", formatted, again)
	}
	// Round-trip preserves semantics.
	w2, err := ParseWorkflow(formatted)
	if err != nil {
		t.Fatal(err)
	}
	if w2.Name != w.Name || w2.Description != w.Description || len(w2.Phases) != len(w.Phases) {
		t.Fatalf("round-trip meta: %#v vs %#v", w2, w)
	}
	for i := range w.Phases {
		a, b := w.Phases[i], w2.Phases[i]
		if a.Name != b.Name || a.Agent != b.Agent || a.Context != b.Context ||
			a.Exit.Type != b.Exit.Type || a.Exit.Command != b.Exit.Command {
			t.Fatalf("phase %d: %#v vs %#v", i, a, b)
		}
		if len(a.Permissions) != len(b.Permissions) {
			t.Fatalf("perms len %d vs %d", len(a.Permissions), len(b.Permissions))
		}
	}
	// Fingerprint matches hash of formatted bytes.
	sum := sha256.Sum256(formatted)
	want := hex.EncodeToString(sum[:])
	fp, err := WorkflowFingerprint(w)
	if err != nil {
		t.Fatal(err)
	}
	if fp != want || w.Fingerprint != want {
		t.Fatalf("fingerprint = %q want %q (parsed %q)", fp, want, w.Fingerprint)
	}
	// Runtime fields must not appear in formatted JSON.
	if strings.Contains(string(formatted), `"Source"`) || strings.Contains(string(formatted), `"Fingerprint"`) {
		t.Fatalf("runtime fields leaked: %s", formatted)
	}
}

func TestScaffoldWorkflow(t *testing.T) {
	w, err := ScaffoldWorkflow("my-flow")
	if err != nil {
		t.Fatal(err)
	}
	if w.Name != "my-flow" || w.SchemaVersion != WorkflowSchemaVersion {
		t.Fatalf("%#v", w)
	}
	if err := ValidateWorkflow(w); err != nil {
		t.Fatal(err)
	}
	if _, err := ScaffoldWorkflow(""); err == nil {
		t.Fatal("empty name should fail")
	}
	if _, err := ScaffoldWorkflow("bad\x00name"); err == nil {
		t.Fatal("control char should fail")
	}
}

func TestWriteWorkflowFileNoOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "w.json")
	w, err := ScaffoldWorkflow("demo")
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteWorkflowFile(path, w, false); err != nil {
		t.Fatal(err)
	}
	if err := WriteWorkflowFile(path, w, false); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("err = %v", err)
	}
	// force overwrites
	w.Description = "updated"
	if err := WriteWorkflowFile(path, w, true); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "updated") {
		t.Fatalf("content = %s", data)
	}
	// Writing does not "activate" — just a file on disk; LoadWorkflows still
	// only returns definitions (activation is engine/catalog).
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, ok := decoded["active"]; ok {
		t.Fatal("scaffold must not mark active")
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
	if got := byName["plan-implement"]; got.Source != WorkflowSourceProject || got.Path == "" || got.Fingerprint == "" {
		t.Fatalf("override diagnostics = source=%q path=%q fp=%q", got.Source, got.Path, got.Fingerprint)
	}
	if _, ok := byName["ship"]; !ok {
		t.Fatalf("missing ship: %#v", byName)
	}
	if got := byName["review-fix"]; got.Name != "review-fix" || len(got.Phases) != 2 {
		t.Fatalf("builtin review-fix missing: %#v", got)
	}
	if got := byName["review-fix"]; got.Source != WorkflowSourceBuiltin {
		t.Fatalf("builtin source = %q", got.Source)
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

func TestLoadWorkflowsGlobalLayer(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".strike", "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"name":"global-only","phases":[{"name":"g","exit":{"type":"agent"}}]}`
	if err := os.WriteFile(filepath.Join(dir, "g.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	ws, err := LoadWorkflows(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	got, ok := FindWorkflow(ws, "global-only")
	if !ok || got.Source != WorkflowSourceGlobal {
		t.Fatalf("got = %#v ok=%v", got, ok)
	}
}

func TestLoadWorkflowsDuplicateSameLayer(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()
	dir := filepath.Join(work, ".strike", "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"name":"dup","phases":[{"name":"a","exit":{"type":"agent"}}]}`
	for _, f := range []string{"a.json", "b.json"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_, err := LoadWorkflows(work)
	if err == nil || !strings.Contains(err.Error(), "duplicate workflow") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadWorkflowsRejectsUnknownFields(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()
	dir := filepath.Join(work, ".strike", "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"name":"bad","extra":1,"phases":[{"name":"a","exit":{"type":"agent"}}]}`
	if err := os.WriteFile(filepath.Join(dir, "bad.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadWorkflows(work)
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("err = %v", err)
	}
}

func TestWorkflowDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()

	g, err := WorkflowDir("global", "")
	if err != nil {
		t.Fatal(err)
	}
	if g != filepath.Join(home, ".strike", "workflows") {
		t.Fatalf("global = %q", g)
	}
	p, err := WorkflowDir("project", work)
	if err != nil {
		t.Fatal(err)
	}
	if p != filepath.Join(work, ".strike", "workflows") {
		t.Fatalf("project = %q", p)
	}
	if _, err := WorkflowDir("project", ""); err == nil {
		t.Fatal("project without workDir should fail")
	}
	if _, err := WorkflowDir("cloud", work); err == nil {
		t.Fatal("unknown scope should fail")
	}
}

func TestValidateWorkflowName(t *testing.T) {
	if err := ValidateWorkflowName("ok-name"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateWorkflowName(""); err == nil {
		t.Fatal("empty")
	}
	if err := ValidateWorkflowName(" x"); err == nil {
		t.Fatal("leading space")
	}
	for _, bad := range []string{"../escape", "a/b", `a\b`, "..", "."} {
		if err := ValidateWorkflowName(bad); err == nil {
			t.Fatalf("expected reject %q", bad)
		}
	}
}

func TestScaffoldRejectsPathTraversalName(t *testing.T) {
	if _, err := ScaffoldWorkflow("../evil"); err == nil {
		t.Fatal("expected path traversal name rejected")
	}
}
