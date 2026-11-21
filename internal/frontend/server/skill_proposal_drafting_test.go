package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jonathanung/strike/internal/product/skillpropose"
)

// draftSideChat stands in for the operator's provider.
type draftSideChat struct {
	mu    sync.Mutex
	calls int
	text  string
	model string
	err   error
}

func (m *draftSideChat) Answer(_ context.Context, in SideChatPrompt) (SideChatCompletion, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if m.err != nil {
		return SideChatCompletion{}, m.err
	}
	return SideChatCompletion{Text: m.text, Provider: "anthropic", Model: m.model}, nil
}

func (m *draftSideChat) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

const draftReply = `{"description":"Use when cutting a release.","body":"## When to use\n\nCutting a release.\n\n## Steps\n\n1. Bump the version.\n\n## Request\n\n$ARGUMENTS"}`

func TestSkillProposalModelDrafting(t *testing.T) {
	tests := []struct {
		name       string
		model      *draftSideChat
		enable     bool
		modelDraft string
		wantCode   int // PATCH modelDraft
		wantCalls  int
		wantModel  string
	}{
		{name: "drafts through the provider when both opt-ins are on", model: &draftSideChat{text: draftReply, model: "claude-test"}, enable: true, modelDraft: `{"modelDraft":true}`, wantCode: http.StatusOK, wantCalls: 1, wantModel: "claude-test"},
		{name: "falls back to provider name when the model is unnamed", model: &draftSideChat{text: draftReply}, enable: true, modelDraft: `{"modelDraft":true}`, wantCode: http.StatusOK, wantCalls: 1, wantModel: "anthropic"},
		{name: "keeps the local draft when the model fails", model: &draftSideChat{err: errors.New("provider down")}, enable: true, modelDraft: `{"modelDraft":true}`, wantCode: http.StatusOK, wantCalls: 1},
		{name: "keeps the local draft on an unusable reply", model: &draftSideChat{text: "You said: hi", model: "echo"}, enable: true, modelDraft: `{"modelDraft":true}`, wantCode: http.StatusOK, wantCalls: 1},
		{name: "no model call while model drafting is off", model: &draftSideChat{text: draftReply, model: "claude-test"}, enable: true, wantCalls: 0},
		{name: "refuses model drafting with no provider", enable: true, modelDraft: `{"modelDraft":true}`, wantCode: http.StatusConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newProposalsFixture(t, false)
			if tt.model != nil {
				f.srv.opts.SideChat = tt.model
			}
			_, got, _ := f.call(t, http.MethodGet, "/v1/skill-proposals", "")
			if got.ModelDraft || got.ModelDraftAvailable != (tt.model != nil) {
				t.Fatalf("defaults: modelDraft=%v available=%v", got.ModelDraft, got.ModelDraftAvailable)
			}
			f.call(t, http.MethodPatch, "/v1/skill-proposals", `{"enabled":true}`)
			if tt.modelDraft != "" {
				code, got, raw := f.call(t, http.MethodPatch, "/v1/skill-proposals", tt.modelDraft)
				if code != tt.wantCode {
					t.Fatalf("PATCH modelDraft = %d %s", code, raw)
				}
				if code == http.StatusOK && !got.ModelDraft {
					t.Fatalf("modelDraft not stored: %s", raw)
				}
			}
			code, got, raw := f.call(t, http.MethodPost, "/v1/skill-proposals/scan", "")
			if code != http.StatusOK || len(got.Pending) != 1 {
				t.Fatalf("scan = %d %s", code, raw)
			}
			calls := 0
			if tt.model != nil {
				calls = tt.model.count()
			}
			if calls != tt.wantCalls {
				t.Fatalf("model calls = %d, want %d", calls, tt.wantCalls)
			}
			p := got.Pending[0]
			if p.Model != tt.wantModel {
				t.Fatalf("model = %q, want %q (%s)", p.Model, tt.wantModel, raw)
			}
			if tt.wantModel != "" {
				if !strings.HasPrefix(p.Body, "Drafted by "+tt.wantModel+" from 3 sessions") || p.Description != "Use when cutting a release." {
					t.Fatalf("model draft not applied: %+v", p)
				}
				// A rescan keeps the model draft without another call.
				f.call(t, http.MethodPost, "/v1/skill-proposals/scan", "")
				if n := tt.model.count(); n != tt.wantCalls {
					t.Fatalf("rescan called the model again: %d", n)
				}
			} else if p.DraftedBy != "local pattern match (no model call)" {
				t.Fatalf("local draft lost: %+v", p)
			}
		})
	}
}

func TestSkillProposalModelDraftingNeverRunsWhileOptInOff(t *testing.T) {
	f := newProposalsFixture(t, false)
	model := &draftSideChat{text: draftReply, model: "claude-test"}
	f.srv.opts.SideChat = model
	if code, got, raw := f.call(t, http.MethodPatch, "/v1/skill-proposals", `{"modelDraft":true}`); code != http.StatusOK || !got.ModelDraft || got.Enabled {
		t.Fatalf("PATCH = %d %s", code, raw)
	}
	if code, _, raw := f.call(t, http.MethodPost, "/v1/skill-proposals/scan", ""); code != http.StatusConflict {
		t.Fatalf("scan while off = %d %s", code, raw)
	}
	if err := proposalStore().SetDir(f.key, f.work); err != nil {
		t.Fatal(err)
	}
	if n := f.srv.scanDueSkillProposals(time.Now()); n != 0 {
		t.Fatalf("cadence scanned %d with the opt-in off", n)
	}
	ws := skillpropose.Workspace{ModelDraft: true, Pending: []skillpropose.Proposal{{ID: "x", Name: "x-workflow", Count: 3}}}
	f.srv.modelDraftProposals(context.Background(), f.key, ws)
	if n := model.count(); n != 0 {
		t.Fatalf("model called %d times with the opt-in off", n)
	}
}

func TestSkillProposalCadenceModelDrafts(t *testing.T) {
	f := newProposalsFixture(t, false)
	model := &draftSideChat{text: draftReply, model: "claude-test"}
	f.srv.opts.SideChat = model
	f.call(t, http.MethodPatch, "/v1/skill-proposals", `{"enabled":true,"modelDraft":true}`)
	if n := f.srv.scanDueSkillProposals(time.Now()); n != 1 {
		t.Fatalf("cadence scanned %d", n)
	}
	ws, err := proposalStore().Get(f.key)
	if err != nil || len(ws.Pending) != 1 || ws.Pending[0].Model != "claude-test" || model.count() != 1 {
		t.Fatalf("cadence draft = %+v calls=%d err=%v", ws.Pending, model.count(), err)
	}
}
