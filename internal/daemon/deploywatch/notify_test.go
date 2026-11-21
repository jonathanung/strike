package deploywatch

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jonathanung/strike/harness/sandbox"
	"github.com/jonathanung/strike/internal/daemon/rootstart"
)

type prPost struct {
	repo string
	pr   int
	body string
}

type sessionPost struct {
	id, mode, text string
}

type fakeNotifier struct {
	mu       sync.Mutex
	prs      []prPost
	sessions []sessionPost
	prErr    error
}

func (f *fakeNotifier) PostPR(_ context.Context, repo string, pr int, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prs = append(f.prs, prPost{repo, pr, body})
	return f.prErr
}

func (f *fakeNotifier) PostSession(_ context.Context, id, mode, _, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions = append(f.sessions, sessionPost{id, mode, text})
	return nil
}

func newNotifyService(t *testing.T, c *clock, n Notifier) *Service {
	t.Helper()
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return New(st, Config{Now: c.Now, Notify: n})
}

func TestStartedPostsPlanThenVerdict(t *testing.T) {
	c := &clock{now: t0}
	n := &fakeNotifier{}
	svc := newNotifyService(t, c, n)
	tg := newTarget(t)
	w, err := svc.Create(Input{
		Repo: "acme/app", PR: 42, SHA: testSHA, Environment: "prod",
		Checks:        []CheckSpec{{Kind: KindHTTP, Name: "api", URL: tg.srv.URL + "/health?token=s3cr3t"}},
		WindowSeconds: 600, IntervalSeconds: 60, FailureThreshold: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.Started(context.Background(), w.ID, "sess-9", "plan")
	if err != nil {
		t.Fatal(err)
	}
	if got.SessionID != "sess-9" || got.SessionMode != "plan" || got.NotifyError != "" {
		t.Fatalf("started = %+v", got)
	}
	if len(n.prs) != 1 || n.prs[0].repo != "acme/app" || n.prs[0].pr != 42 {
		t.Fatalf("plan posts = %+v", n.prs)
	}
	plan := n.prs[0].body
	for _, want := range []string{"monitoring plan", "**api** (http)", "2 consecutive failures", "Instrumentation gaps", "Post-merge CI on the commit is not observed"} {
		if !strings.Contains(plan, want) {
			t.Fatalf("plan missing %q:\n%s", want, plan)
		}
	}
	if strings.Contains(plan, "s3cr3t") || strings.Contains(plan, "/health") {
		t.Fatalf("plan echoes the probe URL:\n%s", plan)
	}
	if len(n.sessions) != 0 {
		t.Fatalf("plan should not be re-posted to the session (it is the first prompt): %+v", n.sessions)
	}

	tg.set(http.StatusInternalServerError)
	for range 2 {
		c.Add(time.Minute)
		if w, err = svc.Poll(context.Background(), w.ID); err != nil {
			t.Fatal(err)
		}
	}
	if w.Verdict != VerdictRegression {
		t.Fatalf("verdict = %+v", w)
	}
	if len(n.prs) != 2 || !strings.Contains(n.prs[1].body, "verdict: **regression**") || !strings.Contains(n.prs[1].body, "status 500, want 200") {
		t.Fatalf("verdict PR posts = %+v", n.prs)
	}
	if len(n.sessions) != 1 || n.sessions[0].id != "sess-9" || n.sessions[0].mode != "plan" || !strings.Contains(n.sessions[0].text, "regression") {
		t.Fatalf("verdict session posts = %+v", n.sessions)
	}
	// Exactly one verdict: later polls and cancels post nothing more.
	_, _ = svc.Poll(context.Background(), w.ID)
	_, _ = svc.Cancel(w.ID)
	if len(n.prs) != 2 || len(n.sessions) != 1 {
		t.Fatalf("verdict posted twice: prs=%d sessions=%d", len(n.prs), len(n.sessions))
	}
}

func TestStartedAfterSettlePostsVerdictAndRecordsErrors(t *testing.T) {
	c := &clock{now: t0}
	n := &fakeNotifier{prErr: errors.New("GitHub API 403: Resource not accessible")}
	svc := newNotifyService(t, c, n)
	w, _ := svc.Create(Input{Repo: "acme/app", PR: 7, SHA: testSHA, Checks: []CheckSpec{{Kind: KindGitHubChecks}}})
	cancelled, err := svc.Cancel(w.ID)
	if err != nil || cancelled.Verdict != VerdictInconclusive {
		t.Fatalf("cancel = %+v %v", cancelled, err)
	}
	if !strings.Contains(cancelled.NotifyError, "PR comment") {
		t.Fatalf("notify error not recorded: %+v", cancelled)
	}
	got, err := svc.Started(context.Background(), w.ID, "sess-2", "plan")
	if err != nil {
		t.Fatal(err)
	}
	// Plan + verdict to the PR (both failing), verdict to the session.
	if len(n.prs) != 3 || len(n.sessions) != 1 || !strings.Contains(n.sessions[0].text, "inconclusive") {
		t.Fatalf("posts prs=%d sessions=%+v", len(n.prs), n.sessions)
	}
	if !strings.Contains(got.NotifyError, "403") {
		t.Fatalf("notify error = %q", got.NotifyError)
	}
	n.mu.Lock()
	n.prErr = nil
	n.mu.Unlock()
	if _, err := svc.Started(context.Background(), w.ID, "sess-2", "plan"); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.Get(w.ID); got.NotifyError != "" {
		t.Fatalf("notify error not cleared: %q", got.NotifyError)
	}
	if _, err := svc.Started(context.Background(), w.ID, "bad id", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("malformed session err = %v", err)
	}
	if _, err := svc.Started(context.Background(), "dw_missing", "s", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing watch err = %v", err)
	}
}

func TestCreateSessionWithoutPostureGetsNoVerdict(t *testing.T) {
	c := &clock{now: t0}
	n := &fakeNotifier{}
	svc := newNotifyService(t, c, n)
	w, err := svc.Create(Input{Repo: "acme/app", SHA: testSHA, SessionID: "someone-elses", Checks: []CheckSpec{{Kind: KindGitHubChecks}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Cancel(w.ID); err != nil {
		t.Fatal(err)
	}
	if len(n.sessions) != 0 {
		t.Fatalf("verdict posted to an unlinked session: %+v", n.sessions)
	}
}

func TestNoPRPostsSessionOnly(t *testing.T) {
	c := &clock{now: t0}
	n := &fakeNotifier{}
	svc := newNotifyService(t, c, n)
	w, _ := svc.Create(Input{Repo: "acme/app", SHA: testSHA, Checks: []CheckSpec{{Kind: KindGitHubChecks}}})
	if _, err := svc.Started(context.Background(), w.ID, "sess-3", "plan"); err != nil {
		t.Fatal(err)
	}
	_, _ = svc.Cancel(w.ID)
	if len(n.prs) != 0 || len(n.sessions) != 1 {
		t.Fatalf("prs=%+v sessions=%+v", n.prs, n.sessions)
	}
}

func TestPlanTextAndGaps(t *testing.T) {
	w := Watch{
		ID: "dw_1", Repo: "acme/app", SHA: testSHA, Environment: "@team prod",
		Checks:        []CheckSpec{{Kind: KindGitHubChecks, Name: "ci"}},
		WindowSeconds: 1800, IntervalSeconds: 60,
	}
	plan := PlanText(w)
	for _, want := range []string{"30m0s", "every 1m0s", "3 consecutive", "No runtime probe", "No PR is linked", "@\u200bteam"} {
		if !strings.Contains(plan, want) {
			t.Fatalf("plan missing %q:\n%s", want, plan)
		}
	}
	if strings.Contains(plan, "@team") {
		t.Fatal("environment text can mention a GitHub user")
	}
	w.PR = 9
	w.Checks = append(w.Checks, CheckSpec{Kind: KindShell, Name: "smoke", Command: "curl -fsS localhost"})
	gaps := strings.Join(Gaps(w), "\n")
	if strings.Contains(gaps, "No runtime probe") || strings.Contains(gaps, "not observed") || strings.Contains(gaps, "No PR") {
		t.Fatalf("gaps = %s", gaps)
	}
	prompt := PlanPrompt(w)
	for _, want := range []string{"dw_1", "gh pr diff 9 --repo acme/app", "Risks, Intended effect, Signals, and Instrumentation gaps", "Do not edit code"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(PlanText(w), "curl") {
		t.Fatal("plan echoes the shell command")
	}
}

func TestVerdictTextEscapesEvidence(t *testing.T) {
	w := Watch{ID: "dw_2", Repo: "acme/app", SHA: testSHA, Verdict: VerdictInconclusive, Reason: "could not confirm: api (unreachable)",
		Latest: []CheckResult{{Name: "api", State: StateUnreachable, Detail: "a | b\nc"}}, Rounds: make([]Round, 4)}
	text := VerdictText(w)
	for _, want := range []string{"verdict: **inconclusive**", "a \\| b c", "4 observation rounds", "not a pass"} {
		if !strings.Contains(text, want) {
			t.Fatalf("verdict missing %q:\n%s", want, text)
		}
	}
}

func TestGitHubComment(t *testing.T) {
	var gotPath, gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":1}`))
	}))
	defer srv.Close()
	g := &GitHub{BaseURL: srv.URL, Token: func(context.Context) string { return "tok" }}
	if err := g.Comment(context.Background(), "acme/app", 42, "hello"); err != nil {
		t.Fatal(err)
	}
	var payload map[string]string
	_ = json.Unmarshal([]byte(gotBody), &payload)
	if gotPath != "/repos/acme/app/issues/42/comments" || gotAuth != "Bearer tok" || payload["body"] != "hello" {
		t.Fatalf("path=%q auth=%q body=%q", gotPath, gotAuth, gotBody)
	}
	noTok := &GitHub{BaseURL: srv.URL, Token: func(context.Context) string { return "" }}
	if err := noTok.Comment(context.Background(), "acme/app", 42, "x"); err == nil {
		t.Fatal("comment without a token should fail")
	}
}

type recLaunch struct {
	reqs []rootstart.Request
}

func (r *recLaunch) StartRoot(_ context.Context, req rootstart.Request) (rootstart.Result, error) {
	r.reqs = append(r.reqs, req)
	return rootstart.Result{SessionID: req.ContinueID}, nil
}

func TestPosterContinuesSessionOnly(t *testing.T) {
	l := &recLaunch{}
	p := Poster{Launcher: l}
	if err := p.PostSession(context.Background(), "sess-1", "plan", "", "verdict"); err != nil {
		t.Fatal(err)
	}
	if len(l.reqs) != 1 || l.reqs[0].ContinueID != "sess-1" || l.reqs[0].PermissionMode != "plan" || l.reqs[0].Source != rootstart.SourceDeployWatch {
		t.Fatalf("requests = %+v", l.reqs)
	}
	if err := p.PostSession(context.Background(), "", "", "", "x"); err == nil || len(l.reqs) != 1 {
		t.Fatal("empty session must not start a new root")
	}
	if err := (Poster{}).PostPR(context.Background(), "a/b", 1, "x"); err == nil {
		t.Fatal("PostPR without GitHub should fail")
	}
}

func TestShellRun(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not installed")
	}
	sh := &Shell{Sandbox: sandbox.Policy{Mode: sandbox.ModeOff}, WorkDir: t.TempDir(), Timeout: 5 * time.Second}
	cases := []struct {
		name, cmd, state, detail string
	}{
		{"exit 0 is healthy", "echo ready", StateHealthy, "exit 0: ready"},
		{"non-zero is failing", "echo first; echo 'db down' >&2; exit 3", StateFailing, "exit 3: db down"},
		{"runs in workdir", "test -d .", StateHealthy, "exit 0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state, detail := sh.Run(context.Background(), tc.cmd)
			if state != tc.state || detail != tc.detail {
				t.Fatalf("Run = (%q, %q), want (%q, %q)", state, detail, tc.state, tc.detail)
			}
		})
	}
	slow := &Shell{WorkDir: t.TempDir(), Timeout: 100 * time.Millisecond}
	if state, detail := slow.Run(context.Background(), "sleep 5"); state != StateUnreachable || !strings.Contains(detail, "timed out") {
		t.Fatalf("timeout = (%q, %q)", state, detail)
	}
}

func TestShellCheckThroughService(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not installed")
	}
	c := &clock{now: t0}
	st, _ := Open(t.TempDir())
	dir := t.TempDir()
	svc := New(st, Config{Now: c.Now, Shell: &Shell{WorkDir: dir, Timeout: 5 * time.Second}})
	w, err := svc.Create(Input{Repo: "acme/app", SHA: testSHA, FailureThreshold: 1,
		Checks: []CheckSpec{{Kind: KindShell, Command: "test -f healthy"}}, WindowSeconds: 600})
	if err != nil {
		t.Fatal(err)
	}
	if w.Checks[0].Name != "Shell check" {
		t.Fatalf("default name = %q", w.Checks[0].Name)
	}
	w, _ = svc.Poll(context.Background(), w.ID)
	if w.Verdict != VerdictRegression || w.Latest[0].State != StateFailing {
		t.Fatalf("missing file should fail: %+v", w)
	}

	noShell, _ := newService(t, nil, c)
	w2, _ := noShell.Create(Input{Repo: "acme/app", SHA: testSHA, Checks: []CheckSpec{{Kind: KindShell, Command: "true"}}, WindowSeconds: 60})
	w2, _ = noShell.Poll(context.Background(), w2.ID)
	if w2.Latest[0].State != StateUnreachable {
		t.Fatalf("no shell runner should be unreachable: %+v", w2.Latest)
	}
}

