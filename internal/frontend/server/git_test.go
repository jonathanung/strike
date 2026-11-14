package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonathanung/strike/internal/persist/session"
	"github.com/jonathanung/strike/internal/product/project"
	"github.com/jonathanung/strike/internal/protocol"
)

func TestParseBranchLine(t *testing.T) {
	cases := []struct {
		line          string
		branch        string
		detached      bool
		ahead, behind int
	}{
		{"## main", "main", false, 0, 0},
		{"## main...origin/main", "main", false, 0, 0},
		{"## main...origin/main [ahead 2]", "main", false, 2, 0},
		{"## main...origin/main [behind 3]", "main", false, 0, 3},
		{"## main...origin/main [ahead 1, behind 4]", "main", false, 1, 4},
		{"## HEAD (no branch)", "HEAD", true, 0, 0},
		{"## No commits yet on main", "main", false, 0, 0},
		{"## release/1.2.3", "release/1.2.3", false, 0, 0},
	}
	for _, tc := range cases {
		branch, detached, ahead, behind := parseBranchLine(tc.line)
		if branch != tc.branch || detached != tc.detached || ahead != tc.ahead || behind != tc.behind {
			t.Errorf("parseBranchLine(%q) = %q detached=%v ahead=%d behind=%d", tc.line, branch, detached, ahead, behind)
		}
	}
}

func TestSafeGitNames(t *testing.T) {
	if _, err := safeRepoPath("../etc/passwd"); err == nil {
		t.Fatal("parent path accepted")
	}
	if _, err := safeRepoPath("/etc/passwd"); err == nil {
		t.Fatal("absolute path accepted")
	}
	if _, err := safeRepoPath("."); err == nil {
		t.Fatal("dot accepted")
	}
	if got, err := safeRepoPath("src/main.go"); err != nil || got != "src/main.go" {
		t.Fatalf("safe path = %q %v", got, err)
	}
	for _, bad := range []string{"-rf", "main..x", "a b", "HEAD@{1}", "foo//bar"} {
		if _, err := safeBranchName(bad); err == nil {
			t.Errorf("branch %q accepted", bad)
		}
	}
	if _, err := safeBranchName("feature/git-panel"); err != nil {
		t.Fatal(err)
	}
}

func TestGitRoutesOnThrowawayRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	repo := t.TempDir()
	sessions := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-b", "main")
	git("config", "user.email", "test@example.com")
	git("config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repo, "alpha.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(".strike/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "alpha.txt", ".gitignore")
	git("commit", "-m", "initial")
	if err := os.WriteFile(filepath.Join(repo, "alpha.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	odd := "odd;name.txt"
	if err := os.WriteFile(filepath.Join(repo, odd), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".strike"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := []byte("{\"session\":{\"childIsolation\":\"worktree\",\"worktreeCleanup\":\"delete\",\"worktree\":\"always\"}}\n")
	if err := os.WriteFile(filepath.Join(repo, ".strike", "config"), cfg, 0o644); err != nil {
		t.Fatal(err)
	}

	mgr := session.NewManager(sessions)
	parent, err := mgr.Create(session.CreateOptions{ID: "parent1", Title: "Parent"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Create(session.CreateOptions{ID: "child1", ParentSessionID: parent.ID, Title: "Worker"}); err != nil {
		t.Fatal(err)
	}
	live := NewLive("parent1", repo, nil, make(chan protocol.Op, 1))
	srv, err := New(Options{SessionDir: sessions, Live: live})
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, target, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		res := httptest.NewRecorder()
		srv.Handler().ServeHTTP(res, req)
		return res
	}

	push := call(http.MethodPost, "/v1/git/push", `{"force":true}`)
	if push.Code != http.StatusNotFound {
		t.Fatalf("push status = %d, want 404 (no force-push route)", push.Code)
	}

	diffEscape := call(http.MethodGet, "/v1/git/diff?path=../../etc/passwd", "")
	if diffEscape.Code != http.StatusBadRequest {
		t.Fatalf("diff escape = %d %s", diffEscape.Code, diffEscape.Body.String())
	}
	badBranch := call(http.MethodPost, "/v1/git/checkout", `{"branch":"-rf","create":true}`)
	if badBranch.Code != http.StatusBadRequest {
		t.Fatalf("bad branch = %d %s", badBranch.Code, badBranch.Body.String())
	}

	status := call(http.MethodGet, "/v1/git/status", "")
	if status.Code != http.StatusOK {
		t.Fatalf("status = %d %s", status.Code, status.Body.String())
	}
	statusBody := status.Body.String()
	for _, want := range []string{`"branch":"main"`, `"path":"alpha.txt"`, `"path":"odd;name.txt"`, `"status":"?"`} {
		if !strings.Contains(statusBody, want) {
			t.Errorf("status missing %s: %s", want, statusBody)
		}
	}
	if strings.Contains(statusBody, "rm -rf") {
		t.Fatalf("status looked executed: %s", statusBody)
	}

	stagedDiff := call(http.MethodGet, "/v1/git/diff?path=alpha.txt&staged=1", "")
	if stagedDiff.Code != http.StatusOK {
		t.Fatalf("staged diff = %d %s", stagedDiff.Code, stagedDiff.Body.String())
	}
	workDiff := call(http.MethodGet, "/v1/git/diff?path=alpha.txt", "")
	if workDiff.Code != http.StatusOK || !strings.Contains(workDiff.Body.String(), "+two") {
		t.Fatalf("work diff = %d %s", workDiff.Code, workDiff.Body.String())
	}
	untrackedDiff := call(http.MethodGet, "/v1/git/diff?path="+url.QueryEscape(odd), "")
	if untrackedDiff.Code != http.StatusOK || !strings.Contains(untrackedDiff.Body.String(), "+x") {
		t.Fatalf("untracked diff = %d %s", untrackedDiff.Code, untrackedDiff.Body.String())
	}

	noConfirm := call(http.MethodPost, "/v1/git/discard", `{"path":"alpha.txt","confirm":"yes"}`)
	if noConfirm.Code != http.StatusBadRequest {
		t.Fatalf("discard without token = %d", noConfirm.Code)
	}
	raw, err := os.ReadFile(filepath.Join(repo, "alpha.txt"))
	if err != nil || string(raw) != "one\ntwo\n" {
		t.Fatalf("discard ran without confirm: %q %v", raw, err)
	}
	discard := call(http.MethodPost, "/v1/git/discard", `{"path":"alpha.txt","confirm":"discard"}`)
	if discard.Code != http.StatusOK {
		t.Fatalf("discard = %d %s", discard.Code, discard.Body.String())
	}
	raw, err = os.ReadFile(filepath.Join(repo, "alpha.txt"))
	if err != nil || string(raw) != "one\n" {
		t.Fatalf("after discard = %q %v", raw, err)
	}

	if err := os.WriteFile(filepath.Join(repo, "alpha.txt"), []byte("one\nstaged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stage := call(http.MethodPost, "/v1/git/stage", `{"paths":["alpha.txt"]}`)
	if stage.Code != http.StatusOK {
		t.Fatalf("stage = %d %s", stage.Code, stage.Body.String())
	}
	emptyMsg := call(http.MethodPost, "/v1/git/commit", `{"message":"  "}`)
	if emptyMsg.Code != http.StatusBadRequest {
		t.Fatalf("empty message = %d", emptyMsg.Code)
	}
	commit := call(http.MethodPost, "/v1/git/commit", `{"message":"stage alpha"}`)
	if commit.Code != http.StatusOK {
		t.Fatalf("commit = %d %s", commit.Code, commit.Body.String())
	}
	logRes := call(http.MethodGet, "/v1/git/log?limit=5", "")
	if logRes.Code != http.StatusOK || !strings.Contains(logRes.Body.String(), "stage alpha") {
		t.Fatalf("log = %d %s", logRes.Code, logRes.Body.String())
	}

	otherBranch := call(http.MethodPost, "/v1/git/checkout", `{"branch":"other","create":true}`)
	if otherBranch.Code != http.StatusOK {
		t.Fatalf("create other = %d %s", otherBranch.Code, otherBranch.Body.String())
	}
	if err := os.WriteFile(filepath.Join(repo, "alpha.txt"), []byte("other\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if res := call(http.MethodPost, "/v1/git/commit", `{"message":"other side","paths":["alpha.txt"]}`); res.Code != http.StatusOK {
		t.Fatalf("commit other = %d %s", res.Code, res.Body.String())
	}
	if res := call(http.MethodPost, "/v1/git/checkout", `{"branch":"main"}`); res.Code != http.StatusOK {
		t.Fatalf("switch main = %d %s", res.Code, res.Body.String())
	}
	if err := os.WriteFile(filepath.Join(repo, "alpha.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	switched := call(http.MethodPost, "/v1/git/checkout", `{"branch":"other"}`)
	if switched.Code == http.StatusOK {
		t.Fatal("switch succeeded on a dirty tree that would overwrite a file")
	}
	raw, _ = os.ReadFile(filepath.Join(repo, "alpha.txt"))
	if string(raw) != "dirty\n" {
		t.Fatalf("dirty file changed: %q", raw)
	}
	resetNo := call(http.MethodPost, "/v1/git/reset", `{"mode":"hard","confirm":"please"}`)
	if resetNo.Code != http.StatusBadRequest {
		t.Fatalf("reset without token = %d", resetNo.Code)
	}
	raw, _ = os.ReadFile(filepath.Join(repo, "alpha.txt"))
	if string(raw) != "dirty\n" {
		t.Fatalf("reset ran without confirm: %q", raw)
	}
	reset := call(http.MethodPost, "/v1/git/reset", `{"mode":"hard","confirm":"reset-hard"}`)
	if reset.Code != http.StatusOK {
		t.Fatalf("reset = %d %s", reset.Code, reset.Body.String())
	}
	raw, _ = os.ReadFile(filepath.Join(repo, "alpha.txt"))
	if string(raw) != "one\nstaged\n" {
		t.Fatalf("after reset = %q", raw)
	}
	other := call(http.MethodPost, "/v1/git/reset", `{"mode":"origin/main","confirm":"reset-hard"}`)
	if other.Code != http.StatusBadRequest {
		t.Fatalf("reset to remote-shaped mode = %d", other.Code)
	}

	makeFeature := call(http.MethodPost, "/v1/git/checkout", `{"branch":"feature","create":true}`)
	if makeFeature.Code != http.StatusOK {
		t.Fatalf("create feature = %d %s", makeFeature.Code, makeFeature.Body.String())
	}
	back := call(http.MethodPost, "/v1/git/checkout", `{"branch":"main"}`)
	if back.Code != http.StatusOK {
		t.Fatalf("switch main = %d %s", back.Code, back.Body.String())
	}
	delNo := call(http.MethodPost, "/v1/git/branch/delete", `{"name":"feature","confirm":"no"}`)
	if delNo.Code != http.StatusBadRequest {
		t.Fatalf("delete without token = %d", delNo.Code)
	}
	delCur := call(http.MethodPost, "/v1/git/branch/delete", `{"name":"main","confirm":"delete-branch"}`)
	if delCur.Code != http.StatusBadRequest {
		t.Fatalf("delete current = %d %s", delCur.Code, delCur.Body.String())
	}
	del := call(http.MethodPost, "/v1/git/branch/delete", `{"name":"feature","confirm":"delete-branch"}`)
	if del.Code != http.StatusOK {
		t.Fatalf("delete feature = %d %s", del.Code, del.Body.String())
	}
	// other has a commit main does not. -d must refuse; -D is not offered.
	forceDel := call(http.MethodPost, "/v1/git/branch/delete", `{"name":"other","confirm":"delete-branch"}`)
	if forceDel.Code == http.StatusOK {
		t.Fatal("unmerged branch was deleted; route must use git branch -d")
	}

	wt := call(http.MethodGet, "/v1/git/worktrees", "")
	if wt.Code != http.StatusOK {
		t.Fatalf("worktrees = %d %s", wt.Code, wt.Body.String())
	}
	for _, want := range []string{`"childIsolation":"worktree"`, `"isolatesChildren":true`, `"worktreeCleanup":"delete"`, `"sessionWorktree":"always"`, `"owner":"primary"`} {
		if !strings.Contains(wt.Body.String(), want) {
			t.Errorf("worktrees missing %s: %s", want, wt.Body.String())
		}
	}
	missing := call(http.MethodPost, "/v1/git/worktrees", `{"sessionId":"ghost1"}`)
	if missing.Code != http.StatusBadRequest {
		t.Fatalf("create unknown session = %d %s", missing.Code, missing.Body.String())
	}
	createdWT := call(http.MethodPost, "/v1/git/worktrees", `{"sessionId":"child1"}`)
	if createdWT.Code != http.StatusCreated {
		t.Fatalf("create worktree = %d %s", createdWT.Code, createdWT.Body.String())
	}
	var createdBody struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(createdWT.Body.Bytes(), &createdBody); err != nil {
		t.Fatal(err)
	}
	listed := call(http.MethodGet, "/v1/git/worktrees", "")
	if !strings.Contains(listed.Body.String(), `"owner":"child"`) || !strings.Contains(listed.Body.String(), `"ownerTitle":"Worker"`) {
		t.Fatalf("child owner missing: %s", listed.Body.String())
	}
	if _, err := project.Add(t.Context(), repo, "ghost1"); err != nil {
		t.Fatal(err)
	}
	stale := call(http.MethodGet, "/v1/git/worktrees", "")
	if !strings.Contains(stale.Body.String(), `"sessionId":"ghost1"`) || !strings.Contains(stale.Body.String(), `"stale":true`) {
		t.Fatalf("stale worktree missing: %s", stale.Body.String())
	}
	pruneNo := call(http.MethodPost, "/v1/git/worktrees/prune", `{"confirm":"no"}`)
	if pruneNo.Code != http.StatusBadRequest {
		t.Fatalf("prune without token = %d", pruneNo.Code)
	}
	if _, err := os.Stat(filepath.Join(repo, ".strike", "worktrees", "ghost1")); err != nil {
		t.Fatal("prune ran without confirm")
	}
	prune := call(http.MethodPost, "/v1/git/worktrees/prune", `{"confirm":"prune-stale"}`)
	if prune.Code != http.StatusOK || !strings.Contains(prune.Body.String(), "ghost1") {
		t.Fatalf("prune = %d %s", prune.Code, prune.Body.String())
	}
	if _, err := os.Stat(filepath.Join(repo, ".strike", "worktrees", "ghost1")); !os.IsNotExist(err) {
		t.Fatalf("ghost worktree still present: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, ".strike", "worktrees", "child1")); err != nil {
		t.Fatalf("owned worktree was pruned: %v", err)
	}
	primary := call(http.MethodPost, "/v1/git/worktrees/remove", `{"path":`+jsonString(repo)+`,"confirm":"remove-worktree"}`)
	if primary.Code != http.StatusBadRequest {
		t.Fatalf("remove primary = %d %s", primary.Code, primary.Body.String())
	}
	removeNo := call(http.MethodPost, "/v1/git/worktrees/remove", `{"path":`+jsonString(createdBody.Path)+`,"confirm":"no"}`)
	if removeNo.Code != http.StatusBadRequest {
		t.Fatalf("remove without token = %d", removeNo.Code)
	}
	remove := call(http.MethodPost, "/v1/git/worktrees/remove", `{"path":`+jsonString(createdBody.Path)+`,"confirm":"remove-worktree"}`)
	if remove.Code != http.StatusOK {
		t.Fatalf("remove = %d %s", remove.Code, remove.Body.String())
	}
	if _, err := os.Stat(createdBody.Path); !os.IsNotExist(err) {
		t.Fatalf("worktree remains: %v", err)
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestGitAttachOnlyRejectsMutations(t *testing.T) {
	bare, err := New(Options{SessionDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	res := httptest.NewRecorder()
	bare.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/v1/git/commit", bytes.NewReader([]byte(`{"message":"x"}`))))
	if res.Code != http.StatusForbidden {
		t.Fatalf("attach-only commit = %d %s", res.Code, res.Body.String())
	}
	res = httptest.NewRecorder()
	bare.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/v1/git/status", nil))
	if res.Code != http.StatusNotImplemented {
		t.Fatalf("attach-only status = %d %s", res.Code, res.Body.String())
	}
}

func TestParseNumstatFilesZ(t *testing.T) {
	got := parseNumstatFilesZ([]byte("\n3\t1\tsrc/a.go\x00-\t-\timg.png\x00"))
	want := []gitBranchFileJSON{{Path: "src/a.go", Added: 3, Deleted: 1}, {Path: "img.png", Binary: true}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("files = %+v", got)
	}
	if files := parseNumstatFilesZ(nil); files == nil || len(files) != 0 {
		t.Fatalf("empty = %#v", files)
	}
}

func TestOptionalBaseName(t *testing.T) {
	for _, bad := range []string{"-p", "--output=x", "main..x", "HEAD~1", "HEAD@{1}", "a b"} {
		if _, err := optionalBaseName(bad); err == nil {
			t.Errorf("base %q accepted", bad)
		}
	}
	for in, want := range map[string]string{"": "", " main ": "main", "origin/main": "origin/main"} {
		if got, err := optionalBaseName(in); err != nil || got != want {
			t.Errorf("optionalBaseName(%q) = %q %v", in, got, err)
		}
	}
}

// TestGitBranchChangesRoute covers #1450 against real repos: a branch
// ahead of base (three-dot files exclude base-side churn), the branch
// equal to base, an explicit base override, no merge-base, detached
// HEAD, no commits, no detectable default, and the per-file diff.
func TestGitBranchChangesRoute(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	newRepo := func(initial string) (string, func(...string) string, func(string, string)) {
		repo := t.TempDir()
		git := func(args ...string) string {
			t.Helper()
			cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
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
		write := func(name, body string) {
			t.Helper()
			p := filepath.Join(repo, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		git("init", "-b", initial)
		return repo, git, write
	}
	serve := func(repo string) func(string) (int, gitBranchChangesJSON, string) {
		live := NewLive("s1", repo, nil, make(chan protocol.Op, 1))
		srv, err := New(Options{SessionDir: t.TempDir(), Live: live})
		if err != nil {
			t.Fatal(err)
		}
		return func(target string) (int, gitBranchChangesJSON, string) {
			t.Helper()
			res := httptest.NewRecorder()
			srv.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, target, nil))
			var body gitBranchChangesJSON
			if res.Code == http.StatusOK && strings.HasPrefix(target, "/v1/git/branch-changes") {
				if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
					t.Fatalf("decode: %v\n%s", err, res.Body.String())
				}
			}
			return res.Code, body, res.Body.String()
		}
	}

	repo, git, write := newRepo("main")
	call := serve(repo)
	if code, got, raw := call("/v1/git/branch-changes"); code != http.StatusOK || got.State != branchStateNoCommits {
		t.Fatalf("no commits = %d %s", code, raw)
	}
	write("base.txt", "base\n")
	git("add", ".")
	git("commit", "-m", "base")
	if code, got, raw := call("/v1/git/branch-changes"); code != http.StatusOK || got.State != branchStateOnBase || got.Base != "main" || !got.BaseDetected {
		t.Fatalf("on base = %d %s", code, raw)
	}
	git("checkout", "-b", "feature")
	write("feat.txt", "one\ntwo\n")
	git("add", ".")
	git("commit", "-m", "feat one")
	write("base.txt", "base\nfeature edit\n")
	git("add", ".")
	git("commit", "-m", "feat two")
	// Base-side churn after the fork must not show as branch changes.
	git("checkout", "main")
	write("main-only.txt", "m\n")
	git("add", ".")
	git("commit", "-m", "main moves on")
	git("checkout", "feature")

	code, got, raw := call("/v1/git/branch-changes")
	if code != http.StatusOK || got.State != branchStateOK || got.Branch != "feature" || got.Base != "main" || !got.BaseDetected {
		t.Fatalf("feature = %d %s", code, raw)
	}
	if got.CommitsTotal != 2 || len(got.Commits) != 2 || got.Commits[0].Subject != "feat two" || got.MergeBase == "" {
		t.Fatalf("commits = %+v total=%d mb=%q", got.Commits, got.CommitsTotal, got.MergeBase)
	}
	files := map[string]gitBranchFileJSON{}
	for _, f := range got.Files {
		files[f.Path] = f
	}
	if len(got.Files) != 2 || files["feat.txt"].Added != 2 || files["base.txt"].Added != 1 {
		t.Fatalf("files = %+v", got.Files)
	}
	if _, ok := files["main-only.txt"]; ok {
		t.Fatal("base-side churn listed as a branch change (two-dot, want three-dot)")
	}
	// The commit list honours limit; the total stays honest.
	if code, got, raw := call("/v1/git/branch-changes?limit=1"); code != http.StatusOK || len(got.Commits) != 1 || got.CommitsTotal != 2 {
		t.Fatalf("limit = %d %s", code, raw)
	}

	// Per-file cumulative diff.
	code, _, raw = call("/v1/git/branch-diff?base=main&path=base.txt")
	if code != http.StatusOK || !strings.Contains(raw, "+feature edit") {
		t.Fatalf("branch diff = %d %s", code, raw)
	}
	for _, target := range []string{
		"/v1/git/branch-diff?path=base.txt",
		"/v1/git/branch-diff?base=main&path=../etc/passwd",
		"/v1/git/branch-diff?base=--output%3Dx&path=base.txt",
		"/v1/git/branch-changes?base=-p",
		"/v1/git/branch-changes?base=nope",
		"/v1/git/branch-changes?limit=0",
	} {
		if code, _, raw := call(target); code != http.StatusBadRequest {
			t.Errorf("%s = %d %s, want 400", target, code, raw)
		}
	}

	// Explicit base override: compared with feature itself is on-base.
	if code, got, raw := call("/v1/git/branch-changes?base=feature"); code != http.StatusOK || got.State != branchStateOnBase || got.BaseDetected {
		t.Fatalf("override = %d %s", code, raw)
	}

	// Unrelated history: no merge-base.
	git("checkout", "--orphan", "island")
	git("rm", "-rf", "--quiet", ".")
	write("island.txt", "i\n")
	git("add", ".")
	git("commit", "-m", "island")
	if code, got, raw := call("/v1/git/branch-changes?base=main"); code != http.StatusOK || got.State != branchStateNoMergeBase {
		t.Fatalf("no merge-base = %d %s", code, raw)
	}

	// Detached HEAD.
	git("checkout", "--detach", "main")
	if code, got, raw := call("/v1/git/branch-changes"); code != http.StatusOK || got.State != branchStateDetached {
		t.Fatalf("detached = %d %s", code, raw)
	}

	// No main/master/origin HEAD: the panel must ask, never guess.
	repo2, git2, write2 := newRepo("trunk")
	write2("a.txt", "a\n")
	git2("add", ".")
	git2("commit", "-m", "a")
	git2("checkout", "-b", "work")
	call2 := serve(repo2)
	if code, got, raw := call2("/v1/git/branch-changes"); code != http.StatusOK || got.State != branchStateChooseBase || got.Base != "" {
		t.Fatalf("choose base = %d %s", code, raw)
	}
	// Remote default: origin/HEAD -> origin/trunk is detected.
	git2("update-ref", "refs/remotes/origin/trunk", "trunk")
	git2("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/trunk")
	write2("w.txt", "w\n")
	git2("add", ".")
	git2("commit", "-m", "work")
	if code, got, raw := call2("/v1/git/branch-changes"); code != http.StatusOK || got.State != branchStateOK || got.Base != "origin/trunk" || !got.BaseDetected || got.CommitsTotal != 1 {
		t.Fatalf("origin/HEAD default = %d %s", code, raw)
	}
	// Dangling origin/HEAD (remote branch pruned): ask, do not 400.
	git2("update-ref", "-d", "refs/remotes/origin/trunk")
	if code, got, raw := call2("/v1/git/branch-changes"); code != http.StatusOK || got.State != branchStateChooseBase {
		t.Fatalf("dangling origin/HEAD = %d %s", code, raw)
	}

	// master is the fallback default when there is no main, and a tag
	// named like the base never shadows the branch.
	repo3, git3, write3 := newRepo("master")
	write3("m.txt", "m\n")
	git3("add", ".")
	git3("commit", "-m", "m")
	git3("checkout", "-b", "topic")
	write3("t.txt", "t\n")
	git3("add", ".")
	git3("commit", "-m", "t")
	git3("tag", "master", "HEAD")
	if code, got, raw := serve(repo3)("/v1/git/branch-changes"); code != http.StatusOK || got.State != branchStateOK || got.Base != "master" || !got.BaseDetected || got.CommitsTotal != 1 {
		t.Fatalf("master default = %d %s", code, raw)
	}

	// Literal pathspec: a bracketed path is not a glob.
	write("app/[id]/page.tsx", "bracket\n")
	write("app/i/page.tsx", "glob-match\n")
	git("checkout", "-q", "-B", "paths", "main")
	git("add", ".")
	git("commit", "-m", "paths")
	code, _, raw = call("/v1/git/branch-diff?base=main&path=" + url.QueryEscape("app/[id]/page.tsx"))
	if code != http.StatusOK || !strings.Contains(raw, "+bracket") || strings.Contains(raw, "glob-match") {
		t.Fatalf("literal pathspec = %d %s", code, raw)
	}
}

func TestSafeGitRef(t *testing.T) {
	for _, bad := range []string{"", "-p", "--output=/tmp/x", "a..b", "HEAD@{1}", "HEAD~1", "HEAD^", "a b", "x:y", "a\nb", strings.Repeat("a", 201)} {
		if _, err := safeGitRef(bad); err == nil {
			t.Errorf("ref %q accepted", bad)
		}
	}
	for _, good := range []string{"0123456789abcdef0123456789abcdef01234567", "main", "v1.2.3", "feature/x"} {
		if got, err := safeGitRef(" " + good + " "); err != nil || got != good {
			t.Errorf("safeGitRef(%q) = %q %v", good, got, err)
		}
	}
}

func TestParseShowNumstatZ(t *testing.T) {
	out := []byte("\n3\t1\tsrc/a.go\x00-\t-\timg.png\x000\t2\tdocs/old.md\x00")
	got := parseShowNumstatZ(out)
	want := []gitShowFileJSON{
		{Path: "src/a.go", Added: 3, Deleted: 1},
		{Path: "img.png", Binary: true},
		{Path: "docs/old.md", Deleted: 2},
	}
	if len(got) != len(want) {
		t.Fatalf("files = %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("file %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if files := parseShowNumstatZ(nil); files == nil || len(files) != 0 {
		t.Fatalf("empty numstat = %#v, want empty non-nil slice", files)
	}
}

// TestGitShowRoute inspects real commits (#1484): a multi-file commit
// with a long body, a merge (diffed against its first parent), the patch
// cap, and refused refs.
func TestGitShowRoute(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
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
	write := func(name, body string) {
		t.Helper()
		p := filepath.Join(repo, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-b", "main")
	write("alpha.txt", "one\n")
	git("add", ".")
	git("commit", "-m", "initial")
	root := git("rev-parse", "HEAD")

	write("alpha.txt", "one\ntwo\n")
	write("src/beta.go", "package beta\n")
	git("add", ".")
	longBody := strings.Repeat("Explains the change in detail.\n", 40)
	git("commit", "-m", "multi-file change", "-m", longBody)
	multi := git("rev-parse", "HEAD")

	git("checkout", "-b", "side")
	write("side.txt", "side\n")
	git("add", ".")
	git("commit", "-m", "side work")
	git("checkout", "main")
	write("gamma.txt", "g\n")
	git("add", ".")
	git("commit", "-m", "main work")
	git("merge", "--no-ff", "-m", "merge side", "side")
	merge := git("rev-parse", "HEAD")

	write("big.txt", strings.Repeat("0123456789abcdef0123456789abcdef\n", gitDiffCap/32+100))
	git("add", ".")
	git("commit", "-m", "big file")
	big := git("rev-parse", "HEAD")

	live := NewLive("s1", repo, nil, make(chan protocol.Op, 1))
	srv, err := New(Options{SessionDir: t.TempDir(), Live: live})
	if err != nil {
		t.Fatal(err)
	}
	show := func(ref string) (int, gitShowJSON, string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/v1/git/show?ref="+url.QueryEscape(ref), nil)
		res := httptest.NewRecorder()
		srv.Handler().ServeHTTP(res, req)
		var body gitShowJSON
		if res.Code == http.StatusOK {
			if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode: %v\n%s", err, res.Body.String())
			}
		}
		return res.Code, body, res.Body.String()
	}

	code, got, raw := show(multi)
	if code != http.StatusOK {
		t.Fatalf("show multi = %d %s", code, raw)
	}
	if got.Hash != multi || got.Subject != "multi-file change" || got.Author != "Test" || got.Date == "" {
		t.Fatalf("metadata = %+v", got)
	}
	if got.Body != strings.TrimRight(longBody, "\n") {
		t.Fatalf("body = %q", got.Body)
	}
	if len(got.Parents) != 1 || got.Parents[0] != root {
		t.Fatalf("parents = %v, want [%s]", got.Parents, root)
	}
	paths := map[string]gitShowFileJSON{}
	for _, f := range got.Files {
		paths[f.Path] = f
	}
	if len(got.Files) != 2 || got.FilesTotal != 2 || paths["alpha.txt"].Added != 1 || paths["src/beta.go"].Added != 1 {
		t.Fatalf("files = %+v", got.Files)
	}
	if !strings.Contains(got.Patch, "+two") || !strings.Contains(got.Patch, "src/beta.go") || got.Truncated {
		t.Fatalf("patch = %q truncated=%v", got.Patch, got.Truncated)
	}

	// Short hashes from the log resolve to the full hash.
	if code, got, raw := show(multi[:7]); code != http.StatusOK || got.Hash != multi {
		t.Fatalf("short hash = %d %s", code, raw)
	}

	code, got, raw = show(merge)
	if code != http.StatusOK {
		t.Fatalf("show merge = %d %s", code, raw)
	}
	if len(got.Parents) != 2 {
		t.Fatalf("merge parents = %v", got.Parents)
	}
	if len(got.Files) != 1 || got.Files[0].Path != "side.txt" || !strings.Contains(got.Patch, "+side") {
		t.Fatalf("merge vs first parent: files=%+v patch=%q", got.Files, got.Patch)
	}

	code, got, raw = show(root)
	if code != http.StatusOK || len(got.Parents) != 0 || len(got.Files) != 1 || got.Files[0].Path != "alpha.txt" {
		t.Fatalf("root commit = %d %+v %s", code, got, raw)
	}

	code, got, raw = show(big)
	if code != http.StatusOK || !got.Truncated || len(got.Patch) != gitDiffCap {
		t.Fatalf("big commit = %d truncated=%v len=%d %s", code, got.Truncated, len(got.Patch), raw[:min(len(raw), 200)])
	}

	pwned := filepath.Join(t.TempDir(), "pwned")
	for _, bad := range []string{"", "--output=" + pwned, "-p", "HEAD~1", "main..side", "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"} {
		if code, _, raw := show(bad); code != http.StatusBadRequest {
			t.Errorf("ref %q = %d %s, want 400", bad, code, raw)
		}
	}
	if _, err := os.Stat(pwned); err == nil {
		t.Fatal("option-looking ref was executed")
	}

	// A commit touching more paths than gitShowFilesMax lists the cap
	// and reports the real total.
	for i := 0; i < gitShowFilesMax+5; i++ {
		write(fmt.Sprintf("many/f%03d.txt", i), "x\n")
	}
	git("add", ".")
	git("commit", "-m", "many files")
	code, got, raw = show(git("rev-parse", "HEAD"))
	if code != http.StatusOK || len(got.Files) != gitShowFilesMax || got.FilesTotal != gitShowFilesMax+5 {
		t.Fatalf("many files = %d files=%d total=%d %s", code, len(got.Files), got.FilesTotal, raw[:min(len(raw), 200)])
	}
}
