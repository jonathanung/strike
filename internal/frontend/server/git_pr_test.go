package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonathanung/strike/internal/persist/session"
	"github.com/jonathanung/strike/internal/protocol"
)

const shipFakeGH = `#!/bin/sh
printf '%s\n' "$*" >> "$GH_FAKE_LOG"
case "$1:$2" in
  pr:list)
    if [ "$GH_FAKE_MODE" = unauth ]; then
      echo "To get started with GitHub CLI, please run:  gh auth login ($GH_TOKEN)" >&2
      exit 4
    fi
    if [ "$GH_FAKE_MODE" = existing ]; then
      printf '%s\n' '[{"number":12,"url":"https://github.com/acme/widgets/pull/12","state":"OPEN","isDraft":true}]'
    else
      printf '[]\n'
    fi
    ;;
  pr:create)
    cat > "$GH_FAKE_STDIN"
    printf '%s\n' "https://github.com/acme/widgets/pull/77"
    ;;
  *)
    echo "unexpected gh $*" >&2
    exit 2
    ;;
esac
`

func shipFakeToken() string { return "ghp_" + strings.Repeat("Q7", 18) }

type shipEnv struct {
	repo, bare, sessions, ghLog, ghStdin string
	srv                                  *Server
}

func (e *shipEnv) git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (e *shipEnv) hasRemoteBranch(branch string) bool {
	cmd := exec.Command("git", "-C", e.bare, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	return cmd.Run() == nil
}

func (e *shipEnv) ghCalls(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(e.ghLog)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return string(data)
}

func (e *shipEnv) call(t *testing.T, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	res := httptest.NewRecorder()
	e.srv.Handler().ServeHTTP(res, req)
	return res
}

// newShipEnv builds a repo on feature/ship (one commit ahead of main) whose
// origin is a GitHub URL rewritten (insteadOf) to a local bare repo, a
// session "root1", a live bridge on the repo, and a fake gh.
func newShipEnv(t *testing.T, opts Options) *shipEnv {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	e := &shipEnv{repo: t.TempDir(), bare: t.TempDir(), sessions: t.TempDir()}
	e.git(t, e.bare, "init", "--bare", "-b", "main")
	e.git(t, e.repo, "init", "-b", "main")
	e.git(t, e.repo, "config", "user.email", "test@example.com")
	e.git(t, e.repo, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(e.repo, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.git(t, e.repo, "add", "a.txt")
	e.git(t, e.repo, "commit", "-m", "initial")
	e.git(t, e.repo, "remote", "add", "origin", "https://github.com/acme/widgets.git")
	e.git(t, e.repo, "config", "url."+e.bare+".insteadOf", "https://github.com/acme/widgets.git")
	e.git(t, e.repo, "push", "origin", "main")
	e.git(t, e.repo, "checkout", "-b", "feature/ship")
	if err := os.WriteFile(filepath.Join(e.repo, "b.txt"), []byte("two\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.git(t, e.repo, "add", "b.txt")
	e.git(t, e.repo, "commit", "-m", "Add widget export")

	ghDir := t.TempDir()
	e.ghLog = filepath.Join(ghDir, "gh.log")
	e.ghStdin = filepath.Join(ghDir, "stdin")
	script := filepath.Join(ghDir, "gh")
	if err := os.WriteFile(script, []byte(shipFakeGH), 0o755); err != nil {
		t.Fatal(err)
	}
	prev := ghBin
	ghBin = script
	t.Cleanup(func() { ghBin = prev })
	t.Setenv("GH_FAKE_LOG", e.ghLog)
	t.Setenv("GH_FAKE_STDIN", e.ghStdin)
	t.Setenv("GH_FAKE_MODE", "")
	t.Setenv("GH_TOKEN", shipFakeToken())

	mgr := session.NewManager(e.sessions)
	if _, err := mgr.Create(session.CreateOptions{ID: "root1", Title: "Widget export"}); err != nil {
		t.Fatal(err)
	}
	opts.SessionDir = e.sessions
	opts.Live = NewLive("root1", e.repo, nil, make(chan protocol.Op, 1))
	srv, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	e.srv = srv
	return e
}

func decodePreview(t *testing.T, res *httptest.ResponseRecorder) shipPRPreview {
	t.Helper()
	if res.Code != http.StatusOK {
		t.Fatalf("preview status = %d: %s", res.Code, res.Body.String())
	}
	var p shipPRPreview
	if err := json.Unmarshal(res.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestShipPRPreviewOnFeatureBranch(t *testing.T) {
	e := newShipEnv(t, Options{})
	p := decodePreview(t, e.call(t, http.MethodGet, "/v1/git/pr", ""))
	if p.State != shipPRStateOK || len(p.Blockers) != 0 {
		t.Fatalf("preview = %+v", p)
	}
	if p.Branch != "feature/ship" || p.Base != "main" || p.Repo != "acme/widgets" || p.CommitsTotal != 1 || !p.NeedsPush {
		t.Fatalf("preview = %+v", p)
	}
	if p.Files != 1 || p.Added != 2 || p.Deleted != 0 || p.Dirty != 0 {
		t.Fatalf("diffstat = files %d +%d -%d dirty %d", p.Files, p.Added, p.Deleted, p.Dirty)
	}
	if p.Title != "Add widget export" {
		t.Fatalf("title = %q", p.Title)
	}
	for _, want := range []string{"## Summary", "Widget export", "Add widget export (", "## Test notes"} {
		if !strings.Contains(p.Body, want) {
			t.Fatalf("body missing %q:\n%s", want, p.Body)
		}
	}
	if !strings.Contains(e.ghCalls(t), "pr list --repo acme/widgets --head feature/ship --state open") {
		t.Fatalf("gh calls = %s", e.ghCalls(t))
	}
}

func TestShipPRRefusesDefaultBranch(t *testing.T) {
	e := newShipEnv(t, Options{})
	e.git(t, e.repo, "checkout", "main")
	if err := os.WriteFile(filepath.Join(e.repo, "c.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.git(t, e.repo, "add", "c.txt")
	e.git(t, e.repo, "commit", "-m", "on main")
	p := decodePreview(t, e.call(t, http.MethodGet, "/v1/git/pr", ""))
	if p.State != shipPRStateBlocked || !strings.Contains(strings.Join(p.Blockers, "\n"), "never pushes the default branch") {
		t.Fatalf("preview = %+v", p)
	}
	// Even naming another base, main itself is never pushed.
	p = decodePreview(t, e.call(t, http.MethodGet, "/v1/git/pr?base=feature/ship", ""))
	if p.State != shipPRStateBlocked || !strings.Contains(strings.Join(p.Blockers, "\n"), "never pushes main") {
		t.Fatalf("preview with other base = %+v", p)
	}
	before := e.git(t, e.bare, "rev-parse", "refs/heads/main")
	res := e.call(t, http.MethodPost, "/v1/git/pr", `{"title":"x","body":"y","confirmPush":true,"allowDirty":true}`)
	if res.Code != http.StatusConflict {
		t.Fatalf("create on main = %d: %s", res.Code, res.Body.String())
	}
	if after := e.git(t, e.bare, "rev-parse", "refs/heads/main"); after != before {
		t.Fatal("main was pushed")
	}
	if strings.Contains(e.ghCalls(t), "pr create") {
		t.Fatalf("gh pr create ran: %s", e.ghCalls(t))
	}
}

func TestShipPRRequiresExplicitConfirmations(t *testing.T) {
	e := newShipEnv(t, Options{})
	res := e.call(t, http.MethodPost, "/v1/git/pr", `{"title":"Add widget export","body":"b"}`)
	if res.Code != http.StatusConflict || !strings.Contains(res.Body.String(), "push-unconfirmed") {
		t.Fatalf("unconfirmed push = %d: %s", res.Code, res.Body.String())
	}
	if e.hasRemoteBranch("feature/ship") {
		t.Fatal("branch pushed without confirmation")
	}
	if err := os.WriteFile(filepath.Join(e.repo, "wip.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := decodePreview(t, e.call(t, http.MethodGet, "/v1/git/pr", ""))
	if p.Dirty != 1 || len(p.Warnings) != 1 || p.State != shipPRStateOK {
		t.Fatalf("dirty preview = %+v", p)
	}
	res = e.call(t, http.MethodPost, "/v1/git/pr", `{"title":"Add widget export","body":"b","confirmPush":true}`)
	if res.Code != http.StatusConflict || !strings.Contains(res.Body.String(), `"code":"dirty"`) {
		t.Fatalf("dirty create = %d: %s", res.Code, res.Body.String())
	}
	if e.hasRemoteBranch("feature/ship") || strings.Contains(e.ghCalls(t), "pr create") {
		t.Fatal("dirty create pushed or created")
	}
	for _, bad := range []string{`{"title":"","confirmPush":true}`, `{"title":"` + strings.Repeat("t", 300) + `","confirmPush":true}`, `{"title":"t","force":true}`} {
		if res := e.call(t, http.MethodPost, "/v1/git/pr", bad); res.Code != http.StatusBadRequest {
			t.Errorf("bad body %s = %d", bad[:20], res.Code)
		}
	}
}

func TestShipPRPushesAndCreatesDraft(t *testing.T) {
	e := newShipEnv(t, Options{})
	res := e.call(t, http.MethodPost, "/v1/git/pr", `{"title":"Add widget export","body":"## Summary\n- export","confirmPush":true}`)
	if res.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", res.Code, res.Body.String())
	}
	var out shipPRCreateResult
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.URL != "https://github.com/acme/widgets/pull/77" || out.Number != 77 || !out.Draft || !out.Pushed || out.Existing {
		t.Fatalf("result = %+v", out)
	}
	if !e.hasRemoteBranch("feature/ship") {
		t.Fatal("branch was not pushed")
	}
	if e.git(t, e.bare, "rev-parse", "refs/heads/feature/ship") != e.git(t, e.repo, "rev-parse", "HEAD") {
		t.Fatal("pushed ref is not HEAD")
	}
	calls := e.ghCalls(t)
	if !strings.Contains(calls, "pr create --repo acme/widgets --base main --head feature/ship --title Add widget export --body-file - --draft") {
		t.Fatalf("gh calls = %s", calls)
	}
	stdin, err := os.ReadFile(e.ghStdin)
	if err != nil || !strings.Contains(string(stdin), "- export") {
		t.Fatalf("gh stdin = %q %v", stdin, err)
	}
	meta, err := session.ReadMeta(e.sessions, "root1")
	if err != nil {
		t.Fatal(err)
	}
	if meta.PRURL != out.URL || meta.PRNumber != 77 || meta.PRState != session.PRStateOpen {
		t.Fatalf("session meta = %+v", meta)
	}
	if strings.Contains(res.Body.String(), shipFakeToken()) {
		t.Fatal("token in response")
	}
	// Re-running preview after the push needs no push.
	if p := decodePreview(t, e.call(t, http.MethodGet, "/v1/git/pr", "")); p.NeedsPush {
		t.Fatalf("preview after push still needs push: %+v", p)
	}
}

func TestShipPRNonDraftWhenAsked(t *testing.T) {
	e := newShipEnv(t, Options{})
	res := e.call(t, http.MethodPost, "/v1/git/pr", `{"title":"Ready","body":"b","draft":false,"confirmPush":true}`)
	if res.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", res.Code, res.Body.String())
	}
	if strings.Contains(e.ghCalls(t), "--draft") {
		t.Fatalf("draft flag passed: %s", e.ghCalls(t))
	}
}

func TestShipPRExistingIsReturnedNotDuplicated(t *testing.T) {
	e := newShipEnv(t, Options{})
	t.Setenv("GH_FAKE_MODE", "existing")
	p := decodePreview(t, e.call(t, http.MethodGet, "/v1/git/pr", ""))
	if p.Existing == nil || p.Existing.Number != 12 || !p.Existing.IsDraft || p.Existing.State != "open" {
		t.Fatalf("existing = %+v", p.Existing)
	}
	res := e.call(t, http.MethodPost, "/v1/git/pr", `{"title":"dup","body":"b","confirmPush":true}`)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"existing":true`) {
		t.Fatalf("create with existing = %d: %s", res.Code, res.Body.String())
	}
	if strings.Contains(e.ghCalls(t), "pr create") || e.hasRemoteBranch("feature/ship") {
		t.Fatal("existing PR was duplicated or branch pushed")
	}
	meta, _ := session.ReadMeta(e.sessions, "root1")
	if meta.PRNumber != 12 {
		t.Fatalf("existing PR not linked to the session: %+v", meta)
	}
}

func TestShipPRNonGitHubRemote(t *testing.T) {
	e := newShipEnv(t, Options{})
	e.git(t, e.repo, "remote", "set-url", "origin", "https://gitlab.com/acme/widgets.git")
	p := decodePreview(t, e.call(t, http.MethodGet, "/v1/git/pr", ""))
	if p.State != shipPRStateBlocked || !strings.Contains(strings.Join(p.Blockers, "\n"), "supports GitHub only") {
		t.Fatalf("preview = %+v", p)
	}
	if e.ghCalls(t) != "" {
		t.Fatalf("gh ran for a non-GitHub remote: %s", e.ghCalls(t))
	}
}

func TestShipPRGHSignedOutNeverEchoesStderr(t *testing.T) {
	e := newShipEnv(t, Options{})
	t.Setenv("GH_FAKE_MODE", "unauth")
	res := e.call(t, http.MethodGet, "/v1/git/pr", "")
	p := decodePreview(t, res)
	if p.State != shipPRStateBlocked || !strings.Contains(strings.Join(p.Blockers, "\n"), "gh auth login") {
		t.Fatalf("preview = %+v", p)
	}
	if strings.Contains(res.Body.String(), shipFakeToken()) || strings.Contains(res.Body.String(), "To get started") {
		t.Fatalf("gh stderr reached the browser: %s", res.Body.String())
	}
}

func TestShipPRCredentialRemoteNeverLeaks(t *testing.T) {
	e := newShipEnv(t, Options{})
	tok := shipFakeToken()
	e.git(t, e.repo, "remote", "set-url", "origin", "https://x-access-token:"+tok+"@github.com/acme/widgets.git")
	res := e.call(t, http.MethodGet, "/v1/git/pr", "")
	p := decodePreview(t, res)
	if p.Repo != "acme/widgets" || strings.Contains(res.Body.String(), tok) {
		t.Fatalf("preview leaked or failed: %s", res.Body.String())
	}
}

func TestShipPRReadOnlyRefused(t *testing.T) {
	e := newShipEnv(t, Options{ReadOnly: true})
	res := e.call(t, http.MethodPost, "/v1/git/pr", `{"title":"t","body":"b","confirmPush":true}`)
	if res.Code != http.StatusForbidden {
		t.Fatalf("read-only create = %d", res.Code)
	}
	if e.hasRemoteBranch("feature/ship") {
		t.Fatal("read-only daemon pushed")
	}
}

func TestShipPRHelpers(t *testing.T) {
	url, n := parsePRCreateOutput("Creating pull request for x into main\n\nhttps://github.com/acme/widgets/pull/9\n")
	if url != "https://github.com/acme/widgets/pull/9" || n != 9 {
		t.Fatalf("parse = %q %d", url, n)
	}
	if url, n := parsePRCreateOutput("https://evil.example/acme/widgets/pull/9"); url != "" || n != 0 {
		t.Fatalf("non-GitHub URL accepted: %q", url)
	}
	for branch, want := range map[string]bool{"main": true, "master": true, "trunk": true, "feature/x": false} {
		if got := isProtectedShipBranch(branch, "origin/trunk"); got != want {
			t.Errorf("isProtectedShipBranch(%q) = %v", branch, got)
		}
	}
	if ghBaseName("origin/main") != "main" || ghBaseName("develop") != "develop" {
		t.Fatal("ghBaseName")
	}
	tok := shipFakeToken()
	got := scrubRemoteText("git push: fatal: unable to access 'https://user:" + tok + "@github.com/a/b.git/': denied")
	if strings.Contains(got, tok) || strings.Contains(got, "user:") {
		t.Fatalf("scrub = %q", got)
	}
	for _, tc := range []struct{ in, want string }{
		{"gh is not installed on the daemon host", "not installed"},
		{"gh: To get started with GitHub CLI, please run:  gh auth login", "gh auth login"},
		{"gh: a pull request for branch \"x\" into branch \"main\" already exists", "already exists"},
		{"gh: something else", "gh auth status"},
	} {
		if msg := shipPRGHMessage(errors.New(tc.in)); !strings.Contains(msg, tc.want) {
			t.Errorf("shipPRGHMessage(%q) = %q", tc.in, msg)
		}
	}
}

func TestShipPRDraftPrefill(t *testing.T) {
	many := shipPRPreview{Branch: "feat-x", CommitsTotal: 25, Files: 3, Added: 10, Deleted: 2}
	for i := 0; i < 25; i++ {
		many.Commits = append(many.Commits, gitCommitJSON{Hash: strings.Repeat("a", 40), Subject: "change"})
	}
	title, body := shipPRDraft("Session goal", many)
	if title != "Session goal" {
		t.Fatalf("title = %q", title)
	}
	if !strings.Contains(body, "and 5 more commit(s)") || !strings.Contains(body, "3 file(s) changed, +10 −2") || !strings.Contains(body, "(aaaaaaaa)") {
		t.Fatalf("body = %s", body)
	}
	title, _ = shipPRDraft("", shipPRPreview{Branch: "fix-the-thing"})
	if title != "fix the thing" {
		t.Fatalf("branch title = %q", title)
	}
	tok := shipFakeToken()
	title, _ = shipPRDraft("", shipPRPreview{CommitsTotal: 1, Commits: []gitCommitJSON{{Hash: "abc", Subject: "rotate " + tok}}})
	if strings.Contains(title, tok) {
		t.Fatalf("draft title leaked a token: %q", title)
	}
}

func TestShipPRRefusesStalePreviewHead(t *testing.T) {
	e := newShipEnv(t, Options{})
	p := decodePreview(t, e.call(t, http.MethodGet, "/v1/git/pr", ""))
	if p.Head == "" || p.Head != e.git(t, e.repo, "rev-parse", "HEAD") {
		t.Fatalf("preview head = %q", p.Head)
	}
	// A new commit lands after the user reviewed the preview.
	if err := os.WriteFile(filepath.Join(e.repo, "late.txt"), []byte("late\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.git(t, e.repo, "add", "late.txt")
	e.git(t, e.repo, "commit", "-m", "late commit")
	res := e.call(t, http.MethodPost, "/v1/git/pr", `{"title":"t","body":"b","confirmPush":true,"head":"`+p.Head+`"}`)
	if res.Code != http.StatusConflict || !strings.Contains(res.Body.String(), "stale-preview") {
		t.Fatalf("stale create = %d: %s", res.Code, res.Body.String())
	}
	if e.hasRemoteBranch("feature/ship") || strings.Contains(e.ghCalls(t), "pr create") {
		t.Fatal("stale preview pushed or created")
	}
}

func TestShipPRRefusesForeignPushURL(t *testing.T) {
	e := newShipEnv(t, Options{})
	e.git(t, e.repo, "config", "remote.origin.pushurl", "https://gitlab.com/someone/else.git")
	p := decodePreview(t, e.call(t, http.MethodGet, "/v1/git/pr", ""))
	if p.State != shipPRStateBlocked || !strings.Contains(strings.Join(p.Blockers, "\n"), "push URL") {
		t.Fatalf("preview = %+v", p)
	}
	e.git(t, e.repo, "config", "remote.origin.pushurl", "git@github.com:acme/widgets.git")
	if p := decodePreview(t, e.call(t, http.MethodGet, "/v1/git/pr", "")); p.State != shipPRStateOK {
		t.Fatalf("same-repo pushurl should be allowed: %+v", p)
	}
}
