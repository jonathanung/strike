package deploywatch

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const testSHA = "0123456789abcdef0123456789abcdef01234567"

var t0 = time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC)

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func TestEvaluate(t *testing.T) {
	specs := []CheckSpec{{Kind: KindHTTP, Name: "api"}, {Kind: KindGitHubChecks, Name: "ci"}}
	deadline := t0.Add(time.Hour)
	res := func(api, ci string) []CheckResult {
		var out []CheckResult
		if api != "" {
			out = append(out, CheckResult{Name: "api", State: api})
		}
		if ci != "" {
			out = append(out, CheckResult{Name: "ci", State: ci})
		}
		return out
	}
	cases := []struct {
		name        string
		latest      []CheckResult
		now         time.Time
		wantVerdict string
		wantFinal   bool
		wantReason  string
	}{
		{"healthy mid-window does not settle", res(StateHealthy, StateHealthy), t0, "", false, ""},
		{"pending mid-window does not settle", res(StatePending, StateHealthy), t0, "", false, ""},
		{"failing mid-window is regression", res(StateFailing, StateHealthy), t0, VerdictRegression, true, "failing: api"},
		{"failing beats unreachable", res(StateFailing, StateUnreachable), deadline, VerdictRegression, true, "failing: api"},
		{"all healthy at deadline", res(StateHealthy, StateHealthy), deadline, VerdictHealthy, true, "all 2 checks healthy"},
		{"unreachable at deadline is inconclusive", res(StateHealthy, StateUnreachable), deadline, VerdictInconclusive, true, "ci (unreachable)"},
		{"pending at deadline is inconclusive", res(StatePending, StateHealthy), deadline.Add(time.Minute), VerdictInconclusive, true, "api (pending)"},
		{"never observed is inconclusive, not healthy", res(StateHealthy, ""), deadline, VerdictInconclusive, true, "ci (never observed)"},
		{"no observations at all", nil, deadline, VerdictInconclusive, true, "never observed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v, reason, final := Evaluate(specs, tc.latest, tc.now, deadline)
			if v != tc.wantVerdict || final != tc.wantFinal || !strings.Contains(reason, tc.wantReason) {
				t.Fatalf("Evaluate = (%q, %q, %v), want (%q, ~%q, %v)", v, reason, final, tc.wantVerdict, tc.wantReason, tc.wantFinal)
			}
		})
	}
	if v, _, final := Evaluate(nil, nil, deadline, deadline); v != VerdictInconclusive || !final {
		t.Fatalf("no specs = %q %v, want inconclusive final", v, final)
	}
}

// fakeGitHub serves check-runs and commit status for one repo@sha.
type fakeGitHub struct {
	mu        sync.Mutex
	runs      string
	statuses  string
	code      int
	gotAuth   []string
	gotPaths  []string
	gotAccept string
}

func (f *fakeGitHub) set(runs, statuses string) {
	f.mu.Lock()
	f.runs, f.statuses = runs, statuses
	f.mu.Unlock()
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gotAuth = append(f.gotAuth, r.Header.Get("Authorization"))
	f.gotPaths = append(f.gotPaths, r.URL.Path)
	f.gotAccept = r.Header.Get("Accept")
	if f.code != 0 {
		w.WriteHeader(f.code)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
		return
	}
	switch r.URL.Path {
	case "/repos/acme/app/commits/" + testSHA + "/check-runs":
		_, _ = w.Write([]byte(f.runs))
	case "/repos/acme/app/commits/" + testSHA + "/status":
		_, _ = w.Write([]byte(f.statuses))
	default:
		http.NotFound(w, r)
	}
}

func runsJSON(total int, runs ...string) string {
	return fmt.Sprintf(`{"total_count":%d,"check_runs":[%s]}`, total, strings.Join(runs, ","))
}

func run(name, status, conclusion string) string {
	return fmt.Sprintf(`{"name":%q,"status":%q,"conclusion":%q}`, name, status, conclusion)
}

