package local

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/harness/permission"
	"github.com/jonathanung/strike-cli/internal/frontend/host"
	"github.com/jonathanung/strike-cli/internal/product/config"
)

func TestNewWorkflowsSummaries(t *testing.T) {
	list := []config.Workflow{
		config.BuiltinPlanImplement(),
		{
			Name:          "custom",
			Description:   "project custom",
			Source:        config.WorkflowSourceProject,
			Fingerprint:   "abc123",
			Path:          "/tmp/custom.json",
			SchemaVersion: config.WorkflowSchemaVersion,
			Phases: []config.Phase{
				{
					Name: "one",
					Exit: config.ExitGate{Type: config.GateAgent},
					Permissions: permission.Ruleset{
						{Permission: "bash", Pattern: "*", Action: permission.Allow},
					},
				},
			},
		},
	}
	// Ensure fingerprints/sources are set on builtins.
	list[0] = config.BuiltinPlanImplement()

	cat := NewWorkflows(list)
	if cat == nil {
		t.Fatal("NewWorkflows returned nil")
	}
	got := cat.List()
	if len(got) != 2 {
		t.Fatalf("List len = %d, want 2", len(got))
	}
	plan, ok := cat.Get("plan-implement")
	if !ok {
		t.Fatal("Get(plan-implement) missing")
	}
	if plan.Source != host.WorkflowSourceBuiltin {
		t.Errorf("source = %q, want builtin", plan.Source)
	}
	if !plan.Valid {
		t.Errorf("plan-implement Valid=false: %s", plan.ValidationError)
	}
	if len(plan.Phases) != 2 {
		t.Fatalf("phases = %d, want 2", len(plan.Phases))
	}
	if plan.Phases[0].Gate != "user" {
		t.Errorf("plan gate = %q, want user", plan.Phases[0].Gate)
	}
	if len(plan.Phases[0].Permissions) == 0 {
		t.Error("plan phase missing permission summary")
	}
	custom, ok := cat.Get("custom")
	if !ok || custom.Source != host.WorkflowSourceProject {
		t.Fatalf("custom = %+v ok=%v", custom, ok)
	}
	if custom.Fingerprint != "abc123" || custom.Path != "/tmp/custom.json" {
		t.Errorf("custom fingerprint/path = %q/%q", custom.Fingerprint, custom.Path)
	}
	if len(custom.Phases[0].Permissions) != 1 || custom.Phases[0].Permissions[0].Action != "allow" {
		t.Errorf("custom perms = %+v", custom.Phases[0].Permissions)
	}
	if _, ok := cat.Get("missing"); ok {
		t.Error("Get(missing) should be false")
	}
	if _, ok := cat.Get(""); ok {
		t.Error("Get(\"\") should be false")
	}
}

func TestNewWorkflowsWithErrorsMarksInvalid(t *testing.T) {
	cat := NewWorkflowsWithErrors(
		[]config.Workflow{config.BuiltinReviewFix()},
		[]host.WorkflowSummary{{
			Name:            "broken",
			Source:          host.WorkflowSourceGlobal,
			ValidationError: "no phases",
		}},
	)
	broken, ok := cat.Get("broken")
	if !ok || broken.Valid {
		t.Fatalf("broken = %+v ok=%v", broken, ok)
	}
	if !strings.Contains(broken.ValidationError, "no phases") {
		t.Errorf("ValidationError = %q", broken.ValidationError)
	}
	if rev, ok := cat.Get("review-fix"); !ok || !rev.Valid {
		t.Fatalf("review-fix = %+v ok=%v", rev, ok)
	}
}

func TestNewWorkflowsEmpty(t *testing.T) {
	cat := NewWorkflows(nil)
	if cat == nil {
		t.Fatal("nil catalog")
	}
	if got := cat.List(); got != nil {
		t.Errorf("List = %#v, want nil", got)
	}
}

func TestWorkflowsDocumentScaffoldFormat(t *testing.T) {
	cat := NewWorkflows([]config.Workflow{config.BuiltinPlanImplement()})
	doc, ok := cat.Document("plan-implement")
	if !ok {
		t.Fatal("Document missing")
	}
	if doc.Name != "plan-implement" || len(doc.Phases) != 2 {
		t.Fatalf("doc = %+v", doc)
	}
	// Context round-trips for review-fix
	cat2 := NewWorkflows([]config.Workflow{config.BuiltinReviewFix()})
	rev, ok := cat2.Document("review-fix")
	if !ok || rev.Phases[0].Context == "" {
		t.Fatalf("review context missing: %+v", rev)
	}

	scaff, err := cat.Scaffold("demo-wf")
	if err != nil {
		t.Fatal(err)
	}
	if scaff.Name != "demo-wf" || len(scaff.Phases) != 1 {
		t.Fatalf("scaffold = %+v", scaff)
	}
	if err := cat.Validate(scaff); err != nil {
		t.Fatalf("scaffold invalid: %v", err)
	}
	raw, err := cat.Format(scaff)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, `"name": "demo-wf"`) || !strings.Contains(raw, `"schemaVersion": 1`) {
		t.Fatalf("format = %s", raw)
	}
	grants := cat.PhaseGrants(doc, 0)
	if len(grants) < 2 {
		t.Fatalf("phase0 grants = %+v", grants)
	}
	if cat.PhaseGrants(doc, 99) != nil {
		t.Fatal("out of range grants")
	}
}

