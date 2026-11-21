package skillpropose

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func localProposal() Proposal {
	return Proposal{
		ID:          "pat1",
		Name:        "bump-version-update",
		Description: "Repeated workflow: Bump the version and update the changelog",
		Body:        "Drafted from 3 similar sessions.\n\n## Steps\n\n1. Use the `bash` tool.\n\n## Request\n\n$ARGUMENTS",
		Tools:       []string{"bash", "edit"},
		Count:       3,
		Sessions:    []Evidence{{SessionID: "a", Title: "Release 1"}, {SessionID: "b", Title: "Release 2"}, {SessionID: "c"}},
		DraftedBy:   "local pattern match (no model call)",
	}
}

func fixedModel(text, model string, err error) DraftModel {
	return func(context.Context, string, string) (string, string, error) { return text, model, err }
}

func TestModelDraft(t *testing.T) {
	good := `{"description":"Use when cutting a release.","body":"## When to use\n\nCutting a release.\n\n## Steps\n\n1. Bump the version.\n\n## Request\n\n$ARGUMENTS"}`
	tests := []struct {
		name      string
		model     DraftModel
		wantErr   bool
		wantDesc  string
		bodyHas   []string
		bodyLacks []string
	}{
		{
			name:     "plain json reply",
			model:    fixedModel(good, "claude-test", nil),
			wantDesc: "Use when cutting a release.",
			bodyHas:  []string{"Drafted by claude-test from 3 sessions", "1. Bump the version.", "$ARGUMENTS"},
		},
		{
			name:     "fenced reply with prose",
			model:    fixedModel("Here you go:\n```json\n"+good+"\n```\n", "claude-test", nil),
			wantDesc: "Use when cutting a release.",
			bodyHas:  []string{"1. Bump the version."},
		},
		{
			name:     "missing placeholder is appended",
			model:    fixedModel(`{"description":"Release.","body":"## Steps\n\n1. Tag it."}`, "m", nil),
			wantDesc: "Release.",
			bodyHas:  []string{"1. Tag it.", "## Request\n\n$ARGUMENTS"},
		},
		{
			name:      "secret in reply is redacted",
			model:     fixedModel(`{"description":"Deploy with `+secret+`","body":"Run with token `+secret+`"}`, "m", nil),
			bodyLacks: []string{secret},
		},
		{
			name:     "multi-line description is folded",
			model:    fixedModel(`{"description":"Use when\n  releasing.","body":"Steps"}`, "m", nil),
			wantDesc: "Use when releasing.",
		},
		{name: "not json", model: fixedModel("You said: hello", "echo", nil), wantErr: true},
		{name: "empty body", model: fixedModel(`{"description":"x","body":"  "}`, "m", nil), wantErr: true},
		{name: "empty description", model: fixedModel(`{"description":"","body":"x"}`, "m", nil), wantErr: true},
		{name: "model error", model: fixedModel("", "", errors.New("provider down")), wantErr: true},
		{name: "unnamed model", model: fixedModel(good, " ", nil), wantErr: true},
		{name: "no model", model: nil, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := localProposal()
			got, err := ModelDraft(context.Background(), in, tt.model)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("want error, got %+v", got)
				}
				if got.Body != in.Body || got.Model != "" || got.DraftedBy != in.DraftedBy {
					t.Fatalf("failed draft changed the proposal: %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.ID != in.ID || got.Name != in.Name || got.Count != in.Count {
				t.Fatalf("identity changed: %+v", got)
			}
			if got.Model == "" || got.DraftedBy != got.Model {
				t.Fatalf("model not disclosed: model=%q draftedBy=%q", got.Model, got.DraftedBy)
			}
			if tt.wantDesc != "" && got.Description != tt.wantDesc {
				t.Fatalf("description = %q, want %q", got.Description, tt.wantDesc)
			}
			for _, s := range tt.bodyHas {
				if !strings.Contains(got.Body, s) {
					t.Fatalf("body lacks %q:\n%s", s, got.Body)
				}
			}
			for _, s := range append(tt.bodyLacks, secret) {
				if strings.Contains(got.Body, s) || strings.Contains(got.Description, s) {
					t.Fatalf("draft contains %q", s)
				}
			}
			if !strings.Contains(got.Body, "$ARGUMENTS") {
				t.Fatalf("body has no $ARGUMENTS:\n%s", got.Body)
			}
		})
	}
}

func TestModelDraftPromptIsRedactedAndBounded(t *testing.T) {
	p := localProposal()
	p.Sessions = append(p.Sessions, Evidence{SessionID: "d", Title: "token " + secret})
	for i := 0; i < 20; i++ {
		p.Sessions = append(p.Sessions, Evidence{SessionID: "x", Title: "extra"})
	}
	var gotSystem, gotPrompt string
	model := func(_ context.Context, system, prompt string) (string, string, error) {
		gotSystem, gotPrompt = system, prompt
		return "", "", errors.New("stop")
	}
	_, _ = ModelDraft(context.Background(), p, model)
	if !strings.Contains(gotSystem, "JSON") {
		t.Fatalf("system prompt = %q", gotSystem)
	}
	if strings.Contains(gotPrompt, secret) {
		t.Fatal("secret reached the model prompt")
	}
	if !strings.Contains(gotPrompt, "Tools run in order: bash, edit") || !strings.Contains(gotPrompt, "Repeated in 3 sessions") {
		t.Fatalf("prompt = %q", gotPrompt)
	}
	if n := strings.Count(gotPrompt, "- extra"); n > 10 {
		t.Fatalf("prompt lists %d titles", n)
	}
}

