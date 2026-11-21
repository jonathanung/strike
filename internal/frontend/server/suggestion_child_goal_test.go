package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/jonathanung/strike/internal/frontend/host"
	"github.com/jonathanung/strike/internal/persist/suggestion"
	"github.com/jonathanung/strike/internal/protocol"
)

func childDone(status protocol.ChildStatus, name, next string, blockers ...string) protocol.ChildCompleted {
	return protocol.ChildCompleted{
		Correlation: protocol.Correlation{SessionID: "child-1", ParentSessionID: "root-1"},
		Status:      status,
		Name:        name,
		Handoff:     protocol.CompletionHandoff{RecommendedNextAction: next, Blockers: blockers},
	}
}

func TestChildFollowUpDraft(t *testing.T) {
	noParent := childDone(protocol.ChildStatusCompleted, "reviewer", "add a test")
	noParent.ParentSessionID = ""
	badIDs := childDone(protocol.ChildStatusCompleted, "has space", "add a test")
	badIDs.SessionID = "../x"
	longName := strings.Repeat("n", 120)
	cases := []struct {
		name     string
		root     string
		cc       protocol.ChildCompleted
		ok       bool
		wantKey  string
		wantText []string
	}{
		{"completed with next step", "", childDone(protocol.ChildStatusCompleted, "reviewer", "add a regression test"), true, "child:root-1:reviewer", []string{"reviewer: add a regression test", "completed"}},
		{"blocked with blockers only", "", childDone(protocol.ChildStatusBlocked, "builder", "", "needs API key", "  "), true, "child:root-1:builder", []string{"builder is blocked", "needs API key"}},
		{"failed with next and blockers", "", childDone(protocol.ChildStatusFailed, "fixer", "retry on linux", "flaky CI"), true, "child:root-1:fixer", []string{"retry on linux", "flaky CI"}},
		{"name with spaces falls back to child session", "", func() protocol.ChildCompleted {
			c := childDone(protocol.ChildStatusCompleted, "the reviewer", "ship it")
			return c
		}(), true, "child:root-1:child-1", []string{"the reviewer: ship it"}},
		{"no parent uses drain root", "root-9", noParent, true, "child:root-9:reviewer", nil},
		{"clean finish files nothing", "", childDone(protocol.ChildStatusCompleted, "reviewer", ""), false, "", nil},
		{"cancel files nothing", "", childDone(protocol.ChildStatusCanceled, "reviewer", "add a test"), false, "", nil},
		{"unknown status files nothing", "", childDone("", "reviewer", "add a test"), false, "", nil},
		{"no parent and no root", "", noParent, false, "", nil},
		{"no safe child id", "", badIDs, false, "", nil},
		{"next that is only JSON is dropped", "", childDone(protocol.ChildStatusCompleted, "reviewer", `{"a":1}`), false, "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, ok := childFollowUpDraft(tc.root, tc.cc)
			if ok != tc.ok {
				t.Fatalf("ok = %v (draft %+v)", ok, d)
			}
			if !ok {
				return
			}
			if d.EvidenceKey != tc.wantKey || d.Kind != kindChildFollowUp {
				t.Fatalf("key/kind = %q/%q", d.EvidenceKey, d.Kind)
			}
			all := d.Title + "\n" + d.Why + "\n" + d.Prompt
			for _, w := range tc.wantText {
				if !strings.Contains(all, w) {
					t.Fatalf("missing %q in %q", w, all)
				}
			}
			if len(d.Evidence) != 1 || d.Evidence[0].SessionID == "" {
				t.Fatalf("evidence = %+v", d.Evidence)
			}
		})
	}
	t.Run("long key is hashed under the store limit", func(t *testing.T) {
		cc := childDone(protocol.ChildStatusCompleted, longName, "next")
		cc.ParentSessionID = strings.Repeat("p", 120)
		d, ok := childFollowUpDraft("", cc)
		if !ok || len(d.EvidenceKey) > maxEvidenceKey || !strings.HasPrefix(d.EvidenceKey, "child:") {
			t.Fatalf("key = %q ok=%v", d.EvidenceKey, ok)
		}
		again, _ := childFollowUpDraft("", cc)
		if again.EvidenceKey != d.EvidenceKey {
			t.Fatal("hashed key is not stable")
		}
	})
	t.Run("blockers are capped and scrubbed", func(t *testing.T) {
		cc := childDone(protocol.ChildStatusBlocked, "b", "", "one", "two", "three", "four ghp_1234567890123456789012345678901234")
		d, ok := childFollowUpDraft("", cc)
		if !ok {
			t.Fatal("refused")
		}
		if strings.Contains(d.Why, "four") || strings.Contains(d.Why+d.Prompt, "ghp_1234567890") {
			t.Fatalf("why = %q", d.Why)
		}
	})
}

