package tools

import (
	"testing"

	"github.com/jonathanung/strike-cli/internal/artifact"
	"github.com/jonathanung/strike-cli/internal/ledger"
	"github.com/jonathanung/strike-cli/internal/tool"
)

func TestProductToolWireNames(t *testing.T) {
	t.Parallel()
	art, err := artifact.Open(t.TempDir(), "names")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = art.Close() })
	led, err := ledger.Open(t.TempDir(), "names")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = led.Close() })
	want := map[string]tool.Tool{
		"memory_write":    NewMemoryWrite(openMemory(t)),
		"memory_read":     NewMemoryRead(openMemory(t)),
		"issue_write":     NewIssueWrite(openIssue(t)),
		"issue_read":      NewIssueRead(openIssue(t)),
		"plan_write":      NewPlanWrite(openPlan(t)),
		"plan_read":       NewPlanRead(openPlan(t)),
		"plan_delegate":   NewPlanDelegate(openPlan(t)),
		"artifact_write":  NewArtifactWrite(art),
		"artifact_read":   NewArtifactRead(art),
		"ledger_write":    NewLedgerWrite(led),
		"ledger_read":     NewLedgerRead(led),
		"notebook_edit":   NewNotebookEdit(),
		"skill":           NewSkill(nil),
		"enter_plan_mode": NewEnterPlanMode(),
		"exit_plan_mode":  NewExitPlanMode(),
		"phase_done":      NewPhaseDone(),
		"tui_snapshot":    NewTUISnapshot(),
		"context_bundle":  NewContextBundle(),
		"definition":      NewDefinition(nil),
		"references":      NewReferences(nil),
		"symbols":         NewSymbols(nil),
		"diagnostics":     NewDiagnostics(nil),
		"call_hierarchy":  NewCallHierarchy(nil),
		"rename_preview":  NewRenamePreview(nil),
		"impact":          NewImpact(nil),
	}
	for name, tl := range want {
		if tl.Name() != name {
			t.Errorf("Name() = %q, want %q", tl.Name(), name)
		}
		if len(tl.Schema()) == 0 || tl.Description() == "" {
			t.Errorf("%s missing schema/description", name)
		}
	}
}

func TestNotebookEditContract(t *testing.T) {
	t.Parallel()
	c := tool.LookupContract(NewNotebookEdit())
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if c.SideEffect != tool.SideEffectWorkspaceMutative || c.Idempotency != tool.IdempotencyConditional {
		t.Fatalf("notebook_edit contract = %+v", c)
	}
}
