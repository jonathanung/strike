package server

import (
	"bytes"
	"encoding/json"
	"errors"
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

func TestParseStashList(t *testing.T) {
	out := []byte("aaaa111\x1f2026-01-02T00:00:00Z\x1fOn main: one\x1fwith sep\nbbbb222\x1f2026-01-01T00:00:00Z\x1fWIP on main: 123 subj\n")
	got := parseStashList(out)
	if len(got) != 2 || got[0].Ref != "stash@{0}" || got[1].Index != 1 || got[1].Hash != "bbbb222" || got[0].Message != "On main: one\x1fwith sep" || got[0].Date != "2026-01-02T00:00:00Z" {
		t.Fatalf("stashes = %+v", got)
	}
	if empty := parseStashList(nil); empty == nil || len(empty) != 0 {
		t.Fatalf("empty = %#v", empty)
	}
}

func TestStashFailureKeepsGitWords(t *testing.T) {
	msg := stashFailure("pop", errors.New("exit status 1"), "CONFLICT (content): Merge conflict in a.txt\n", "")
	if !strings.Contains(msg, "CONFLICT") || !strings.Contains(msg, "kept") {
		t.Fatalf("pop failure = %q", msg)
	}
	if msg := stashFailure("apply", errors.New("exit status 1"), "", ""); !strings.Contains(msg, "exit status 1") || strings.Contains(msg, "kept") {
		t.Fatalf("apply failure = %q", msg)
	}
}

// TestGitStashRoutes drives #1471 against a real repo: zero, one and
// several stashes (including a terminal-created one), push on a clean
// tree, untracked opt-in, diff preview, the hash pin, a conflicting pop
// that keeps the stash, apply, confirm-gated drop, and attach-only.
func TestGitStashRoutes(t *testing.T) {
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
		if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-b", "main")
	git("config", "user.email", "test@example.com")
	git("config", "user.name", "Test")
	write("a.txt", "one\n")
	git("add", ".")
	git("commit", "-m", "initial")

	live := NewLive("s1", repo, nil, make(chan protocol.Op, 1))
	srv, err := New(Options{SessionDir: t.TempDir(), Live: live})
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, target, body string) (int, string) {
		t.Helper()
		res := httptest.NewRecorder()
		srv.Handler().ServeHTTP(res, httptest.NewRequest(method, target, strings.NewReader(body)))
		return res.Code, res.Body.String()
	}
	list := func() []gitStashJSON {
		t.Helper()
		code, raw := call(http.MethodGet, "/v1/git/stash", "")
		if code != http.StatusOK {
			t.Fatalf("list = %d %s", code, raw)
		}
		var body struct {
			Stashes []gitStashJSON `json:"stashes"`
		}
		if err := json.Unmarshal([]byte(raw), &body); err != nil {
			t.Fatal(err)
		}
		return body.Stashes
	}
	ref := func(s gitStashJSON) string {
		return fmt.Sprintf(`{"index":%d,"hash":%q`, s.Index, s.Hash)
	}

	if got := list(); len(got) != 0 {
		t.Fatalf("zero stashes = %+v", got)
	}
	// Clean tree: honest error, nothing stashed.
	if code, raw := call(http.MethodPost, "/v1/git/stash", `{}`); code != http.StatusBadRequest || !strings.Contains(raw, "No local changes") {
		t.Fatalf("clean push = %d %s", code, raw)
	}
	// One stash with a message (a leading "-" stays a message).
	write("a.txt", "one\ntwo\n")
	if code, raw := call(http.MethodPost, "/v1/git/stash", `{"message":"-p not an option"}`); code != http.StatusOK {
		t.Fatalf("push = %d %s", code, raw)
	}
	if st := git("status", "--porcelain"); st != "" {
		t.Fatalf("tree not clean after stash: %q", st)
	}
	got := list()
	if len(got) != 1 || !strings.Contains(got[0].Message, "-p not an option") || got[0].Date == "" {
		t.Fatalf("one stash = %+v", got)
	}
	// Untracked files stay unless opted in (Q1).
	write("new.txt", "n\n")
	if code, raw := call(http.MethodPost, "/v1/git/stash", `{}`); code != http.StatusBadRequest {
		t.Fatalf("untracked-only push without opt-in = %d %s", code, raw)
	}
	if code, raw := call(http.MethodPost, "/v1/git/stash", `{"includeUntracked":true}`); code != http.StatusOK {
		t.Fatalf("push -u = %d %s", code, raw)
	}
	if _, err := os.Stat(filepath.Join(repo, "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("untracked file not stashed: %v", err)
	}
	// A terminal-created stash shows up too (default WIP message, Q3).
	write("a.txt", "one\nterminal\n")
	git("stash", "push")
	got = list()
	if len(got) != 3 || !strings.HasPrefix(got[0].Message, "WIP on main") || !strings.Contains(got[2].Message, "-p not an option") {
		t.Fatalf("several stashes = %+v", got)
	}
	for _, bad := range []string{`{"message":"two\nlines"}`, `{"message":"a\u001fb"}`, `{"message":"` + strings.Repeat("x", gitStashMessageMax+1) + `"}`} {
		write("a.txt", "dirty\n")
		if code, raw := call(http.MethodPost, "/v1/git/stash", bad); code != http.StatusBadRequest {
			t.Errorf("bad message = %d %s", code, raw)
		}
		git("checkout", "--", "a.txt")
	}

	// Diff preview of the oldest stash.
	oldest := got[2]
	code, raw := call(http.MethodGet, fmt.Sprintf("/v1/git/stash/diff?index=%d&hash=%s", oldest.Index, oldest.Hash), "")
	if code != http.StatusOK || !strings.Contains(raw, "+two") {
		t.Fatalf("stash diff = %d %s", code, raw)
	}
	// The -u stash preview includes its untracked file.
	code, raw = call(http.MethodGet, fmt.Sprintf("/v1/git/stash/diff?index=%d&hash=%s", got[1].Index, got[1].Hash), "")
	if code != http.StatusOK || !strings.Contains(raw, "new.txt") {
		t.Fatalf("untracked stash diff = %d %s", code, raw)
	}
	// The hash pin refuses a shifted index.
	if code, raw := call(http.MethodPost, "/v1/git/stash/apply", fmt.Sprintf(`{"index":0,"hash":%q}`, oldest.Hash)); code != http.StatusBadRequest || !strings.Contains(raw, "changed") {
		t.Fatalf("shifted index = %d %s", code, raw)
	}
	for _, body := range []string{`{"index":-1,"hash":"abcdef1"}`, `{"index":0,"hash":"zz"}`, `{"index":0,"hash":"--all"}`} {
		if code, raw := call(http.MethodPost, "/v1/git/stash/pop", body); code != http.StatusBadRequest {
			t.Errorf("bad ref %s = %d %s", body, code, raw)
		}
	}

	// Conflicting pop: 409 with git's words, and the stash is kept.
	write("a.txt", "one\nconflict\n")
	git("commit", "-am", "conflicting change")
	code, raw = call(http.MethodPost, "/v1/git/stash/pop", ref(oldest)+"}")
	if code != http.StatusConflict || !strings.Contains(raw, "kept") {
		t.Fatalf("conflicting pop = %d %s", code, raw)
	}
	if got := list(); len(got) != 3 {
		t.Fatalf("pop conflict dropped a stash: %+v", got)
	}
	git("reset", "--hard", "HEAD")

	// Clean apply of the untracked stash keeps it listed.
	got = list()
	untracked := got[1]
	if code, raw := call(http.MethodPost, "/v1/git/stash/apply", ref(untracked)+"}"); code != http.StatusOK {
		t.Fatalf("apply = %d %s", code, raw)
	}
	if _, err := os.Stat(filepath.Join(repo, "new.txt")); err != nil {
		t.Fatalf("apply did not restore new.txt: %v", err)
	}
	if got := list(); len(got) != 3 {
		t.Fatalf("apply removed the stash: %+v", got)
	}

	// Drop is confirm-gated.
	if code, raw := call(http.MethodPost, "/v1/git/stash/drop", ref(untracked)+"}"); code != http.StatusBadRequest {
		t.Fatalf("drop without confirm = %d %s", code, raw)
	}
	if code, raw := call(http.MethodPost, "/v1/git/stash/drop", ref(untracked)+`,"confirm":"drop-stash"}`); code != http.StatusOK {
		t.Fatalf("drop = %d %s", code, raw)
	}
	if got := list(); len(got) != 2 {
		t.Fatalf("after drop = %+v", got)
	}

	// Attach-only daemons refuse stash mutations.
	bare, err := New(Options{SessionDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	res := httptest.NewRecorder()
	bare.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/v1/git/stash", strings.NewReader(`{}`)))
	if res.Code != http.StatusForbidden {
		t.Fatalf("attach-only stash = %d %s", res.Code, res.Body.String())
	}
}