func TestModelDraftClipsLongBodyAndKeepsPlaceholder(t *testing.T) {
	long := strings.Repeat("word ", 3000) + "$ARGUMENTS"
	got, err := ModelDraft(context.Background(), localProposal(), fixedModel(`{"description":"d","body":"`+long+`"}`, "m", nil))
	if err != nil {
		t.Fatal(err)
	}
	if n := len([]rune(got.Body)); n > maxModelBodyRunes+40 {
		t.Fatalf("body runes = %d", n)
	}
	if !strings.HasSuffix(got.Body, "$ARGUMENTS") {
		t.Fatal("clipped body lost $ARGUMENTS")
	}
}

func TestStoreModelDraftGates(t *testing.T) {
	wfs, err := ReadWorkflows(plantedHistory(t), "ws", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	on, off := true, false
	tests := []struct {
		name       string
		enabled    bool
		modelDraft bool
		mutate     func(*Proposal)
		wantErr    error
	}{
		{name: "applies while scan and model drafting are on", enabled: true, modelDraft: true},
		{name: "refused while model drafting is off", enabled: true, modelDraft: false, wantErr: ErrDisabled},
		{name: "refused while the scan is off", enabled: false, modelDraft: true, wantErr: ErrDisabled},
		{name: "refused for a proposal no longer pending", enabled: true, modelDraft: true, mutate: func(p *Proposal) { p.ID = "gone" }, wantErr: ErrNotFound},
		{name: "refused when the group changed size", enabled: true, modelDraft: true, mutate: func(p *Proposal) { p.Count++ }, wantErr: ErrNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := NewStore(filepath.Join(t.TempDir(), "skill-proposals.json"))
			if _, err := st.Settings("ws", &on, nil); err != nil {
				t.Fatal(err)
			}
			ws, err := st.Scan("ws", wfs, nil, day(10))
			if err != nil || len(ws.Pending) != 1 {
				t.Fatalf("scan = %+v %v", ws, err)
			}
			if _, err := st.SetModelDraft("ws", tt.modelDraft); err != nil {
				t.Fatal(err)
			}
			if !tt.enabled {
				if _, err := st.Settings("ws", &off, nil); err != nil {
					t.Fatal(err)
				}
			}
			drafted, err := ModelDraft(context.Background(), ws.Pending[0], fixedModel(`{"description":"d","body":"b"}`, "m", nil))
			if err != nil {
				t.Fatal(err)
			}
			if tt.mutate != nil {
				tt.mutate(&drafted)
			}
			_, err = st.ApplyModelDraft("ws", drafted)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("ApplyModelDraft err = %v, want %v", err, tt.wantErr)
			}
			got, _ := st.Get("ws")
			applied := got.Pending[0].Model != ""
			if applied != (tt.wantErr == nil) {
				t.Fatalf("applied = %v, pending = %+v", applied, got.Pending[0])
			}
		})
	}
}

func TestScanKeepsModelDraftOnlyWhileOn(t *testing.T) {
	wfs, err := ReadWorkflows(plantedHistory(t), "ws", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	on := true
	st := NewStore(filepath.Join(t.TempDir(), "skill-proposals.json"))
	if _, err := st.Settings("ws", &on, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetModelDraft("ws", true); err != nil {
		t.Fatal(err)
	}
	ws, _ := st.Scan("ws", wfs, nil, day(10))
	drafted, err := ModelDraft(context.Background(), ws.Pending[0], fixedModel(`{"description":"d","body":"model body"}`, "m", nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ApplyModelDraft("ws", drafted); err != nil {
		t.Fatal(err)
	}
	ws, _ = st.Scan("ws", wfs, nil, day(11))
	if ws.Pending[0].Model != "m" || !strings.Contains(ws.Pending[0].Body, "model body") {
		t.Fatalf("rescan dropped the model draft: %+v", ws.Pending[0])
	}
	if _, err := st.SetModelDraft("ws", false); err != nil {
		t.Fatal(err)
	}
	ws, _ = st.Scan("ws", wfs, nil, day(12))
	if ws.Pending[0].Model != "" || strings.Contains(ws.Pending[0].Body, "model body") {
		t.Fatalf("rescan with model drafting off kept the model draft: %+v", ws.Pending[0])
	}
}

func TestModelDraftClipsLongModelName(t *testing.T) {
	reply := `{"description":"d","body":"b"}`
	got, err := ModelDraft(context.Background(), localProposal(), fixedModel(reply, strings.Repeat("big model ", 20), nil))
	if err != nil {
		t.Fatal(err)
	}
	if n := len([]rune(got.Model)); n != maxModelNameRunes {
		t.Fatalf("model name runes = %d, want %d", n, maxModelNameRunes)
	}
	if !strings.HasPrefix(got.Body, "Drafted by "+got.Model+" from 3 sessions") {
		t.Fatalf("body = %q", got.Body)
	}
}
