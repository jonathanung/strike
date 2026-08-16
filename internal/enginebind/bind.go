// Package enginebind adapts Strike product stores onto engine kernel seams.
// cmd/strike is the composition root; engine tests may use these adapters.
package enginebind

import (
	"context"
	"errors"

	"github.com/jonathanung/strike-cli/harness/engine"
	"github.com/jonathanung/strike-cli/internal/persist/artifact"
	"github.com/jonathanung/strike-cli/internal/persist/attachment"
	"github.com/jonathanung/strike-cli/internal/persist/ledger"
	"github.com/jonathanung/strike-cli/internal/persist/memory"
	"github.com/jonathanung/strike-cli/internal/persist/plan"
	"github.com/jonathanung/strike-cli/internal/product/config"
	"github.com/jonathanung/strike-cli/internal/product/project"
)

// Memory adapts *memory.Store to engine.MemorySource.
func Memory(s *memory.Store) engine.MemorySource {
	if s == nil {
		return nil
	}
	return memoryBind{s}
}

type memoryBind struct{ *memory.Store }

func (m memoryBind) AutoLoad() (string, int, error) {
	return memory.AutoLoadLayer(m.Store)
}

// Ledger adapts *ledger.Store to engine.LedgerSource.
func Ledger(s *ledger.Store) engine.LedgerSource {
	if s == nil {
		return nil
	}
	return ledgerBind{s}
}

type ledgerBind struct{ *ledger.Store }

func (l ledgerBind) AutoLoad(workDir string) (string, int, error) {
	return ledger.AutoLoadLayer(l.Store, "", "", workDir)
}

func (l ledgerBind) ActiveSlice(path, taskID, workDir string) ([]engine.LedgerEntry, error) {
	entries, err := l.Store.ActiveSlice(path, taskID)
	if err != nil {
		return nil, err
	}
	out := make([]engine.LedgerEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, ledgerEntry(e, workDir))
	}
	return out, nil
}

func ledgerEntry(e ledger.Entry, workDir string) engine.LedgerEntry {
	fresh := "fresh"
	if ledger.AssessFreshness(e, workDir).State == ledger.FreshStale {
		fresh = "stale"
	}
	return engine.LedgerEntry{
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
	}
}

// Plan adapts *plan.Store to engine.PlanStore.
func Plan(s *plan.Store) engine.PlanStore {
	if s == nil {
		return nil
	}
	return planBind{s}
}

type planBind struct{ *plan.Store }

func (p planBind) Get(id string) (engine.PlanView, bool, error) {
	got, ok, err := p.Store.Get(id)
	if err != nil || !ok {
		return engine.PlanView{}, ok, mapPlanErr(err)
	}
	return planView(got), true, nil
}

func (p planBind) SetStatus(id, actorRoot, status string, expectedVersion int) (engine.PlanView, error) {
	got, err := p.Store.SetStatus(id, actorRoot, status, expectedVersion)
	if err != nil {
		return engine.PlanView{}, mapPlanErr(err)
	}
	return planView(got), nil
}

func (p planBind) FinishSectionDelegate(id, actorRoot, sectionID, childID string, outcome engine.DelegateOutcome) (engine.PlanView, error) {
	got, err := p.Store.FinishSectionDelegate(id, actorRoot, sectionID, childID, plan.DelegateOutcome{
		Status: outcome.Status,
		Title:  outcome.Title,
		Body:   outcome.Body,
		Detail: outcome.Detail,
	})
	if err != nil {
		return engine.PlanView{}, mapPlanErr(err)
	}
	return planView(got), nil
}

func planView(p plan.Plan) engine.PlanView {
	out := engine.PlanView{
		ID:        p.ID,
		OwnerRoot: p.OwnerRoot,
		Status:    p.Status,
		Title:     p.Title,
		Version:   p.Version,
	}
	if len(p.Sections) > 0 {
		out.Sections = make([]engine.PlanSectionView, len(p.Sections))
		for i, s := range p.Sections {
			out.Sections[i] = engine.PlanSectionView{ID: s.ID, Title: s.Title, Body: s.Body}
		}
	}
	return out
}

func mapPlanErr(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, plan.ErrNotOwner):
		return engine.ErrPlanNotOwner
	case errors.Is(err, plan.ErrConflict):
		return engine.ErrPlanConflict
	default:
		return err
	}
}

// Attachments adapts *attachment.Store to engine.AttachmentStore.
func Attachments(s *attachment.Store) engine.AttachmentStore {
	if s == nil {
		return nil
	}
	return attachmentBind{s}
}

