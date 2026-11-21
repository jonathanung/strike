package deploywatch

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jonathanung/strike/internal/daemon/rootstart"
)

type startLaunch struct {
	mu   sync.Mutex
	reqs []rootstart.Request
	err  error
}

func (l *startLaunch) StartRoot(_ context.Context, req rootstart.Request) (rootstart.Result, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.reqs = append(l.reqs, req)
	if l.err != nil {
		return rootstart.Result{}, l.err
	}
	return rootstart.Result{SessionID: "fix-1", Created: true}, nil
}

// regressed returns a service holding one watch settled as regression.
func regressed(t *testing.T, cfg Config) (*Service, Watch) {
	t.Helper()
	c := &clock{now: t0}
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg.Now = c.Now
	svc := New(st, cfg)
	tg := newTarget(t)
	tg.set(http.StatusBadGateway)
	w, err := svc.Create(Input{Repo: "acme/app", PR: 42, SHA: testSHA, FailureThreshold: 1,
		Checks: []CheckSpec{{Kind: KindHTTP, Name: "api", URL: tg.srv.URL}}, WindowSeconds: 600})
	if err != nil {
		t.Fatal(err)
	}
	w, err = svc.Poll(context.Background(), w.ID)
	if err != nil || w.Verdict != VerdictRegression {
		t.Fatalf("setup verdict = %+v %v", w, err)
	}
	return svc, w
}

func TestOpenFixSession(t *testing.T) {
	l := &startLaunch{}
	svc, w := regressed(t, Config{Launcher: l})
	got, err := svc.OpenFixSession(context.Background(), w.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.FixSession == nil || got.FixSession.SessionID != "fix-1" || got.FixSession.At.IsZero() {
		t.Fatalf("fix session = %+v", got.FixSession)
	}
	if len(l.reqs) != 1 {
		t.Fatalf("requests = %d", len(l.reqs))
	}
	req := l.reqs[0]
	if req.ContinueID != "" || req.Source != rootstart.SourceDeployWatch || req.SourceID != w.ID || req.PermissionMode != "" {
		t.Fatalf("request = %+v", req)
	}
	for _, want := range []string{"regression", "status 502, want 200", "Do not merge", "acme/app"} {
		if !strings.Contains(req.Prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, req.Prompt)
		}
	}
	// Confirmed once: a repeat returns the recorded session, no second root.
	again, err := svc.OpenFixSession(context.Background(), w.ID, "")
	if err != nil || again.FixSession.SessionID != "fix-1" || len(l.reqs) != 1 {
		t.Fatalf("repeat = %+v %v requests=%d", again.FixSession, err, len(l.reqs))
	}
	if persisted, _ := svc.Get(w.ID); persisted.FixSession == nil {
		t.Fatal("fix session not persisted")
	}
}

func TestHandoffRefusals(t *testing.T) {
	l := &startLaunch{}
	svc, w := regressed(t, Config{Launcher: l})
	if _, err := svc.OpenFixSession(context.Background(), w.ID, "nonsense"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad mode err = %v", err)
	}
	if _, err := svc.OpenFixSession(context.Background(), "dw_missing", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing err = %v", err)
	}
	healthy, _ := svc.Create(Input{Repo: "acme/app", SHA: testSHA, Checks: []CheckSpec{{Kind: KindGitHubChecks}}})
	if _, err := svc.OpenFixSession(context.Background(), healthy.ID, ""); !errors.Is(err, ErrNotRegression) {
		t.Fatalf("watching err = %v", err)
	}
	cancelled, _ := svc.Cancel(healthy.ID)
	if _, err := svc.DraftRevertPR(context.Background(), cancelled.ID); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("no reverter err = %v", err)
	}
	l.err = errors.New("hub down")
	if _, err := svc.OpenFixSession(context.Background(), w.ID, ""); err == nil {
		t.Fatal("launcher failure should surface")
	}
	if got, _ := svc.Get(w.ID); got.FixSession != nil {
		t.Fatal("failed start recorded a session")
	}
	bare, w2 := regressed(t, Config{})
	if _, err := bare.OpenFixSession(context.Background(), w2.ID, ""); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("no launcher err = %v", err)
	}
}

func TestRemoteIs(t *testing.T) {
	cases := []struct {
		url  string
		want bool
	}{
		{"https://github.com/acme/app.git", true},
		{"https://github.com/Acme/App", true},
		{"git@github.com:acme/app.git", true},
		{"ssh://git@github.com/acme/app.git", true},
		{"/srv/git/acme/app.git", true},
		{"https://github.com/acme/app-fork.git", false},
		{"https://github.com/evil/acme/app2", false},
		{"https://github.com/other/app.git", false},
	}
	for _, tc := range cases {
		if got := remoteIs(tc.url, "acme/app"); got != tc.want {
			t.Errorf("remoteIs(%q) = %v, want %v", tc.url, got, tc.want)
		}
	}
}