func statusJSON(pairs ...string) string {
	var rows []string
	for i := 0; i+1 < len(pairs); i += 2 {
		rows = append(rows, fmt.Sprintf(`{"context":%q,"state":%q}`, pairs[i], pairs[i+1]))
	}
	return `{"state":"x","statuses":[` + strings.Join(rows, ",") + `]}`
}

func newGitHub(t *testing.T, f *fakeGitHub) *GitHub {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return &GitHub{BaseURL: srv.URL + "/", Client: srv.Client(), Token: func(context.Context) string { return "tok" }}
}

func TestGitHubCheck(t *testing.T) {
	cases := []struct {
		name       string
		runs       string
		statuses   string
		code       int
		wantState  string
		wantDetail string
	}{
		{"all passed", runsJSON(2, run("test", "completed", "success"), run("lint", "completed", "skipped")), statusJSON("deploy", "success"), 0, StateHealthy, "3 of 3 passed"},
		{"one failing run", runsJSON(2, run("test", "completed", "failure"), run("lint", "completed", "success")), statusJSON(), 0, StateFailing, "test=failure"},
		{"timed out is failing", runsJSON(1, run("e2e", "completed", "timed_out")), statusJSON(), 0, StateFailing, "e2e=timed_out"},
		{"legacy status error is failing", runsJSON(1, run("test", "completed", "success")), statusJSON("ci/legacy", "error"), 0, StateFailing, "ci/legacy=error"},
		{"in progress is pending", runsJSON(1, run("test", "in_progress", "")), statusJSON(), 0, StatePending, "test=in_progress"},
		{"cancelled is pending, not pass or fail", runsJSON(1, run("test", "completed", "cancelled")), statusJSON(), 0, StatePending, "test=cancelled"},
		{"legacy pending status", runsJSON(0), statusJSON("deploy", "pending"), 0, StatePending, "deploy=pending"},
		{"nothing reported yet", runsJSON(0), statusJSON(), 0, StatePending, "no checks reported"},
		{"partial page is not healthy", runsJSON(150, run("a", "completed", "success")), statusJSON(), 0, StatePending, "first 1 of 150"},
		{"404 is unreachable", "", "", http.StatusNotFound, StateUnreachable, "GitHub API 404: Not Found"},
		{"500 is unreachable", "", "", http.StatusInternalServerError, StateUnreachable, "GitHub API 500"},
		{"bad json is unreachable", "{", statusJSON(), 0, StateUnreachable, "decode GitHub response"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeGitHub{runs: tc.runs, statuses: tc.statuses, code: tc.code}
			gh := newGitHub(t, f)
			state, detail := gh.Check(context.Background(), "acme/app", testSHA)
			if state != tc.wantState || !strings.Contains(detail, tc.wantDetail) {
				t.Fatalf("Check = (%q, %q), want (%q, ~%q)", state, detail, tc.wantState, tc.wantDetail)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.gotAuth) == 0 || f.gotAuth[0] != "Bearer tok" {
				t.Fatalf("auth headers = %q", f.gotAuth)
			}
			if f.gotAccept != "application/vnd.github+json" {
				t.Fatalf("accept = %q", f.gotAccept)
			}
		})
	}
}

func TestGitHubCheckWithoutTokenSendsNoAuth(t *testing.T) {
	f := &fakeGitHub{runs: runsJSON(0), statuses: statusJSON()}
	gh := newGitHub(t, f)
	gh.Token = func(context.Context) string { return "" }
	gh.Check(context.Background(), "acme/app", testSHA)
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, a := range f.gotAuth {
		if a != "" {
			t.Fatalf("unexpected auth header %q", a)
		}
	}
}

func TestGitHubCheckTransportErrorIsUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	base := srv.URL
	srv.Close()
	gh := &GitHub{BaseURL: base, Token: func(context.Context) string { return "" }}
	if state, _ := gh.Check(context.Background(), "acme/app", testSHA); state != StateUnreachable {
		t.Fatalf("state = %q, want unreachable", state)
	}
}

