package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jonathanung/strike/internal/persist/session"
)

// fakeGH records calls and answers by first two args ("pr view", "run view").
type fakeGH struct {
	mu    sync.Mutex
	calls [][]string
	dirs  []string
	pr    string
	prErr error
	log   string
	logEr error
}

func (f *fakeGH) run(_ context.Context, dir string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, append([]string(nil), args...))
	f.dirs = append(f.dirs, dir)
	if len(args) >= 2 && args[0] == "run" && args[1] == "view" {
		if f.logEr != nil {
			return nil, f.logEr
		}
		return []byte(f.log), nil
	}
	if f.prErr != nil {
		return nil, f.prErr
	}
	return []byte(f.pr), nil
}

func (f *fakeGH) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(strings.Join(c, " "), prefix) {
			n++
		}
	}
	return n
}

func fakeToken() string { return "ghp_" + strings.Repeat("Z9", 18) }

func seedPRSession(t *testing.T, dir, id string, meta session.Meta) {
	t.Helper()
	line := `{"type":"user.message","time":"2026-11-20T06:00:00Z","data":{"text":"ship it"}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := session.WriteMeta(dir, id, meta); err != nil {
		t.Fatal(err)
	}
}

func newPRServer(t *testing.T, gh *fakeGH) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	srv, err := New(Options{SessionDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	srv.prWatch.run = gh.run
	return srv, dir
}

func prGet(t *testing.T, srv *Server, path string) (int, string) {
	t.Helper()
	res := httptest.NewRecorder()
	srv.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, path, nil))
	return res.Code, res.Body.String()
}

func prPost(t *testing.T, srv *Server, path string) (int, string) {
	t.Helper()
	res := httptest.NewRecorder()
	srv.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodPost, path, nil))
	return res.Code, res.Body.String()
}

const prURL = "https://github.com/acme/widgets/pull/42"

func prJSON(state, review string, checks string) string {
	return `{"number":42,"url":"` + prURL + `","state":"` + state + `","reviewDecision":"` + review + `","statusCheckRollup":[` + checks + `]}`
}

const (
	checkPass = `{"__typename":"CheckRun","name":"test","workflowName":"CI","status":"COMPLETED","conclusion":"SUCCESS","detailsUrl":"https://github.com/acme/widgets/actions/runs/1/job/11"}`
	checkFail = `{"__typename":"CheckRun","name":"vet","workflowName":"CI","status":"COMPLETED","conclusion":"FAILURE","detailsUrl":"https://github.com/acme/widgets/actions/runs/1/job/12"}`
	checkRun  = `{"__typename":"CheckRun","name":"race","workflowName":"CI","status":"IN_PROGRESS","conclusion":"","detailsUrl":"https://github.com/acme/widgets/actions/runs/1/job/13"}`
	checkCtx  = `{"__typename":"StatusContext","context":"ci/external","state":"FAILURE","targetUrl":"https://ci.example.com/build/9"}`
)

func TestSessionsPayloadIncludesPRFields(t *testing.T) {
	srv, dir := newPRServer(t, &fakeGH{})
	seedPRSession(t, dir, "pr-session", session.Meta{Title: "Shipped", PRURL: prURL, PRNumber: 42, PRState: "OPEN"})
	seedPRSession(t, dir, "plain-session", session.Meta{Title: "No PR"})
	code, body := prGet(t, srv, "/v1/sessions")
	if code != http.StatusOK {
		t.Fatalf("status = %d: %s", code, body)
	}
	var got sessionsResponse
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	byID := map[string]sessionListItem{}
	for _, it := range got.Sessions {
		byID[it.ID] = it
	}
	pr := byID["pr-session"]
	if pr.PRURL != prURL || pr.PRNumber != 42 || pr.PRState != "open" {
		t.Fatalf("pr-session PR fields = %+v", pr)
	}
	plain := byID["plain-session"]
	if plain.PRURL != "" || plain.PRNumber != 0 || plain.PRState != "" {
		t.Fatalf("plain-session should carry no PR fields: %+v", plain)
	}
	if strings.Contains(body, `"prUrl":""`) {
		t.Fatalf("empty prUrl should be omitted: %s", body)
	}
}

func TestMapCheckStates(t *testing.T) {
	cases := []struct {
		name string
		item ghRollupItem
		want string
	}{
		{"queued", ghRollupItem{Typename: "CheckRun", Name: "a", Status: "QUEUED"}, prCheckQueued},
		{"waiting", ghRollupItem{Typename: "CheckRun", Name: "a", Status: "WAITING"}, prCheckQueued},
		{"pending run", ghRollupItem{Typename: "CheckRun", Name: "a", Status: "PENDING"}, prCheckQueued},
		{"running", ghRollupItem{Typename: "CheckRun", Name: "a", Status: "IN_PROGRESS"}, prCheckRunning},
		{"pass", ghRollupItem{Typename: "CheckRun", Name: "a", Status: "COMPLETED", Conclusion: "SUCCESS"}, prCheckPass},
		{"fail", ghRollupItem{Typename: "CheckRun", Name: "a", Status: "COMPLETED", Conclusion: "FAILURE"}, prCheckFail},
		{"timed out", ghRollupItem{Typename: "CheckRun", Name: "a", Status: "COMPLETED", Conclusion: "TIMED_OUT"}, prCheckFail},
		{"cancelled", ghRollupItem{Typename: "CheckRun", Name: "a", Status: "COMPLETED", Conclusion: "CANCELLED"}, prCheckFail},
		{"skipped", ghRollupItem{Typename: "CheckRun", Name: "a", Status: "COMPLETED", Conclusion: "SKIPPED"}, prCheckSkipped},
		{"neutral", ghRollupItem{Typename: "CheckRun", Name: "a", Status: "COMPLETED", Conclusion: "NEUTRAL"}, prCheckSkipped},
		{"ctx success", ghRollupItem{Typename: "StatusContext", Context: "c", State: "SUCCESS"}, prCheckPass},
		{"ctx pending", ghRollupItem{Typename: "StatusContext", Context: "c", State: "PENDING"}, prCheckRunning},
		{"ctx expected", ghRollupItem{Typename: "StatusContext", Context: "c", State: "EXPECTED"}, prCheckQueued},
		{"ctx error", ghRollupItem{Typename: "StatusContext", Context: "c", State: "ERROR"}, prCheckFail},
		{"ctx failure", ghRollupItem{Typename: "StatusContext", Context: "c", State: "FAILURE"}, prCheckFail},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mapPRChecks([]ghRollupItem{tc.item})
			if len(got) != 1 || got[0].State != tc.want {
				t.Fatalf("mapPRChecks(%+v) = %+v, want state %q", tc.item, got, tc.want)
			}
		})
	}
}

func TestMapCheckNamesAndURLs(t *testing.T) {
	got := mapPRChecks([]ghRollupItem{
		{Typename: "CheckRun", Name: "vet", WorkflowName: "CI", Status: "COMPLETED", Conclusion: "SUCCESS", DetailsURL: "https://github.com/a/b/actions/runs/1/job/2"},
		{Typename: "StatusContext", Context: "ext", State: "SUCCESS", TargetURL: "https://ci.example.com/x"},
		{Typename: "CheckRun", Name: "", Status: "QUEUED", DetailsURL: "javascript:alert(1)"},
	})
	if got[0].Name != "CI / vet" || got[0].URL == "" {
		t.Fatalf("check run name/url = %+v", got[0])
	}
	if got[1].Name != "check" || got[1].URL != "" {
		t.Fatalf("unnamed check / unsafe url should be dropped: %+v", got[1])
	}
	if got[2].Name != "ext" || got[2].URL != "" {
		t.Fatalf("non-GitHub status URL should be dropped: %+v", got[2])
	}
}

func TestRollupPRChecks(t *testing.T) {
	c := func(states ...string) []prCheck {
		out := []prCheck{}
		for _, s := range states {
			out = append(out, prCheck{Name: s, State: s})
		}
		return out
	}
	cases := []struct {
		in   []prCheck
		want string
	}{
		{nil, "none"},
		{c(prCheckPass, prCheckSkipped), prCheckPass},
		{c(prCheckSkipped), prCheckPass},
		{c(prCheckPass, prCheckQueued), prCheckQueued},
		{c(prCheckQueued, prCheckRunning), prCheckRunning},
		{c(prCheckRunning, prCheckFail, prCheckPass), prCheckFail},
	}
	for _, tc := range cases {
		if got := rollupPRChecks(tc.in); got != tc.want {
			t.Errorf("rollupPRChecks(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSessionPRStatusFailingOpenPR(t *testing.T) {
	gh := &fakeGH{pr: prJSON("OPEN", "", strings.Join([]string{checkPass, checkFail, checkCtx}, ","))}
	srv, dir := newPRServer(t, gh)
	clock := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	srv.prWatch.now = func() time.Time { return clock }
	seedPRSession(t, dir, "s1", session.Meta{PRURL: prURL, PRNumber: 42, PRState: "open"})

	code, body := prGet(t, srv, "/v1/sessions/s1/pr")
	if code != http.StatusOK {
		t.Fatalf("status = %d: %s", code, body)
	}
	var st prWatchStatus
	if err := json.Unmarshal([]byte(body), &st); err != nil {
		t.Fatal(err)
	}
	if !st.Linked || !st.Available || st.Rollup != prCheckFail || !st.Attention || !st.Poll || st.PollAfterSec != prWatchPollSec {
		t.Fatalf("status = %+v", st)
	}
	if len(st.Checks) != 3 {
		t.Fatalf("checks = %+v", st.Checks)
	}
	// gh is asked for the PR by URL with the rollup fields.
	if gh.count("pr view "+prURL+" --json number,url,state,reviewDecision,statusCheckRollup") != 1 || gh.dirs[0] != "" {
		t.Fatalf("gh calls = %v dirs = %v", gh.calls, gh.dirs)
	}
	// Second poll inside the cache TTL does not run gh again.
	if code, _ := prGet(t, srv, "/v1/sessions/s1/pr"); code != http.StatusOK {
		t.Fatal(code)
	}
	if n := gh.count("pr view"); n != 1 {
		t.Fatalf("cached poll ran gh %d times", n)
	}
	// refresh=1 inside the 5s floor still serves the cache...
	prGet(t, srv, "/v1/sessions/s1/pr?refresh=1")
	if n := gh.count("pr view"); n != 1 {
		t.Fatalf("refresh inside the floor ran gh %d times total, want 1", n)
	}
	// ...and past the floor (but inside the 30s TTL) asks gh again.
	clock = clock.Add(prWatchRefreshFloor + time.Second)
	prGet(t, srv, "/v1/sessions/s1/pr?refresh=1")
	if n := gh.count("pr view"); n != 2 {
		t.Fatalf("refresh ran gh %d times total, want 2", n)
	}
	prGet(t, srv, "/v1/sessions/s1/pr")
	if n := gh.count("pr view"); n != 2 {
		t.Fatalf("plain poll after refresh ran gh %d times total, want 2", n)
	}
	// Attention raised one notification, deduped across polls.
	items, _, _ := srv.notifications().snapshot(0)
	got := 0
	for _, it := range items {
		if it.Kind == notePRAttention && it.SessionID == "s1" {
			got++
			if !strings.Contains(it.Body, "CI / vet") || !strings.Contains(it.Title, "#42") {
				t.Fatalf("note = %+v", it)
			}
		}
	}
	if got != 1 {
		t.Fatalf("pr.attention notes = %d, want 1 (%+v)", got, items)
	}
}

func TestSessionPRStatusStopsPollingOnTerminalState(t *testing.T) {
	for _, state := range []string{"MERGED", "CLOSED"} {
		t.Run(state, func(t *testing.T) {
			gh := &fakeGH{pr: prJSON(state, "", checkFail)}
			srv, dir := newPRServer(t, gh)
			seedPRSession(t, dir, "s1", session.Meta{PRURL: prURL, PRNumber: 42, PRState: "open"})
			_, body := prGet(t, srv, "/v1/sessions/s1/pr")
			var st prWatchStatus
			if err := json.Unmarshal([]byte(body), &st); err != nil {
				t.Fatal(err)
			}
			if st.Poll || st.PollAfterSec != 0 || st.Attention {
				t.Fatalf("terminal PR should stop polling and need no attention: %+v", st)
			}
			if st.State != strings.ToLower(state) {
				t.Fatalf("state = %q", st.State)
			}
			meta, err := session.ReadMeta(dir, "s1")
			if err != nil {
				t.Fatal(err)
			}
			if meta.PRState != strings.ToLower(state) || meta.PRUpdatedAt == "" {
				t.Fatalf("terminal state not persisted: %+v", meta)
			}
		})
	}
}

func TestSessionPRStatusChangesRequestedNeedsAttention(t *testing.T) {
	gh := &fakeGH{pr: prJSON("OPEN", "CHANGES_REQUESTED", checkPass)}
	srv, dir := newPRServer(t, gh)
	seedPRSession(t, dir, "s1", session.Meta{PRURL: prURL})
	_, body := prGet(t, srv, "/v1/sessions/s1/pr")
	var st prWatchStatus
	_ = json.Unmarshal([]byte(body), &st)
	if !st.Attention || st.ReviewDecision != "changes_requested" || st.Rollup != prCheckPass {
		t.Fatalf("status = %+v", st)
	}
}

func TestSessionPRStatusUnavailableStopsPolling(t *testing.T) {
	cases := []struct {
		name     string
		meta     session.Meta
		err      error
		want     string
		poll     bool
		ghCalled bool
	}{
		{"unauthenticated", session.Meta{PRURL: prURL}, errGHUnauthenticated, "gh auth login", false, true},
		{"gh missing", session.Meta{PRURL: prURL}, errGHMissing, "not installed", false, true},
		{"no remote", session.Meta{PRNumber: 7, ProjectKey: "/work/repo"}, errPRNotFound, "no GitHub remote", false, true},
		{"number without project", session.Meta{PRNumber: 7}, nil, "without a link or project directory", false, false},
		{"transient", session.Meta{PRURL: prURL}, errGHFailed, "Retrying later", true, true},
		{"non-github", session.Meta{PRURL: "https://gitlab.example.com/a/b/-/merge_requests/3"}, nil, "GitHub pull requests only", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gh := &fakeGH{prErr: tc.err}
			srv, dir := newPRServer(t, gh)
			seedPRSession(t, dir, "s1", tc.meta)
			code, body := prGet(t, srv, "/v1/sessions/s1/pr")
			if code != http.StatusOK {
				t.Fatalf("status = %d", code)
			}
			var st prWatchStatus
			if err := json.Unmarshal([]byte(body), &st); err != nil {
				t.Fatal(err)
			}
			if !st.Linked || st.Available || !strings.Contains(st.Reason, tc.want) || st.Poll != tc.poll || st.Attention {
				t.Fatalf("status = %+v", st)
			}
			if tc.poll && st.PollAfterSec != prWatchErrorPollSec {
				t.Fatalf("transient error should back off: %+v", st)
			}
			if (gh.count("pr view") > 0) != tc.ghCalled {
				t.Fatalf("gh called = %v, want %v", gh.calls, tc.ghCalled)
			}
		})
	}
}

func TestSessionPRStatusUnlinkedAndInvalid(t *testing.T) {
	gh := &fakeGH{}
	srv, dir := newPRServer(t, gh)
	seedPRSession(t, dir, "plain", session.Meta{Title: "x"})
	_, body := prGet(t, srv, "/v1/sessions/plain/pr")
	var st prWatchStatus
	_ = json.Unmarshal([]byte(body), &st)
	if st.Linked || st.Poll || len(gh.calls) != 0 {
		t.Fatalf("unlinked session = %+v calls=%v", st, gh.calls)
	}
	if code, _ := prGet(t, srv, "/v1/sessions/a..b/pr"); code != http.StatusBadRequest {
		t.Fatalf("invalid id status = %d", code)
	}
}

func TestPRFixDraftIsRedactedAndPostsNothing(t *testing.T) {
	secret := fakeToken()
	t.Setenv("GH_TOKEN", secret)
	gh := &fakeGH{
		pr:  prJSON("OPEN", "CHANGES_REQUESTED", strings.Join([]string{checkPass, checkFail, checkCtx}, ",")),
		log: "vet\tRun go vet\t./x.go:3: bad\nvet\tRun go vet\ttoken=" + secret + "\n",
	}
	srv, dir := newPRServer(t, gh)
	seedPRSession(t, dir, "s1", session.Meta{PRURL: prURL, PRNumber: 42, PRState: "open"})
	before, err := os.ReadFile(filepath.Join(dir, "s1.jsonl"))
	if err != nil {
		t.Fatal(err)
	}

	code, body := prPost(t, srv, "/v1/sessions/s1/pr/fix-draft")
	if code != http.StatusOK {
		t.Fatalf("status = %d: %s", code, body)
	}
	var draft prFixDraft
	if err := json.Unmarshal([]byte(body), &draft); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"PR #42", "CI / vet", "./x.go:3: bad", "ci/external", "not a GitHub Actions job", "requested changes", "gh pr view 42 --comments", "Do not push or merge"} {
		if !strings.Contains(draft.Text, want) {
			t.Errorf("draft missing %q:\n%s", want, draft.Text)
		}
	}
	if strings.Contains(body, secret) {
		t.Fatalf("draft leaked a token: %s", body)
	}
	if !draft.Review || len(draft.Checks) != 2 {
		t.Fatalf("draft meta = %+v", draft)
	}
	if gh.count("run view --repo acme/widgets --job 12 --log-failed") != 1 {
		t.Fatalf("gh calls = %v", gh.calls)
	}
	// The draft route never writes to the session: posting is a separate,
	// user-confirmed user.input op.
	after, err := os.ReadFile(filepath.Join(dir, "s1.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("fix-draft modified the session log")
	}
}

func TestPRFixDraftRefusesWithoutAttention(t *testing.T) {
	cases := []struct {
		name string
		meta session.Meta
		gh   *fakeGH
	}{
		{"unlinked", session.Meta{}, &fakeGH{}},
		{"all green", session.Meta{PRURL: prURL}, &fakeGH{pr: prJSON("OPEN", "", checkPass)}},
		{"merged", session.Meta{PRURL: prURL}, &fakeGH{pr: prJSON("MERGED", "", checkFail)}},
		{"gh signed out", session.Meta{PRURL: prURL}, &fakeGH{prErr: errGHUnauthenticated}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, dir := newPRServer(t, tc.gh)
			seedPRSession(t, dir, "s1", tc.meta)
			if code, body := prPost(t, srv, "/v1/sessions/s1/pr/fix-draft"); code != http.StatusConflict {
				t.Fatalf("status = %d: %s", code, body)
			}
			if tc.gh.count("run view") != 0 {
				t.Fatalf("no logs should be fetched: %v", tc.gh.calls)
			}
		})
	}
}

func TestPRWatchNeverReturnsTokens(t *testing.T) {
	secret := fakeToken()
	t.Setenv("GH_TOKEN", secret)
	t.Setenv("GITHUB_TOKEN", secret)
	gh := &fakeGH{pr: prJSON("OPEN", "", checkRun)}
	srv, dir := newPRServer(t, gh)
	seedPRSession(t, dir, "s1", session.Meta{PRURL: prURL})
	for _, path := range []string{"/v1/sessions", "/v1/sessions/s1/pr"} {
		if _, body := prGet(t, srv, path); strings.Contains(body, secret) {
			t.Fatalf("%s leaked a token", path)
		}
	}
	// Errors never carry gh stderr.
	reason, _ := prWatchErrorReason(classifyGHStderr("HTTP 401: Bad credentials " + secret))
	if strings.Contains(reason, secret) || !strings.Contains(reason, "gh auth login") {
		t.Fatalf("reason = %q", reason)
	}
}

func TestClassifyGHStderr(t *testing.T) {
	cases := []struct {
		in   string
		want error
	}{
		{"To get started with GitHub CLI, please run:  gh auth login", errGHUnauthenticated},
		{"You are not logged into any GitHub hosts.", errGHUnauthenticated},
		{"HTTP 401: Bad credentials", errGHUnauthenticated},
		{"GraphQL: Could not resolve to a PullRequest with the number of 9.", errPRNotFound},
		{"none of the git remotes configured for this repository point to a known GitHub host", errPRNotFound},
		{"fatal: not a git repository", errPRNotFound},
		{"something else broke", errGHFailed},
	}
	for _, tc := range cases {
		if got := classifyGHStderr(tc.in); !errors.Is(got, tc.want) {
			t.Errorf("classifyGHStderr(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestPRWatchURLHelpers(t *testing.T) {
	for raw, want := range map[string]bool{
		prURL: true,
		"https://github.com/acme/widgets/pull/42/":       true,
		"http://github.com/acme/widgets/pull/42":         false,
		"https://github.com.evil.io/acme/widgets/pull/4": false,
		"https://github.com/acme/widgets/issues/4":       false,
		"https://gitlab.com/acme/widgets/pull/4":         false,
	} {
		if got := isGitHubPRURL(raw); got != want {
			t.Errorf("isGitHubPRURL(%q) = %v, want %v", raw, got, want)
		}
	}
	repo, job, ok := actionsJobRef("https://github.com/acme/widgets/actions/runs/123/job/456")
	if !ok || repo != "acme/widgets" || job != "456" {
		t.Fatalf("actionsJobRef = %q %q %v", repo, job, ok)
	}
	for _, raw := range []string{"https://ci.example.com/acme/widgets/actions/runs/1/job/2", "https://github.com/acme/widgets/runs/2", ""} {
		if _, _, ok := actionsJobRef(raw); ok {
			t.Errorf("actionsJobRef(%q) should fail", raw)
		}
	}
}

func TestTailLines(t *testing.T) {
	lines := make([]string, 150)
	for i := range lines {
		lines[i] = "line"
	}
	lines[149] = "last"
	out, cut := tailLines(strings.Join(lines, "\n")+"\n", 100, 1<<20)
	if !cut || strings.Count(out, "\n") != 99 || !strings.HasSuffix(out, "last") {
		t.Fatalf("tail = %d lines cut=%v", strings.Count(out, "\n")+1, cut)
	}
	out, cut = tailLines("a\nb", 100, 1<<20)
	if cut || out != "a\nb" {
		t.Fatalf("short = %q %v", out, cut)
	}
	out, cut = tailLines(strings.Repeat("x", 50)+"\n"+strings.Repeat("y", 10), 100, 20)
	if !cut || out != strings.Repeat("y", 10) {
		t.Fatalf("byte cap = %q %v", out, cut)
	}
}

func TestPRAttentionNoteDedupesBySet(t *testing.T) {
	now := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	a := prWatchStatus{SessionID: "s", URL: prURL, Number: 42, Checks: []prCheck{{Name: "vet", State: prCheckFail}}}
	b := a
	b.Checks = []prCheck{{Name: "vet", State: prCheckFail}, {Name: "race", State: prCheckFail}}
	if prAttentionNote(a, now).ID == prAttentionNote(b, now).ID {
		t.Fatal("a new failure set must produce a new note id")
	}
	if prAttentionNote(a, now).ID != prAttentionNote(a, now.Add(time.Hour)).ID {
		t.Fatal("the same failure set must keep its id")
	}
	if prAttentionNote(a, now).Href != "/#session=s" {
		t.Fatalf("href = %q", prAttentionNote(a, now).Href)
	}
}

func TestSessionPRNumberOnlyRunsInSessionRepo(t *testing.T) {
	gh := &fakeGH{pr: prJSON("OPEN", "", checkPass)}
	srv, dir := newPRServer(t, gh)
	seedPRSession(t, dir, "s1", session.Meta{PRNumber: 42, ProjectKey: "/work/session-repo"})
	_, body := prGet(t, srv, "/v1/sessions/s1/pr")
	var st prWatchStatus
	_ = json.Unmarshal([]byte(body), &st)
	if !st.Available || st.URL != prURL {
		t.Fatalf("status = %+v", st)
	}
	if len(gh.calls) != 1 || gh.calls[0][2] != "42" || gh.dirs[0] != "/work/session-repo" {
		t.Fatalf("gh calls = %v dirs = %v", gh.calls, gh.dirs)
	}
}

func TestCapBuffer(t *testing.T) {
	head := &capBuffer{max: 4}
	_, _ = head.Write([]byte("abc"))
	_, _ = head.Write([]byte("def"))
	if string(head.buf) != "abcd" || !head.over {
		t.Fatalf("head = %q over=%v", head.buf, head.over)
	}
	tail := &capBuffer{max: 4, keepTail: true}
	_, _ = tail.Write([]byte("abc"))
	_, _ = tail.Write([]byte("def"))
	if string(tail.buf) != "cdef" || !tail.over {
		t.Fatalf("tail = %q over=%v", tail.buf, tail.over)
	}
	small := &capBuffer{max: 10}
	if n, err := small.Write([]byte("hi")); n != 2 || err != nil || small.over {
		t.Fatalf("small write = %d %v over=%v", n, err, small.over)
	}
}

func TestDefaultGHRunnerUsesFakeGH(t *testing.T) {
	bin := t.TempDir()
	script := filepath.Join(bin, "gh")
	body := "#!/bin/sh\nif [ \"$1\" = fail ]; then echo 'To get started with GitHub CLI, please run:  gh auth login' >&2; exit 4; fi\nprintf '%s' \"$GH_PROMPT_DISABLED:$(pwd -P)\"\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	old := ghBin
	ghBin = script
	t.Cleanup(func() { ghBin = old })
	work, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	out, err := defaultGHRunner(context.Background(), work, "pr", "view")
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out); got != "1:"+work {
		t.Fatalf("stdout = %q", got)
	}
	if _, err := defaultGHRunner(context.Background(), "", "fail"); !errors.Is(err, errGHUnauthenticated) {
		t.Fatalf("err = %v, want errGHUnauthenticated", err)
	}
	ghBin = filepath.Join(bin, "missing-gh")
	if _, err := defaultGHRunner(context.Background(), "", "pr"); !errors.Is(err, errGHMissing) {
		t.Fatalf("err = %v, want errGHMissing", err)
	}
}
