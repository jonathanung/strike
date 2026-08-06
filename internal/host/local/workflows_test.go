package local

import (
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/config"
	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/permission"
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