// gitFixture is a bare "origin" at <dir>/acme/app.git and a clone with two
// commits on main; bad is the second commit, the one to revert.
type gitFixture struct {
	clone string
	bare  string
	bad   string
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func newGitFixture(t *testing.T) gitFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	bare := filepath.Join(root, "acme", "app.git")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, bare, "init", "--quiet", "--bare", "--initial-branch=main")
	clone := filepath.Join(root, "clone")
	runGit(t, root, "clone", "--quiet", bare, clone)
	runGit(t, clone, "symbolic-ref", "HEAD", "refs/heads/main")
	write := func(body string) {
		if err := os.WriteFile(filepath.Join(clone, "app.txt"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("good\n")
	runGit(t, clone, "add", "app.txt")
	runGit(t, clone, "commit", "--quiet", "-m", "good")
	write("bad\n")
	runGit(t, clone, "commit", "--quiet", "-am", "bad change")
	bad := runGit(t, clone, "rev-parse", "HEAD")
	runGit(t, clone, "push", "--quiet", "origin", "main")
	runGit(t, bare, "symbolic-ref", "HEAD", "refs/heads/main")
	return gitFixture{clone: clone, bare: bare, bad: bad}
}

type fakePulls struct {
	mu    sync.Mutex
	calls []PRInput
	code  int
}

func (f *fakePulls) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Method != http.MethodPost || r.URL.Path != "/repos/acme/app/pulls" {
		http.NotFound(w, r)
		return
	}
	var in PRInput
	b, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(b, &in)
	f.calls = append(f.calls, in)
	if f.code != 0 {
		w.WriteHeader(f.code)
		_, _ = w.Write([]byte(`{"message":"Validation Failed"}`))
		return
	}
	w.WriteHeader(http.StatusCreated)
	_, _ = w.Write([]byte(`{"number":77,"html_url":"https://github.com/acme/app/pull/77"}`))
}

func TestDraftRevertPR(t *testing.T) {
	fx := newGitFixture(t)
	pulls := &fakePulls{}
	srv := httptest.NewServer(pulls)
	defer srv.Close()
	gh := &GitHub{BaseURL: srv.URL, Token: func(context.Context) string { return "tok" }}
	svc, _ := regressed(t, Config{Revert: &Reverter{RepoDir: fx.clone, GitHub: gh}})
	w, err := svc.Create(Input{Repo: "acme/app", PR: 42, SHA: fx.bad, FailureThreshold: 1,
		Checks: []CheckSpec{{Kind: KindGitHubChecks}}, WindowSeconds: 600})
	if err != nil {
		t.Fatal(err)
	}
	if w, err = svc.Cancel(w.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.DraftRevertPR(context.Background(), w.ID); !errors.Is(err, ErrNotRegression) {
		t.Fatalf("cancelled watch err = %v", err)
	}
	// Force a regression verdict on this watch for the handoff.
	_, _ = svc.recordHandoff(w.ID, func(w *Watch) { w.Verdict = VerdictRegression })

	got, err := svc.DraftRevertPR(context.Background(), w.ID)
	if err != nil {
		t.Fatal(err)
	}
	branch := "strike/revert-" + fx.bad[:12]
	if got.RevertPR == nil || got.RevertPR.PR != 77 || got.RevertPR.URL != "https://github.com/acme/app/pull/77" || got.RevertPR.Branch != branch {
		t.Fatalf("revert = %+v", got.RevertPR)
	}
	if len(pulls.calls) != 1 {
		t.Fatalf("pull calls = %d", len(pulls.calls))
	}
	call := pulls.calls[0]
	if !call.Draft || call.Base != "main" || call.Head != branch || call.Title != "Revert #42 (deploy watch regression)" || !strings.Contains(call.Body, "does not merge") {
		t.Fatalf("pull input = %+v", call)
	}
	// The pushed branch reverts the bad commit; main is untouched.
	content := runGit(t, fx.bare, "show", branch+":app.txt")
	if content != "good" {
		t.Fatalf("reverted content = %q", content)
	}
	if head := runGit(t, fx.bare, "rev-parse", "main"); head != fx.bad {
		t.Fatalf("main moved to %s", head)
	}
	if list := runGit(t, fx.clone, "worktree", "list"); strings.Count(list, "\n") != 0 {
		t.Fatalf("temporary worktree left behind:\n%s", list)
	}
	// Once per watch.
	if again, err := svc.DraftRevertPR(context.Background(), w.ID); err != nil || again.RevertPR.PR != 77 || len(pulls.calls) != 1 {
		t.Fatalf("repeat = %+v %v calls=%d", again.RevertPR, err, len(pulls.calls))
	}
}

func TestDraftRevertPRFailures(t *testing.T) {
	fx := newGitFixture(t)
	pulls := &fakePulls{code: http.StatusUnprocessableEntity}
	srv := httptest.NewServer(pulls)
	defer srv.Close()
	gh := &GitHub{BaseURL: srv.URL, Token: func(context.Context) string { return "tok" }}
	r := &Reverter{RepoDir: fx.clone, GitHub: gh}
	w := Watch{ID: "dw_x", Repo: "acme/app", SHA: fx.bad, Verdict: VerdictRegression}

	// PR creation fails after the push: the branch is kept and reused.
	if _, err := r.Draft(context.Background(), w); err == nil || !strings.Contains(err.Error(), "422") {
		t.Fatalf("pr failure err = %v", err)
	}
	pulls.mu.Lock()
	pulls.code = 0
	pulls.mu.Unlock()
	h, err := r.Draft(context.Background(), w)
	if err != nil || h.PR != 77 {
		t.Fatalf("retry = %+v %v", h, err)
	}

	other := w
	other.Repo = "other/app"
	if _, err := r.Draft(context.Background(), other); err == nil || !strings.Contains(err.Error(), "not a clone of other/app") {
		t.Fatalf("wrong repo err = %v", err)
	}
	notOnMain := w
	notOnMain.SHA = strings.Repeat("a", 40)
	if _, err := r.Draft(context.Background(), notOnMain); err == nil || !strings.Contains(err.Error(), "is not on origin/main") {
		t.Fatalf("unknown commit err = %v", err)
	}
	if _, err := (&Reverter{RepoDir: fx.clone}).Draft(context.Background(), w); err == nil {
		t.Fatal("no GitHub should fail")
	}
}