func TestWorkflowsValidateAgents(t *testing.T) {
	cat := NewWorkflowsWithOpts(nil, nil, WorkflowsOpts{
		Agents: []string{"build"},
	})
	doc, err := cat.Scaffold("a")
	if err != nil {
		t.Fatal(err)
	}
	doc.Phases[0].Agent = "nope"
	if err := cat.Validate(doc); err == nil || !strings.Contains(err.Error(), "unknown agent") {
		t.Fatalf("want unknown agent, got %v", err)
	}
	doc.Phases[0].Agent = "build"
	if err := cat.Validate(doc); err != nil {
		t.Fatal(err)
	}
}

func TestWorkflowsSaveProjectAtomicNoActivate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()
	// Ensure project .strike root exists via writing under work.
	if err := os.MkdirAll(filepath.Join(work, ".strike"), 0o755); err != nil {
		t.Fatal(err)
	}

	cat := NewWorkflowsWithOpts(nil, nil, WorkflowsOpts{
		WorkDir: work,
		Agents:  []string{"build"},
	})
	doc, err := cat.Scaffold("saved-flow")
	if err != nil {
		t.Fatal(err)
	}
	doc.Description = "from builder"
	doc.Phases[0].Permissions = []host.WorkflowPermission{
		{Permission: "write", Pattern: "*", Action: "deny"},
	}

	path, err := cat.Save(doc, host.WorkflowScopeProject, false)
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(work, ".strike", "workflows", "saved-flow.json")
	if path != wantPath {
		t.Fatalf("path = %q want %q", path, wantPath)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"name": "saved-flow"`) {
		t.Fatalf("file = %s", raw)
	}
	// Catalog updated
	sum, ok := cat.Get("saved-flow")
	if !ok || !sum.Valid || sum.Source != host.WorkflowSourceProject {
		t.Fatalf("catalog = %+v ok=%v", sum, ok)
	}
	// No second write without force
	if _, err := cat.Save(doc, host.WorkflowScopeProject, false); !errors.Is(err, host.ErrWorkflowExists) {
		t.Fatalf("exists err = %v", err)
	}
	// Force overwrite
	doc.Description = "updated"
	if _, err := cat.Save(doc, host.WorkflowScopeProject, true); err != nil {
		t.Fatal(err)
	}
	got, _ := cat.Document("saved-flow")
	if got.Description != "updated" {
		t.Fatalf("desc = %q", got.Description)
	}
}

func TestWorkflowsSaveGlobal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cat := NewWorkflowsWithOpts(nil, nil, WorkflowsOpts{Agents: []string{"build"}})
	doc, _ := cat.Scaffold("g-flow")
	path, err := cat.Save(doc, host.WorkflowScopeGlobal, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(path, filepath.Join(home, ".strike", "workflows", "g-flow.json")) {
		// GlobalRoot may resolve under home/.strike
		if _, statErr := os.Stat(path); statErr != nil {
			t.Fatalf("path %q missing: %v", path, statErr)
		}
	}
	sum, ok := cat.Get("g-flow")
	if !ok || sum.Source != host.WorkflowSourceGlobal {
		t.Fatalf("sum = %+v ok=%v", sum, ok)
	}
}

func TestWorkflowsSaveRejectsInvalid(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()
	_ = os.MkdirAll(filepath.Join(work, ".strike"), 0o755)
	cat := NewWorkflowsWithOpts(nil, nil, WorkflowsOpts{WorkDir: work})
	doc := host.WorkflowDocument{Name: "x"} // no phases
	_, err := cat.Save(doc, host.WorkflowScopeProject, true)
	if !errors.Is(err, host.ErrWorkflowInvalid) {
		t.Fatalf("err = %v", err)
	}
	// ensure nothing written
	entries, _ := os.ReadDir(filepath.Join(work, ".strike", "workflows"))
	if len(entries) != 0 {
		t.Fatalf("wrote files on invalid: %v", entries)
	}
}

func TestWorkflowsSaveUnknownScope(t *testing.T) {
	cat := NewWorkflows(nil)
	doc, _ := cat.Scaffold("x")
	if _, err := cat.Save(doc, "cloud", true); err == nil {
		t.Fatal("expected scope error")
	}
}

func TestDocumentCloneIndependent(t *testing.T) {
	cat := NewWorkflows([]config.Workflow{config.BuiltinPlanImplement()})
	doc, _ := cat.Document("plan-implement")
	doc.Phases[0].Name = "mutated"
	doc2, _ := cat.Document("plan-implement")
	if doc2.Phases[0].Name == "mutated" {
		t.Fatal("Document should return a clone")
	}
}
