package enginebind

import (
	"errors"
	"testing"

	"github.com/jonathanung/strike-cli/harness/engine"
	"github.com/jonathanung/strike-cli/internal/persist/artifact"
	"github.com/jonathanung/strike-cli/internal/persist/ledger"
	"github.com/jonathanung/strike-cli/internal/persist/memory"
	"github.com/jonathanung/strike-cli/internal/persist/plan"
	"github.com/jonathanung/strike-cli/internal/product/config"
	"github.com/jonathanung/strike-cli/internal/product/project"
)

func TestMemoryAutoLoad(t *testing.T) {
	store, err := memory.Open(t.TempDir(), "p")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Put("k", "pref-value", []string{memory.TagPreference}); err != nil {
		t.Fatal(err)
	}
	text, omitted, err := Memory(store).AutoLoad()
	if err != nil || omitted != 0 {
		t.Fatalf("autoload: text=%q omitted=%d err=%v", text, omitted, err)
	}
	if text == "" || !contains(text, "pref-value") {
		t.Fatalf("text=%q", text)
	}
}

func TestLedgerActiveSliceFreshness(t *testing.T) {
	store, err := ledger.Open(t.TempDir(), "p")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.Append(ledger.AppendInput{
		Kind:          ledger.KindDecision,
		Statement:     "ship it",
		AuthorSession: "s",
	}); err != nil {
		t.Fatal(err)
	}
	src := Ledger(store)
	text, _, err := src.AutoLoad("")
	if err != nil || !contains(text, "ship it") {
		t.Fatalf("autoload=%q err=%v", text, err)
	}
	entries, err := src.ActiveSlice("", "", "")
	if err != nil || len(entries) != 1 || entries[0].Statement != "ship it" {
		t.Fatalf("slice=%#v err=%v", entries, err)
	}
	if entries[0].Freshness != "fresh" {
		t.Fatalf("freshness=%q", entries[0].Freshness)
	}
}

func TestPlanAdapter(t *testing.T) {
	store, err := plan.Open(t.TempDir(), "p")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	p, err := store.Create("root", "Ship", []plan.SectionInput{{Title: "A", Body: "b"}})
	if err != nil {
		t.Fatal(err)
	}
	src := Plan(store)
	got, ok, err := src.Get(p.ID)
	if err != nil || !ok || got.Title != "Ship" || len(got.Sections) != 1 {
		t.Fatalf("get=%#v ok=%v err=%v", got, ok, err)
	}
	approved, err := src.SetStatus(p.ID, "root", engine.PlanStatusApproved, got.Version)
	if err != nil || approved.Status != engine.PlanStatusApproved {
		t.Fatalf("approve=%#v err=%v", approved, err)
	}
	_, err = src.SetStatus(p.ID, "other", engine.PlanStatusClosed, approved.Version)
	if !errors.Is(err, engine.ErrPlanNotOwner) {
		t.Fatalf("want not-owner, got %v", err)
	}
}

func TestProjectors(t *testing.T) {
	a, ok := ProjectArtifact(artifact.Artifact{ID: "a1", Type: "findings", Version: 2, Title: "t"})
	if !ok || a.ID != "a1" || a.Type != "findings" || a.Version != 2 {
		t.Fatalf("artifact=%#v ok=%v", a, ok)
	}
	if _, ok := ProjectArtifact("nope"); ok {
		t.Fatal("expected reject")
	}
	l, ok := ProjectLedger(ledger.Entry{ID: "l1", Kind: ledger.KindDecision, Status: ledger.StatusActive})
	if !ok || l.ID != "l1" || l.Kind != ledger.KindDecision {
		t.Fatalf("ledger=%#v ok=%v", l, ok)
	}
}

func TestWorkflowConvert(t *testing.T) {
	in := config.BuiltinPlanImplement()
	got := Workflow(in)
	if got.Name != in.Name || len(got.Phases) != len(in.Phases) {
		t.Fatalf("got=%#v", got)
	}
	if got.Phases[0].Agent != "plan" || got.Phases[0].Exit.Type != engine.GateUser {
		t.Fatalf("phase0=%#v", got.Phases[0])
	}
	if got.Source != engine.WorkflowSourceBuiltin {
		t.Fatalf("source=%q", got.Source)
	}
}

func TestWorktreeNotGit(t *testing.T) {
	_, err := Worktrees().Add(t.Context(), t.TempDir(), "child")
	if !errors.Is(err, engine.ErrNotGitRepository) {
		t.Fatalf("want not-git, got %v", err)
	}
	_ = project.ErrNotGitRepository
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		(func() bool {
			for i := 0; i+len(sub) <= len(s); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		})())
}