func TestDefaultTokenPrefersEnv(t *testing.T) {
	t.Setenv("GH_TOKEN", " gh-env ")
	t.Setenv("GITHUB_TOKEN", "github-env")
	if got := DefaultToken(context.Background()); got != "gh-env" {
		t.Fatalf("token = %q", got)
	}
	t.Setenv("GH_TOKEN", "")
	if got := DefaultToken(context.Background()); got != "github-env" {
		t.Fatalf("token = %q", got)
	}
}

func TestProbeHTTP(t *testing.T) {
	var mu sync.Mutex
	code := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.WriteHeader(code)
	}))
	defer srv.Close()
	url := srv.URL + "/health?token=s3cret"

	if state, detail := probeHTTP(context.Background(), srv.Client(), url, 0); state != StateHealthy || detail != "status 200" {
		t.Fatalf("healthy = %q %q", state, detail)
	}
	mu.Lock()
	code = http.StatusServiceUnavailable
	mu.Unlock()
	state, detail := probeHTTP(context.Background(), srv.Client(), url, 200)
	if state != StateFailing || !strings.Contains(detail, "status 503, want 200") {
		t.Fatalf("failing = %q %q", state, detail)
	}
	if state, _ := probeHTTP(context.Background(), srv.Client(), url, 503); state != StateHealthy {
		t.Fatalf("expect 503 = %q", state)
	}

	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL + "/health?token=s3cret"
	dead.Close()
	state, detail = probeHTTP(context.Background(), nil, deadURL, 200)
	if state != StateUnreachable {
		t.Fatalf("dead = %q %q", state, detail)
	}
	if strings.Contains(detail, "s3cret") {
		t.Fatalf("detail leaks URL: %q", detail)
	}
}

func newService(t *testing.T, f *fakeGitHub, c *clock) (*Service, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Now: c.Now}
	if f != nil {
		cfg.GitHub = newGitHub(t, f)
	}
	return New(st, cfg), dir
}

// target is a health endpoint a test can flip.
type target struct {
	mu   sync.Mutex
	code int
	srv  *httptest.Server
}

func newTarget(t *testing.T) *target {
	t.Helper()
	tg := &target{code: http.StatusOK}
	tg.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tg.mu.Lock()
		defer tg.mu.Unlock()
		w.WriteHeader(tg.code)
	}))
	t.Cleanup(tg.srv.Close)
	return tg
}

func (tg *target) set(code int) {
	tg.mu.Lock()
	tg.code = code
	tg.mu.Unlock()
}

