package engine

import (
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/plan"
	"github.com/jonathanung/strike-cli/internal/protocol"
)

func TestSectionDelegateOutcomeMapping(t *testing.T) {
	t.Run("applied", func(t *testing.T) {
		h := protocol.CompletionHandoff{
			Summary:        "ok",
			SectionBody:    "new-body",
			SectionBodySet: true,
			SectionTitle:   "New",
		}
		out := sectionDelegateOutcome(protocol.ChildCompleted{
			Status:  protocol.ChildStatusCompleted,
			Handoff: h,
			Summary: "ok",
		})
		if out.Status != DelegateApplied || out.Body == nil || *out.Body != "new-body" {
			t.Fatalf("out=%+v", out)
		}
		if out.Title == nil || *out.Title != "New" {
			t.Fatalf("title=%v", out.Title)
		}
	})
	t.Run("failed", func(t *testing.T) {
		out := sectionDelegateOutcome(protocol.ChildCompleted{
			Status:  protocol.ChildStatusFailed,
			Summary: "boom",
			Handoff: protocol.CompletionHandoff{Summary: "boom"},
		})
		if out.Status != DelegateFailed || out.Body != nil {
			t.Fatalf("out=%+v", out)
		}
	})
	t.Run("canceled", func(t *testing.T) {
		out := sectionDelegateOutcome(protocol.ChildCompleted{
			Status:  protocol.ChildStatusCanceled,
			Handoff: protocol.CompletionHandoff{},
		})
		if out.Status != DelegateCanceled {
			t.Fatalf("out=%+v", out)
		}
	})
	t.Run("malformed incomplete", func(t *testing.T) {
		out := sectionDelegateOutcome(protocol.ChildCompleted{
			Status: protocol.ChildStatusCompleted,
			Handoff: protocol.CompletionHandoff{
				Summary:    "prose only",
				Incomplete: true,
			},
		})
		if out.Status != DelegateMalformed {
			t.Fatalf("out=%+v", out)
		}
	})
	t.Run("malformed missing body", func(t *testing.T) {
		out := sectionDelegateOutcome(protocol.ChildCompleted{
			Status: protocol.ChildStatusCompleted,
			Handoff: protocol.CompletionHandoff{
				Summary:    "ok",
				Incomplete: false,
			},
		})
		if out.Status != DelegateMalformed {
			t.Fatalf("out=%+v", out)
		}
	})
	t.Run("parse from summary json", func(t *testing.T) {
		raw := `{"summary":"x","section_body":"from-json","findings":[],"blockers":[]}`
		h, ok := parseCompletionHandoff(raw)
		if !ok {
			t.Fatal("parse handoff")
		}
		if !h.SectionBodySet || h.SectionBody != "from-json" {
			t.Fatalf("handoff=%#v", h)
		}
		out := sectionDelegateOutcome(protocol.ChildCompleted{
			Status:  protocol.ChildStatusCompleted,
			Handoff: h,
		})
		if out.Status != DelegateApplied || out.Body == nil || *out.Body != "from-json" {
			t.Fatalf("out=%+v", out)
		}
	})
}

func TestApplyPlanSectionDelegateCAS(t *testing.T) {
	store, err := plan.Open(t.TempDir(), "eng-plan")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	p, err := store.Create("lead-plan", "Ship", []plan.SectionInput{
		{Title: "Research", Body: "original-a"},
		{Title: "Implement", Body: "original-b"},
	})
	if err != nil {
		t.Fatal(err)
	}

	eng := &Engine{
		opts: Options{
			SessionID: "lead-plan",
			PlanStore: adaptPlan(store),
		},
	}

	if _, err := store.BeginSectionDelegate(p.ID, "lead-plan", "s1", "c-a", "ra"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginSectionDelegate(p.ID, "lead-plan", "s2", "c-b", "rb"); err != nil {
		t.Fatal(err)
	}

	eng.applyPlanSectionDelegate(&childHandle{
		id: "c-a", planID: p.ID, sectionID: "s1",
	}, protocol.ChildCompleted{
		Status: protocol.ChildStatusCompleted,
		Handoff: protocol.CompletionHandoff{
			Summary:        "done a",
			SectionBody:    "refined-a",
			SectionBodySet: true,
		},
	})

	cur, ok, err := store.Get(p.ID)
	if err != nil || !ok {
		t.Fatal(err)
	}
	edited := "user-edit-b"
	if _, err := store.UpdateSection(p.ID, "lead-plan", "s2", nil, &edited, cur.Version); err != nil {
		t.Fatal(err)
	}
	eng.applyPlanSectionDelegate(&childHandle{
		id: "c-b", planID: p.ID, sectionID: "s2",
	}, protocol.ChildCompleted{
		Status: protocol.ChildStatusCompleted,
		Handoff: protocol.CompletionHandoff{
			Summary:        "done b",
			SectionBody:    "child-b",
			SectionBodySet: true,
		},
	})

	// Re-dispatch s1 after applied is allowed; failed preserves refined body.
	if _, err := store.BeginSectionDelegate(p.ID, "lead-plan", "s1", "c-fail", ""); err != nil {
		t.Fatal(err)
	}
	eng.applyPlanSectionDelegate(&childHandle{
		id: "c-fail", planID: p.ID, sectionID: "s1",
	}, protocol.ChildCompleted{
		Status:  protocol.ChildStatusFailed,
		Summary: "explode",
		Handoff: protocol.CompletionHandoff{Summary: "explode"},
	})

	got, ok, err := store.Get(p.ID)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if got.Sections[0].Body != "refined-a" || got.Sections[0].DelegateStatus != plan.DelegateFailed {
		t.Fatalf("s1=%#v", got.Sections[0])
	}
	if !strings.Contains(got.Sections[0].DelegateDetail, "explode") {
		t.Fatalf("s1 detail=%q", got.Sections[0].DelegateDetail)
	}
	if got.Sections[1].Body != "user-edit-b" || got.Sections[1].DelegateStatus != plan.DelegateConflict {
		t.Fatalf("s2=%#v", got.Sections[1])
	}
}

func TestApplyPlanSectionDelegateIgnoresUncorrelated(t *testing.T) {
	store, err := plan.Open(t.TempDir(), "uncorr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	p, err := store.Create("lead", "P", []plan.SectionInput{{Title: "A", Body: "b"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginSectionDelegate(p.ID, "lead", "s1", "c1", ""); err != nil {
		t.Fatal(err)
	}
	eng := &Engine{opts: Options{SessionID: "lead", PlanStore: adaptPlan(store)}}
	// Wrong child id — store rejects; section stays in_flight.
	eng.applyPlanSectionDelegate(&childHandle{
		id: "other", planID: p.ID, sectionID: "s1",
	}, protocol.ChildCompleted{
		Status: protocol.ChildStatusCompleted,
		Handoff: protocol.CompletionHandoff{
			SectionBody: "x", SectionBodySet: true,
		},
	})
	got, ok, err := store.Get(p.ID)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if got.Sections[0].DelegateStatus != plan.DelegateInFlight || got.Sections[0].Body != "b" {
		t.Fatalf("got=%#v", got.Sections[0])
	}
	// Empty plan correlation is a no-op.
	eng.applyPlanSectionDelegate(&childHandle{id: "c1"}, protocol.ChildCompleted{
		Status: protocol.ChildStatusCompleted,
		Handoff: protocol.CompletionHandoff{
			SectionBody: "y", SectionBodySet: true,
		},
	})
	got, _, _ = store.Get(p.ID)
	if got.Sections[0].Body != "b" {
		t.Fatalf("uncorrelated apply mutated body: %#v", got.Sections[0])
	}
}