func TestSuggestChildFollowUpOnePerKeyAndDismissSticks(t *testing.T) {
	srv := mustServer(t, Options{SessionDir: t.TempDir()})
	cc := childDone(protocol.ChildStatusCompleted, "reviewer", "add a regression test")
	srv.SuggestChildFollowUp("", cc)
	srv.SuggestChildFollowUp("", cc)
	srv.SuggestChildFollowUp("", childDone(protocol.ChildStatusCompleted, "quiet", "")) // nothing to do
	body := suggestionsBody(t, srv)
	if len(body.Suggestions) != 1 || body.Open != 1 {
		t.Fatalf("suggestions = %+v", body.Suggestions)
	}
	sg := body.Suggestions[0]
	if sg.Source != suggestion.SourceChildAgent || sg.EvidenceKey != "child:root-1:reviewer" || sg.Repeats != 1 {
		t.Fatalf("idea = %+v", sg)
	}
	if res := notifyCall(t, srv, http.MethodPost, "/v1/suggestions/"+sg.ID+"/dismiss", ""); res.code != http.StatusOK {
		t.Fatalf("dismiss = %d %s", res.code, res.raw)
	}
	srv.SuggestChildFollowUp("", cc)
	if body = suggestionsBody(t, srv); body.Open != 0 || len(body.Suggestions) != 1 {
		t.Fatalf("after dismiss = %+v", body)
	}
}

func TestGoalStoppedDraft(t *testing.T) {
	base := host.Goal{ID: "g-1", Description: "Ship the release", Status: "failed", Autopilot: true, LastIteration: 4, FailReason: "max iterations reached"}
	with := func(f func(*host.Goal)) host.Goal {
		g := base
		f(&g)
		return g
	}
	cases := []struct {
		name     string
		g        host.Goal
		ok       bool
		wantKey  string
		wantText []string
	}{
		{"autopilot failed", base, true, "goal:g-1:failed", []string{"Autopilot goal failed: Ship the release", "max iterations reached", "4 iteration"}},
		{"autopilot blocked", with(func(g *host.Goal) { g.Status = "blocked"; g.FailReason = "" }), true, "goal:g-1:blocked", []string{"is waiting on you", "Do not resume"}},
		{"status case and space", with(func(g *host.Goal) { g.Status = " Failed " }), true, "goal:g-1:failed", nil},
		{"empty description falls back to id", with(func(g *host.Goal) { g.Description = "" }), true, "goal:g-1:failed", []string{"Goal g-1"}},
		{"manual goal files nothing", with(func(g *host.Goal) { g.Autopilot = false }), false, "", nil},
		{"done files nothing", with(func(g *host.Goal) { g.Status = "done" }), false, "", nil},
		{"aborted files nothing", with(func(g *host.Goal) { g.Status = "aborted" }), false, "", nil},
		{"paused files nothing", with(func(g *host.Goal) { g.Status = "paused" }), false, "", nil},
		{"unsafe id", with(func(g *host.Goal) { g.ID = "../g" }), false, "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, ok := goalStoppedDraft(tc.g)
			if ok != tc.ok {
				t.Fatalf("ok = %v (draft %+v)", ok, d)
			}
			if !ok {
				return
			}
			if d.EvidenceKey != tc.wantKey || d.Kind != kindGoalStopped {
				t.Fatalf("key/kind = %q/%q", d.EvidenceKey, d.Kind)
			}
			all := d.Title + "\n" + d.Why + "\n" + d.Prompt + "\n" + d.Evidence[0].Label
			for _, w := range tc.wantText {
				if !strings.Contains(all, w) {
					t.Fatalf("missing %q in %q", w, all)
				}
			}
		})
	}
	t.Run("reason is scrubbed", func(t *testing.T) {
		d, ok := goalStoppedDraft(with(func(g *host.Goal) { g.FailReason = "token ghp_1234567890123456789012345678901234 leaked" }))
		if !ok || strings.Contains(d.Why+d.Prompt, "ghp_1234567890") {
			t.Fatalf("draft = %+v", d)
		}
	})
}

func TestSuggestGoalOutcomeOnePerKeyAndMutedSource(t *testing.T) {
	srv := mustServer(t, Options{SessionDir: t.TempDir()})
	g := host.Goal{ID: "g-1", Description: "Ship", Status: "failed", Autopilot: true}
	srv.SuggestGoalOutcome(g)
	srv.SuggestGoalOutcome(g)
	srv.SuggestGoalOutcome(host.Goal{ID: "g-2", Description: "Manual", Status: "failed"})
	body := suggestionsBody(t, srv)
	if len(body.Suggestions) != 1 || body.Suggestions[0].Source != suggestion.SourceAutopilot || body.Suggestions[0].EvidenceKey != "goal:g-1:failed" {
		t.Fatalf("suggestions = %+v", body.Suggestions)
	}
	// A muted producer cannot deposit.
	if res := notifyCall(t, srv, http.MethodPut, "/v1/suggestions/prefs", `{"mutedSources":["goal-autopilot"],"mutedKinds":[]}`); res.code != http.StatusOK {
		t.Fatalf("prefs = %d %s", res.code, res.raw)
	}
	srv.SuggestGoalOutcome(host.Goal{ID: "g-3", Description: "Other", Status: "blocked", Autopilot: true})
	if body = suggestionsBody(t, srv); len(body.Suggestions) != 1 {
		t.Fatalf("muted source deposited: %+v", body.Suggestions)
	}
}
