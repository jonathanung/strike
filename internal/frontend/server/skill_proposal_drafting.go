package server

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jonathanung/strike/internal/product/skillpropose"
)

// Model-assisted skill proposal drafting (#1958). The model call goes
// through the side-chat model: the operator's configured provider, called
// with no tools. It runs only for a workspace whose scan is on and whose
// model drafting is on, right after a scan, for at most
// skillpropose.MaxModelDrafts drafts. A failed call keeps the local draft.

const (
	modelDraftCallTimeout = 60 * time.Second
	modelDraftScanBudget  = 2 * time.Minute
)

// proposalDraftModel adapts the side-chat model, or returns nil when no
// provider is configured (attach-only serve).
func (s *Server) proposalDraftModel() skillpropose.DraftModel {
	if s == nil || s.opts.SideChat == nil {
		return nil
	}
	m := s.opts.SideChat
	return func(ctx context.Context, system, prompt string) (string, string, error) {
		out, err := m.Answer(ctx, SideChatPrompt{System: system, Question: prompt})
		if err != nil {
			return "", "", err
		}
		name := strings.TrimSpace(out.Model)
		if name == "" {
			name = strings.TrimSpace(out.Provider)
		}
		return out.Text, name, nil
	}
}

// modelDraftProposals rewrites ws's local drafts with the drafting model
// and returns the workspace as stored afterwards. It does nothing unless
// ws.ModelDrafting() and a model is configured.
func (s *Server) modelDraftProposals(ctx context.Context, key string, ws skillpropose.Workspace) skillpropose.Workspace {
	model := s.proposalDraftModel()
	if model == nil || !ws.ModelDrafting() || s.opts.ReadOnly {
		return ws
	}
	ctx, cancel := context.WithTimeout(ctx, modelDraftScanBudget)
	defer cancel()
	store := proposalStore()
	calls := 0
	for _, p := range ws.Pending {
		if p.Model != "" {
			continue
		}
		if calls == skillpropose.MaxModelDrafts || ctx.Err() != nil {
			break
		}
		calls++
		callCtx, callCancel := context.WithTimeout(ctx, modelDraftCallTimeout)
		drafted, err := skillpropose.ModelDraft(callCtx, p, model)
		callCancel()
		if err != nil {
			continue
		}
		next, err := store.ApplyModelDraft(key, drafted)
		if err != nil {
			// Turned off meanwhile: stop calling the model.
			if errors.Is(err, skillpropose.ErrDisabled) {
				break
			}
			continue
		}
		ws = next
	}
	return ws
}
