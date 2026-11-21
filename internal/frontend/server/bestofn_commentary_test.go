package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/jonathanung/strike/internal/daemon/bestofn"
	"github.com/jonathanung/strike/internal/protocol"
)

// Tokens/cost reach the outcome only when child.completed carries usage, and
// stay absent (nil, not zero) otherwise (#1962).
func TestBestOfNOutcomeUsage(t *testing.T) {
	cost := 0.0123
	tests := []struct {
		name   string
		usage  *protocol.ChildUsage
		tokens *int
		cost   *float64
	}{
		{name: "no usage reported"},
		{name: "tokens without a price", usage: &protocol.ChildUsage{Tokens: 900}, tokens: intPtr(900)},
		{name: "measured zero", usage: &protocol.ChildUsage{Tokens: 0}, tokens: intPtr(0)},
		{name: "tokens and cost", usage: &protocol.ChildUsage{Tokens: 1500, CostUSD: &cost}, tokens: intPtr(1500), cost: &cost},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := bestOfNOutcome(protocol.ChildCompleted{Status: protocol.ChildStatusCompleted, Usage: tt.usage})
			if (out.Tokens == nil) != (tt.tokens == nil) || (out.Tokens != nil && *out.Tokens != *tt.tokens) {
				t.Fatalf("tokens = %v, want %v", out.Tokens, tt.tokens)
			}
			if (out.CostUSD == nil) != (tt.cost == nil) || (out.CostUSD != nil && *out.CostUSD != *tt.cost) {
				t.Fatalf("cost = %v, want %v", out.CostUSD, tt.cost)
			}
		})
	}
}

func intPtr(v int) *int { return &v }

// commentaryServer starts a two-model run owned by a live session whose ops
// land on the returned channel.
func commentaryServer(t *testing.T) (*Server, chan protocol.Op, *bestOfNEntry) {
	t.Helper()
	ops := make(chan protocol.Op, 4)
	live := NewLive("root-a", t.TempDir(), nil, ops)
	t.Cleanup(live.Close)
	sp := &fakeBestOfNSpawner{}
	srv := mustServer(t, Options{
		SessionDir:     t.TempDir(),
		Live:           live,
		BestOfNSpawner: func(string) bestofn.Spawner { return sp },
	})
	code, dto, raw := bestOfNDo(t, srv, http.MethodPost, "/v1/bestofn",
		`{"ownerRoot":"root-a","prompt":"why does Close race?","candidates":[{"model":"a"},{"model":"b","effort":"high"}]}`)
	if code != http.StatusOK {
		t.Fatalf("start = %d: %s", code, raw)
	}
	e := srv.bestOfN.get(dto.ID)
	if e == nil {
		t.Fatal("run not registered")
	}
	return srv, ops, e
}

// commentaryTurns drains ops and returns the user turns among them.
func commentaryTurns(ops chan protocol.Op) []protocol.UserInput {
	var out []protocol.UserInput
	for {
		select {
		case op := <-ops:
			if in, ok := op.(protocol.UserInput); ok {
				out = append(out, in)
			}
		default:
			return out
		}
	}
}

func finishBestOfNCandidates(srv *Server, e *bestOfNEntry) {
	cost := 0.042
	srv.applyBestOfNChild(e, protocol.ChildCompleted{
		Correlation: protocol.Correlation{SessionID: "child-1"},
		Status:      protocol.ChildStatusCompleted,
		Handoff:     protocol.CompletionHandoff{Summary: "the race is in Close"},
		Usage:       &protocol.ChildUsage{Tokens: 1200, CostUSD: &cost},
	})
	srv.applyBestOfNChild(e, protocol.ChildCompleted{
		Correlation: protocol.Correlation{SessionID: "child-2"},
		Status:      protocol.ChildStatusFailed,
		Summary:     "provider error",
	})
}