type attachmentBind struct{ *attachment.Store }

func (a attachmentBind) Put(raw []byte, in engine.AttachmentPut) (engine.AttachmentMeta, error) {
	meta, err := a.Store.Put(raw, attachment.PutInput{
		MIME:      in.MIME,
		Name:      in.Name,
		Kind:      in.Kind,
		SessionID: in.SessionID,
	})
	if err != nil {
		return engine.AttachmentMeta{}, err
	}
	return engine.AttachmentMeta{
		SHA256: meta.SHA256,
		MIME:   meta.MIME,
		Kind:   meta.Kind,
		Name:   meta.Name,
		Bytes:  meta.Bytes,
	}, nil
}

func (a attachmentBind) Get(ref string) ([]byte, engine.AttachmentMeta, error) {
	raw, meta, err := a.Store.Get(ref)
	if err != nil {
		return nil, engine.AttachmentMeta{}, err
	}
	return raw, engine.AttachmentMeta{
		SHA256: meta.SHA256,
		MIME:   meta.MIME,
		Kind:   meta.Kind,
		Name:   meta.Name,
		Bytes:  meta.Bytes,
	}, nil
}

// Worktrees returns the product git worktree binder.
func Worktrees() engine.WorktreeBinder {
	return worktreeBind{}
}

type worktreeBind struct{}

func (worktreeBind) Add(ctx context.Context, base, childID string) (engine.Worktree, error) {
	wt, err := project.Add(ctx, base, childID)
	if err != nil {
		if errors.Is(err, project.ErrNotGitRepository) {
			return engine.Worktree{}, engine.ErrNotGitRepository
		}
		return engine.Worktree{}, err
	}
	return engine.Worktree{Path: wt.Path, Branch: wt.Branch, RepoRoot: wt.RepoRoot}, nil
}

func (worktreeBind) HeadRev(ctx context.Context, path string) string {
	return project.HeadRev(ctx, path)
}

func (worktreeBind) DiffUnified(ctx context.Context, path string) (string, error) {
	return project.DiffUnified(ctx, path)
}

func (worktreeBind) Remove(ctx context.Context, repo, path, branch string) error {
	return project.Remove(ctx, repo, path, branch)
}

// ProjectArtifact maps artifact.Artifact payloads onto engine.ArtifactNotice.
func ProjectArtifact(payload any) (engine.ArtifactNotice, bool) {
	a, ok := payload.(artifact.Artifact)
	if !ok {
		return engine.ArtifactNotice{}, false
	}
	return engine.ArtifactNotice{
		ID:        a.ID,
		Type:      a.Type,
		Version:   a.Version,
		Scope:     a.Scope,
		Title:     a.Title,
		SessionID: a.SessionID,
	}, true
}

// ProjectLedger maps ledger.Entry payloads onto engine.LedgerNotice.
func ProjectLedger(payload any) (engine.LedgerNotice, bool) {
	e, ok := payload.(ledger.Entry)
	if !ok {
		return engine.LedgerNotice{}, false
	}
	return engine.LedgerNotice{
		ID:            e.ID,
		Kind:          e.Kind,
		Status:        e.Status,
		Statement:     e.Statement,
		Reason:        e.InvalidateReason,
		Supersedes:    e.Supersedes,
		SupersededBy:  e.SupersededBy,
		AuthorSession: e.AuthorSession,
	}, true
}

// Workflows converts product config workflows into engine kernel workflows.
func Workflows(in []config.Workflow) []engine.Workflow {
	if in == nil {
		return nil
	}
	out := make([]engine.Workflow, len(in))
	for i, w := range in {
		out[i] = Workflow(w)
	}
	return out
}

// Workflow converts one product config workflow.
func Workflow(w config.Workflow) engine.Workflow {
	out := engine.Workflow{
		SchemaVersion: w.SchemaVersion,
		Name:          w.Name,
		Description:   w.Description,
		Source:        engine.WorkflowSource(w.Source),
		Path:          w.Path,
		Fingerprint:   w.Fingerprint,
	}
	if len(w.Phases) > 0 {
		out.Phases = make([]engine.Phase, len(w.Phases))
		for i, p := range w.Phases {
			out.Phases[i] = engine.Phase{
				Name:        p.Name,
				Description: p.Description,
				Agent:       p.Agent,
				Context:     p.Context,
				Permissions: p.Permissions,
				Exit: engine.ExitGate{
					Type:    engine.GateType(p.Exit.Type),
					Command: p.Exit.Command,
				},
			}
		}
	}
	return out
}
