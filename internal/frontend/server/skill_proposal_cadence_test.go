package server

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike/internal/persist/suggestion"
	"github.com/jonathanung/strike/internal/product/skillpropose"
)

func proposalIdeas(t *testing.T, srv *Server) []suggestion.Suggestion {
	t.Helper()
	store, err := srv.suggestions()
	if err != nil {
		t.Fatal(err)
	}
	list, _, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	var out []suggestion.Suggestion
	for _, sg := range list {
		if sg.Source == proposalIdeaSource {
			out = append(out, sg)
		}
	}
	return out
}

func TestSkillProposalCadenceNeverScansWhileOff(t *testing.T) {
	f := newProposalsFixture(t, false)
	// GET records the directory, but the opt-in is off.
	f.call(t, http.MethodGet, "/v1/skill-proposals", "")
	if n := f.srv.scanDueSkillProposals(time.Now().Add(30 * 24 * time.Hour)); n != 0 {
		t.Fatalf("scanned %d workspaces while off", n)
	}
	ws, err := proposalStore().Get(f.key)
	if err != nil || !ws.LastScan.IsZero() || len(ws.Pending) != 0 {
		t.Fatalf("off workspace changed: %+v %v", ws, err)
	}
	if ideas := proposalIdeas(t, f.srv); len(ideas) != 0 {
		t.Fatalf("ideas filed while off: %+v", ideas)
	}
}

func TestSkillProposalCadenceScansWeeklyAndMirrorsIdeas(t *testing.T) {
	f := newProposalsFixture(t, false)
	if code, _, raw := f.call(t, http.MethodPatch, "/v1/skill-proposals", `{"enabled":true}`); code != http.StatusOK {
		t.Fatalf("enable = %d %s", code, raw)
	}
	now := time.Now()
	if n := f.srv.scanDueSkillProposals(now); n != 1 {
		t.Fatalf("first cadence scanned %d, want 1 (opted in, never scanned)", n)
	}
	ws, _ := proposalStore().Get(f.key)
	if len(ws.Pending) != 1 || ws.LastScan.IsZero() {
		t.Fatalf("cadence scan = %+v", ws)
	}
	// Not due again within the week.
	if n := f.srv.scanDueSkillProposals(now.Add(time.Hour)); n != 0 {
		t.Fatalf("rescanned within the week: %d", n)
	}
	if n := f.srv.scanDueSkillProposals(now.Add(skillpropose.WeeklyCadence)); n != 1 {
		t.Fatalf("weekly rescan = %d, want 1", n)
	}
	// The draft is mirrored once in the Ideas inbox, with evidence sessions.
	ideas := proposalIdeas(t, f.srv)
	if len(ideas) != 1 {
		t.Fatalf("ideas = %+v", ideas)
	}
	idea := ideas[0]
	if idea.Kind != proposalIdeaKind || idea.EvidenceKey != proposalIdeaKey(ws.Pending[0].ID) || len(idea.Evidence) != 3 || idea.Evidence[0].SessionID == "" {
		t.Fatalf("idea = %+v", idea)
	}
	if !strings.Contains(idea.Title, ws.Pending[0].Name) || !strings.Contains(idea.Prompt, "Customize") {
		t.Fatalf("idea text = %+v", idea)
	}
}

func TestSkillProposalIdeaSettlesWithCustomize(t *testing.T) {
	cases := []struct {
		name  string
		path  string
		state suggestion.State
	}{
		{"accept", "/v1/skill-proposals/accept", suggestion.StateAccepted},
		{"dismiss", "/v1/skill-proposals/dismiss", suggestion.StateDismissed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newProposalsFixture(t, false)
			f.call(t, http.MethodPatch, "/v1/skill-proposals", `{"enabled":true}`)
			_, got, raw := f.call(t, http.MethodPost, "/v1/skill-proposals/scan", "")
			if len(got.Pending) != 1 {
				t.Fatalf("scan = %s", raw)
			}
			if ideas := proposalIdeas(t, f.srv); len(ideas) != 1 || ideas[0].State != suggestion.StateOpen {
				t.Fatalf("manual scan did not mirror: %+v", ideas)
			}
			code, _, raw := f.call(t, http.MethodPost, tc.path, `{"id":"`+got.Pending[0].ID+`"}`)
			if code != http.StatusOK {
				t.Fatalf("%s = %d %s", tc.name, code, raw)
			}
			ideas := proposalIdeas(t, f.srv)
			if len(ideas) != 1 || ideas[0].State != tc.state {
				t.Fatalf("idea after %s = %+v", tc.name, ideas)
			}
		})
	}
}

func TestSkillProposalCadenceReadOnly(t *testing.T) {
	f := newProposalsFixture(t, true)
	on := true
	if _, err := proposalStore().Settings(f.key, &on, nil); err != nil {
		t.Fatal(err)
	}
	if err := proposalStore().SetDir(f.key, f.work); err != nil {
		t.Fatal(err)
	}
	if n := f.srv.scanDueSkillProposals(time.Now()); n != 0 {
		t.Fatalf("read-only scanned %d", n)
	}
}

func TestProposalIdeaDraft(t *testing.T) {
	p := skillpropose.Proposal{
		ID:          "abc",
		Name:        "bump-version-update",
		Description: "Repeated workflow: bump",
		Sessions: []skillpropose.Evidence{
			{SessionID: "s1", Title: "Release 1"},
			{SessionID: "bad id", Title: "skipped"},
			{SessionID: "s3"},
		},
	}
	d := proposalIdea(p)
	if d.Source != proposalIdeaSource || d.EvidenceKey != "skill-proposal:abc" || d.Kind != proposalIdeaKind {
		t.Fatalf("draft = %+v", d)
	}
	if len(d.Evidence) != 2 || d.Evidence[0].Label != "Release 1" || d.Evidence[1].Label != "Session" {
		t.Fatalf("evidence = %+v", d.Evidence)
	}
	if !strings.Contains(d.Why, "3 sessions") || !strings.HasPrefix(d.Why, "Repeated workflow") {
		t.Fatalf("why = %q", d.Why)
	}
	long := p
	long.Name = strings.Repeat("n", 200)
	if got := proposalIdea(long); len([]rune(got.Title)) != 120 {
		t.Fatalf("title not clamped: %d", len([]rune(got.Title)))
	}
}