func TestPresetAndDeployEvent(t *testing.T) {
	p, err := Preset{Repo: " acme/app ", Environment: "prod", Checks: []CheckSpec{{Kind: KindShell, Command: " ./smoke.sh "}}}.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if p.Repo != "acme/app" || p.Checks[0].Name != "Shell check" || p.Checks[0].Command != "./smoke.sh" {
		t.Fatalf("normalized = %+v", p)
	}
	if _, err := (Preset{}).Normalize(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty preset err = %v", err)
	}
	if _, err := (Preset{Repo: "nope", Checks: p.Checks}).Normalize(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad repo err = %v", err)
	}

	cases := []struct {
		name    string
		body    string
		wantErr string
		repo    string
		env     string
	}{
		{"uses preset repo and env", `{"sha":"abc1234","pr":5}`, "", "acme/app", "prod"},
		{"event env wins", `{"sha":"abc1234","environment":"staging"}`, "", "acme/app", "staging"},
		{"same repo any case", `{"repo":"Acme/App","sha":"abc1234"}`, "", "Acme/App", "prod"},
		{"other repo refused", `{"repo":"evil/app","sha":"abc1234"}`, "does not match", "", ""},
		{"missing sha", `{"repo":"acme/app"}`, "needs sha", "", ""},
		{"not json", `sha=abc`, "JSON object", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev, err := ParseDeployEvent([]byte(tc.body))
			var in Input
			if err == nil {
				in, err = p.Input(ev)
			}
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want ~%q", err, tc.wantErr)
				}
				return
			}
			if err != nil || in.Repo != tc.repo || in.Environment != tc.env || len(in.Checks) != 1 {
				t.Fatalf("input = %+v %v", in, err)
			}
		})
	}
	// Body-supplied checks are ignored: checks only come from the preset.
	ev, _ := ParseDeployEvent([]byte(`{"sha":"abc1234","checks":[{"kind":"shell","command":"rm -rf ~"}]}`))
	in, _ := p.Input(ev)
	if len(in.Checks) != 1 || in.Checks[0].Command != "./smoke.sh" {
		t.Fatalf("event injected checks: %+v", in.Checks)
	}
	if _, err := (Preset{Checks: p.Checks}).Input(DeployEvent{SHA: "abc1234"}); err == nil || !strings.Contains(err.Error(), "needs repo") {
		t.Fatalf("no repo anywhere err = %v", err)
	}
}
