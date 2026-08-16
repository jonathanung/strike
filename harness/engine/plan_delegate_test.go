package engine

import (
	"testing"

	"github.com/jonathanung/strike-cli/pkg/protocol"
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

type recordPlan struct {
	calls []struct {
		id, actor, section, child string
		outcome                   DelegateOutcome
	}
	err error
}

func (r *recordPlan) Get(string) (PlanView, bool, error) { return PlanView{}, false, nil }
func (r *recordPlan) SetStatus(string, string, string, int) (PlanView, error) {
	return PlanView{}, nil
}
func (r *recordPlan) FinishSectionDelegate(id, actor, section, child string, outcome DelegateOutcome) (PlanView, error) {
	r.calls = append(r.calls, struct {
		id, actor, section, child string
		outcome                   DelegateOutcome
	}{id, actor, section, child, outcome})
	return PlanView{}, r.err
}

func TestApplyPlanSectionDelegateRecordsOutcome(t *testing.T) {
	store := &recordPlan{}
	eng := &Engine{opts: Options{SessionID: "lead-plan", PlanStore: store}}
	eng.applyPlanSectionDelegate(&childHandle{
		id: "c-a", planID: "p1", sectionID: "s1",
	}, protocol.ChildCompleted{
		Status: protocol.ChildStatusCompleted,
		Handoff: protocol.CompletionHandoff{
			Summary:        "done a",
			SectionBody:    "refined-a",
			SectionBodySet: true,
		},
	})
	if len(store.calls) != 1 {
		t.Fatalf("calls=%d", len(store.calls))
	}
	got := store.calls[0]
	if got.id != "p1" || got.actor != "lead-plan" || got.section != "s1" || got.child != "c-a" {
		t.Fatalf("call=%+v", got)
	}
	if got.outcome.Status != DelegateApplied || got.outcome.Body == nil || *got.outcome.Body != "refined-a" {
		t.Fatalf("outcome=%+v", got.outcome)
	}

	store.err = ErrPlanConflict
	eng.applyPlanSectionDelegate(&childHandle{
		id: "c-b", planID: "p1", sectionID: "s2",
	}, protocol.ChildCompleted{
		Status: protocol.ChildStatusCompleted,
		Handoff: protocol.CompletionHandoff{
			Summary:        "done b",
			SectionBody:    "child-b",
			SectionBodySet: true,
		},
	})
	if len(store.calls) != 2 {
		t.Fatalf("calls=%d after conflict", len(store.calls))
	}
}

func TestApplyPlanSectionDelegateIgnoresUncorrelated(t *testing.T) {
	store := &recordPlan{}
	eng := &Engine{opts: Options{SessionID: "lead", PlanStore: store}}
	eng.applyPlanSectionDelegate(&childHandle{id: "c1"}, protocol.ChildCompleted{
		Status: protocol.ChildStatusCompleted,
		Handoff: protocol.CompletionHandoff{
			SectionBody: "y", SectionBodySet: true,
		},
	})
	if len(store.calls) != 0 {
		t.Fatalf("uncorrelated apply called store: %+v", store.calls)
	}
}
