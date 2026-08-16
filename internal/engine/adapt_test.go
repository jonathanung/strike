package engine

import (
	"github.com/jonathanung/strike-cli/internal/ledger"
	"github.com/jonathanung/strike-cli/internal/memory"
	"github.com/jonathanung/strike-cli/internal/plan"
)

type memoryAdapt struct{ *memory.Store }

func (m memoryAdapt) AutoLoad() (string, int, error) {
	return memory.AutoLoadLayer(m.Store)
}

func adaptMemory(s *memory.Store) MemorySource {
	if s == nil {
		return nil
	}
	return memoryAdapt{s}
}

type ledgerAdapt struct{ *ledger.Store }

func (l ledgerAdapt) AutoLoad(workDir string) (string, int, error) {
	return ledger.AutoLoadLayer(l.Store, "", "", workDir)
}

func (l ledgerAdapt) ActiveSlice(path, taskID, workDir string) ([]LedgerEntry, error) {
	entries, err := l.Store.ActiveSlice(path, taskID)
	if err != nil {
		return nil, err
	}
	out := make([]LedgerEntry, 0, len(entries))
	for _, e := range entries {
		fresh := "fresh"
		if ledger.AssessFreshness(e, workDir).State == ledger.FreshStale {
			fresh = "stale"
		}
		out = append(out, LedgerEntry{
			ID:            e.ID,
			Kind:          e.Kind,
			Status:        e.Status,
			Statement:     e.Statement,
			Confidence:    e.Confidence,
			EvidenceRefs:  append([]string(nil), e.EvidenceRefs...),
			ScopePaths:    append([]string(nil), e.ScopePaths...),
			ScopeTaskIDs:  append([]string(nil), e.ScopeTaskIDs...),
			AuthorSession: e.AuthorSession,
			Reason:        e.InvalidateReason,
			Supersedes:    e.Supersedes,
			SupersededBy:  e.SupersededBy,
			Freshness:     fresh,
		})
	}
	return out, nil
}

func adaptLedger(s *ledger.Store) LedgerSource {
	if s == nil {
		return nil
	}
	return ledgerAdapt{s}
}

type planAdapt struct{ *plan.Store }

func (p planAdapt) Get(id string) (PlanView, bool, error) {
	got, ok, err := p.Store.Get(id)
	if err != nil || !ok {
		return PlanView{}, ok, err
	}
	return testPlanView(got), true, nil
}

func (p planAdapt) SetStatus(id, actorRoot, status string, expectedVersion int) (PlanView, error) {
	got, err := p.Store.SetStatus(id, actorRoot, status, expectedVersion)
	if err != nil {
		return PlanView{}, err
	}
	return testPlanView(got), nil
}

func (p planAdapt) FinishSectionDelegate(id, actorRoot, sectionID, childID string, outcome DelegateOutcome) (PlanView, error) {
	got, err := p.Store.FinishSectionDelegate(id, actorRoot, sectionID, childID, plan.DelegateOutcome{
		Status: outcome.Status,
		Title:  outcome.Title,
		Body:   outcome.Body,
		Detail: outcome.Detail,
	})
	if err != nil {
		return PlanView{}, err
	}
	return testPlanView(got), nil
}

func testPlanView(p plan.Plan) PlanView {
	out := PlanView{ID: p.ID, OwnerRoot: p.OwnerRoot, Status: p.Status, Title: p.Title, Version: p.Version}
	if len(p.Sections) > 0 {
		out.Sections = make([]PlanSectionView, len(p.Sections))
		for i, s := range p.Sections {
			out.Sections[i] = PlanSectionView{ID: s.ID, Title: s.Title, Body: s.Body}
		}
	}
	return out
}

func adaptPlan(s *plan.Store) PlanStore {
	if s == nil {
		return nil
	}
	return planAdapt{s}
}