func TestWatchFlipsToRegressionMidWindow(t *testing.T) {
	c := &clock{now: t0}
	f := &fakeGitHub{runs: runsJSON(1, run("test", "completed", "success")), statuses: statusJSON()}
	svc, _ := newService(t, f, c)
	tg := newTarget(t)
	w, err := svc.Create(Input{
		Repo: "acme/app", PR: 42, SHA: strings.ToUpper(testSHA), Environment: "prod",
		Checks:        []CheckSpec{{Kind: KindGitHubChecks}, {Kind: KindHTTP, Name: "api", URL: tg.srv.URL + "/health"}},
		WindowSeconds: 600, IntervalSeconds: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	if w.SHA != testSHA || w.Status != StatusWatching || !w.Deadline.Equal(t0.Add(10*time.Minute)) {
		t.Fatalf("created = %+v", w)
	}
	if w.Checks[0].Name != "GitHub checks" || w.Checks[1].ExpectStatus != 200 {
		t.Fatalf("defaults not applied: %+v", w.Checks)
	}

	c.Add(time.Minute)
	w, err = svc.Poll(context.Background(), w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if w.Status != StatusWatching || w.Verdict != "" || len(w.Rounds) != 1 {
		t.Fatalf("healthy mid-window should keep watching: %+v", w)
	}

	tg.set(http.StatusBadGateway)
	c.Add(time.Minute)
	w, err = svc.Poll(context.Background(), w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if w.Status != StatusDone || w.Verdict != VerdictRegression || !strings.Contains(w.Reason, "api") {
		t.Fatalf("want regression, got %+v", w)
	}
	if len(w.Rounds) != 2 || w.Latest[1].Detail != "status 502, want 200" {
		t.Fatalf("evidence = %+v", w.Latest)
	}

	// Finished watches are immutable to polls.
	tg.set(http.StatusOK)
	again, err := svc.Poll(context.Background(), w.ID)
	if err != nil || again.Verdict != VerdictRegression || len(again.Rounds) != 2 {
		t.Fatalf("poll after verdict = %+v %v", again, err)
	}
}

func TestWatchHealthyAtWindowClose(t *testing.T) {
	c := &clock{now: t0}
	f := &fakeGitHub{runs: runsJSON(0), statuses: statusJSON()}
	svc, _ := newService(t, f, c)
	w, err := svc.Create(Input{Repo: "acme/app", SHA: testSHA, Checks: []CheckSpec{{Kind: KindGitHubChecks, Name: "ci"}}, WindowSeconds: 120, IntervalSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	c.Add(time.Minute)
	w, _ = svc.Poll(context.Background(), w.ID)
	if w.Status != StatusWatching || w.Latest[0].State != StatePending {
		t.Fatalf("no checks yet should be pending: %+v", w)
	}
	f.set(runsJSON(1, run("test", "completed", "success")), statusJSON())
	c.Add(time.Minute)
	w, _ = svc.Poll(context.Background(), w.ID)
	if w.Status != StatusDone || w.Verdict != VerdictHealthy || w.FinishedAt.IsZero() {
		t.Fatalf("want healthy, got %+v", w)
	}
}

func TestWatchUnreachableTargetIsInconclusive(t *testing.T) {
	c := &clock{now: t0}
	svc, _ := newService(t, nil, c)
	dead := httptest.NewServer(http.NotFoundHandler())
	url := dead.URL + "/health"
	dead.Close()
	w, err := svc.Create(Input{Repo: "acme/app", SHA: testSHA, Checks: []CheckSpec{{Kind: KindHTTP, Name: "api", URL: url}}, WindowSeconds: 60, IntervalSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	c.Add(time.Minute)
	w, _ = svc.Poll(context.Background(), w.ID)
	if w.Verdict != VerdictInconclusive || !strings.Contains(w.Reason, "api (unreachable)") {
		t.Fatalf("want inconclusive, got %+v", w)
	}
}

func TestPollCancelledContextRecordsNothing(t *testing.T) {
	c := &clock{now: t0}
	tg := newTarget(t)
	svc, _ := newService(t, nil, c)
	w, _ := svc.Create(Input{Repo: "acme/app", SHA: testSHA, Checks: []CheckSpec{{Kind: KindHTTP, URL: tg.srv.URL}}, WindowSeconds: 60})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.Add(2 * time.Minute)
	if _, err := svc.Poll(ctx, w.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	got, _ := svc.Get(w.ID)
	if !got.Active() || len(got.Rounds) != 0 {
		t.Fatalf("cancelled poll recorded evidence: %+v", got)
	}
}

func TestCancel(t *testing.T) {
	c := &clock{now: t0}
	svc, _ := newService(t, nil, c)
	w, _ := svc.Create(Input{Repo: "acme/app", SHA: testSHA, Checks: []CheckSpec{{Kind: KindHTTP, URL: "http://127.0.0.1:1/"}}})
	got, err := svc.Cancel(w.ID)
	if err != nil || got.Status != StatusCancelled || got.Verdict != VerdictInconclusive {
		t.Fatalf("cancel = %+v %v", got, err)
	}
	again, err := svc.Cancel(w.ID)
	if err != nil || !again.FinishedAt.Equal(got.FinishedAt) {
		t.Fatalf("second cancel changed record: %+v %v", again, err)
	}
	if _, err := svc.Cancel("dw_missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing cancel err = %v", err)
	}
	if _, err := svc.Poll(context.Background(), "dw_missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing poll err = %v", err)
	}
}

func TestDueAndTick(t *testing.T) {
	c := &clock{now: t0}
	tg := newTarget(t)
	svc, _ := newService(t, nil, c)
	w, _ := svc.Create(Input{Repo: "acme/app", SHA: testSHA, Checks: []CheckSpec{{Kind: KindHTTP, URL: tg.srv.URL}}, WindowSeconds: 300, IntervalSeconds: 60})
	if due := svc.Due(c.Now()); len(due) != 1 || due[0] != w.ID {
		t.Fatalf("never-polled watch should be due: %v", due)
	}
	svc.Tick(context.Background())
	if due := svc.Due(c.Now()); len(due) != 0 {
		t.Fatalf("just-polled watch should not be due: %v", due)
	}
	c.Add(59 * time.Second)
	if due := svc.Due(c.Now()); len(due) != 0 {
		t.Fatalf("not due before interval: %v", due)
	}
	c.Add(time.Second)
	if due := svc.Due(c.Now()); len(due) != 1 {
		t.Fatalf("due at interval: %v", due)
	}
	// Daemon was down past the deadline: the next tick settles the verdict.
	c.Add(time.Hour)
	svc.Tick(context.Background())
	got, _ := svc.Get(w.ID)
	if got.Verdict != VerdictHealthy || len(got.Rounds) != 2 {
		t.Fatalf("after deadline tick = %+v", got)
	}
	if due := svc.Due(c.Now()); len(due) != 0 {
		t.Fatalf("finished watch is due: %v", due)
	}
}

func TestRunStopsWithContext(t *testing.T) {
	c := &clock{now: t0}
	svc, _ := newService(t, nil, c)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		svc.Run(ctx, time.Millisecond)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
}

func TestCreateValidation(t *testing.T) {
	ok := []CheckSpec{{Kind: KindGitHubChecks}}
	cases := []struct {
		name string
		in   Input
		want string
	}{
		{"bad repo", Input{Repo: "acme", SHA: testSHA, Checks: ok}, "repo"},
		{"repo traversal", Input{Repo: "../x", SHA: testSHA, Checks: ok}, "repo"},
		{"bad sha", Input{Repo: "acme/app", SHA: "main", Checks: ok}, "sha"},
		{"negative pr", Input{Repo: "acme/app", SHA: testSHA, PR: -1, Checks: ok}, "pr"},
		{"no checks", Input{Repo: "acme/app", SHA: testSHA}, "at least one check"},
		{"unknown kind", Input{Repo: "acme/app", SHA: testSHA, Checks: []CheckSpec{{Kind: "shell"}}}, "kind must be"},
		{"http without url", Input{Repo: "acme/app", SHA: testSHA, Checks: []CheckSpec{{Kind: KindHTTP}}}, "url is required"},
		{"http bad scheme", Input{Repo: "acme/app", SHA: testSHA, Checks: []CheckSpec{{Kind: KindHTTP, URL: "file:///etc/passwd"}}}, "absolute http"},
		{"http bad status", Input{Repo: "acme/app", SHA: testSHA, Checks: []CheckSpec{{Kind: KindHTTP, URL: "http://x/", ExpectStatus: 99}}}, "expectStatus"},
		{"github with url", Input{Repo: "acme/app", SHA: testSHA, Checks: []CheckSpec{{Kind: KindGitHubChecks, URL: "http://x/"}}}, "takes no url"},
		{"duplicate names", Input{Repo: "acme/app", SHA: testSHA, Checks: []CheckSpec{{Kind: KindGitHubChecks}, {Kind: KindGitHubChecks}}}, "duplicate"},
		{"window too short", Input{Repo: "acme/app", SHA: testSHA, Checks: ok, WindowSeconds: 5}, "windowSeconds"},
		{"window negative", Input{Repo: "acme/app", SHA: testSHA, Checks: ok, WindowSeconds: -60}, "windowSeconds"},
		{"interval too short", Input{Repo: "acme/app", SHA: testSHA, Checks: ok, IntervalSeconds: 1}, "intervalSeconds"},
		{"interval over window", Input{Repo: "acme/app", SHA: testSHA, Checks: ok, WindowSeconds: 60, IntervalSeconds: 120}, "must not exceed"},
		{"bad session", Input{Repo: "acme/app", SHA: testSHA, Checks: ok, SessionID: "../x y"}, "sessionId"},
	}
	svc, _ := newService(t, nil, &clock{now: t0})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.Create(tc.in)
			if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want ErrInvalid ~%q", err, tc.want)
			}
		})
	}
	if len(svc.List()) != 0 {
		t.Fatal("invalid input was stored")
	}
	w, err := svc.Create(Input{Repo: "acme/app", SHA: testSHA, Checks: ok})
	if err != nil {
		t.Fatal(err)
	}
	if w.WindowSeconds != 1800 || w.IntervalSeconds != 60 {
		t.Fatalf("defaults = %d/%d", w.WindowSeconds, w.IntervalSeconds)
	}
}

func TestStorePersistsWithPrivateMode(t *testing.T) {
	c := &clock{now: t0}
	svc, dir := newService(t, nil, c)
	a, _ := svc.Create(Input{Repo: "acme/app", SHA: testSHA, Checks: []CheckSpec{{Kind: KindGitHubChecks}}})
	c.Add(time.Second)
	b, _ := svc.Create(Input{Repo: "acme/web", SHA: testSHA, Checks: []CheckSpec{{Kind: KindGitHubChecks}}})
	info, err := os.Stat(filepath.Join(dir, "deploy-watches.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", info.Mode().Perm())
	}
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	list := st.List()
	if len(list) != 2 || list[0].ID != b.ID || list[1].ID != a.ID {
		t.Fatalf("reopened list = %+v", list)
	}
	got, err := st.Get(a.ID)
	if err != nil || got.Repo != "acme/app" || !got.Active() {
		t.Fatalf("get = %+v %v", got, err)
	}
}

func TestOpenRejectsCorruptFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "deploy-watches.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err == nil {
		t.Fatal("corrupt file opened")
	}
}

func TestStorePrunesOldestFinishedOnly(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	active := Watch{ID: "dw_active", Status: StatusWatching}
	if err := st.Put(active); err != nil {
		t.Fatal(err)
	}
	for i := range maxFinished + 5 {
		if err := st.Put(Watch{ID: fmt.Sprintf("dw_%04d", i), Status: StatusDone}); err != nil {
			t.Fatal(err)
		}
	}
	list := st.List()
	if len(list) != maxFinished+1 {
		t.Fatalf("len = %d", len(list))
	}
	if _, err := st.Get("dw_active"); err != nil {
		t.Fatal("active watch pruned")
	}
	if _, err := st.Get("dw_0000"); !errors.Is(err, ErrNotFound) {
		t.Fatal("oldest finished watch kept")
	}
}

func TestRoundsAreCapped(t *testing.T) {
	c := &clock{now: t0}
	tg := newTarget(t)
	svc, _ := newService(t, nil, c)
	w, _ := svc.Create(Input{Repo: "acme/app", SHA: testSHA, Checks: []CheckSpec{{Kind: KindHTTP, URL: tg.srv.URL}}, WindowSeconds: 86400, IntervalSeconds: 10})
	for range maxRounds + 3 {
		c.Add(10 * time.Second)
		if _, err := svc.Poll(context.Background(), w.ID); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := svc.Get(w.ID)
	if len(got.Rounds) != maxRounds {
		t.Fatalf("rounds = %d", len(got.Rounds))
	}
}