func TestBestOfNCompareRequestsCommentaryOnce(t *testing.T) {
	srv, ops, e := commentaryServer(t)
	path := "/v1/bestofn/" + e.id + "/compare"

	if code, _, raw := bestOfNDo(t, srv, http.MethodPost, path, `{}`); code != http.StatusConflict || !strings.Contains(raw, "stopped") {
		t.Fatalf("compare while running = %d: %s", code, raw)
	}
	if got := commentaryTurns(ops); len(got) != 0 {
		t.Fatalf("compare while running submitted %d turn(s)", len(got))
	}

	finishBestOfNCandidates(srv, e)
	code, dto, raw := bestOfNDo(t, srv, http.MethodPost, path, `{}`)
	if code != http.StatusOK || !dto.Commentary {
		t.Fatalf("compare = %d: %s", code, raw)
	}
	// Commentary never decides anything.
	if dto.Decision != nil {
		t.Fatalf("compare decided the run: %+v", dto.Decision)
	}
	if got := dto.Candidates[0]; got.Tokens == nil || *got.Tokens != 1200 || got.CostUSD == nil || *got.CostUSD != 0.042 {
		t.Fatalf("candidate 1 usage = %v / %v", got.Tokens, got.CostUSD)
	}
	if got := dto.Candidates[1]; got.Tokens != nil || got.CostUSD != nil {
		t.Fatalf("candidate 2 usage = %v / %v, want absent", got.Tokens, got.CostUSD)
	}
	if strings.Contains(raw, `"tokens":0`) {
		t.Fatalf("unreported usage rendered as zero: %s", raw)
	}

	turns := commentaryTurns(ops)
	if len(turns) != 1 {
		t.Fatalf("compare submitted %d turn(s), want 1", len(turns))
	}
	in := turns[0]
	for _, want := range []string{
		"[best-of-N " + e.id + "]",
		"Do not choose, rank or recommend a winner",
		"why does Close race?",
		"1. a: done (session child-1)",
		"1200 tokens",
		"Summary: the race is in Close",
		"2. b (high): failed",
		"Error: failed: provider error",
	} {
		if !strings.Contains(in.Text, want) {
			t.Errorf("commentary turn missing %q:\n%s", want, in.Text)
		}
	}

	if code, _, raw := bestOfNDo(t, srv, http.MethodPost, path, `{}`); code != http.StatusConflict || !strings.Contains(raw, "already requested") {
		t.Fatalf("second compare = %d: %s", code, raw)
	}
	if got := commentaryTurns(ops); len(got) != 0 {
		t.Fatalf("second compare submitted %d more turn(s)", len(got))
	}
}

func TestBestOfNCompareRefusals(t *testing.T) {
	t.Run("decided run", func(t *testing.T) {
		srv, ops, e := commentaryServer(t)
		finishBestOfNCandidates(srv, e)
		if code, _, raw := bestOfNDo(t, srv, http.MethodPost, "/v1/bestofn/"+e.id+"/keep-none", `{}`); code != http.StatusOK {
			t.Fatalf("keep-none = %d: %s", code, raw)
		}
		if code, _, raw := bestOfNDo(t, srv, http.MethodPost, "/v1/bestofn/"+e.id+"/compare", `{}`); code != http.StatusConflict || !strings.Contains(raw, "decided") {
			t.Fatalf("compare after decision = %d: %s", code, raw)
		}
		if got := commentaryTurns(ops); len(got) != 0 {
			t.Fatalf("compare after decision submitted %d turn(s)", len(got))
		}
	})
	t.Run("foreign root", func(t *testing.T) {
		srv, _, e := commentaryServer(t)
		finishBestOfNCandidates(srv, e)
		if code, _, _ := bestOfNDo(t, srv, http.MethodPost, "/v1/bestofn/"+e.id+"/compare?root=other", `{}`); code != http.StatusForbidden {
			t.Fatalf("foreign compare = %d, want 403", code)
		}
	})
	t.Run("owner not live", func(t *testing.T) {
		sp := &fakeBestOfNSpawner{}
		srv := bestOfNServer(t, sp, false)
		code, dto, raw := bestOfNDo(t, srv, http.MethodPost, "/v1/bestofn",
			`{"ownerRoot":"root-z","prompt":"p","candidates":[{"model":"a"},{"model":"b"}]}`)
		if code != http.StatusOK {
			t.Fatalf("start = %d: %s", code, raw)
		}
		e := srv.bestOfN.get(dto.ID)
		finishBestOfNCandidates(srv, e)
		if code, _, raw := bestOfNDo(t, srv, http.MethodPost, "/v1/bestofn/"+e.id+"/compare", `{}`); code != http.StatusConflict || !strings.Contains(raw, "not live") {
			t.Fatalf("compare without live owner = %d: %s", code, raw)
		}
	})
}
