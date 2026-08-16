package tool

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
}

func testGitEnv() []string {
	return append(os.Environ(),
		"GIT_AUTHOR_NAME=t",
		"GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t",
		"GIT_COMMITTER_EMAIL=t@t",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
	)
}

func runGitOk(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = testGitEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func initGitRepo(t *testing.T) string {
	t.Helper()
	requireGit(t)
	dir := t.TempDir()
	runGitOk(t, dir, "init", "-b", "main")
	runGitOk(t, dir, "config", "user.name", "t")
	runGitOk(t, dir, "config", "user.email", "t@t")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitOk(t, dir, "add", "README")
	runGitOk(t, dir, "commit", "-m", "init")
	return dir
}

func parseGitPayload(t *testing.T, res Result) gitPayload {
	t.Helper()
	var p gitPayload
	if err := json.Unmarshal([]byte(res.Output), &p); err != nil {
		t.Fatalf("unmarshal output: %v\n%s", err, res.Output)
	}
	var meta gitPayload
	if err := json.Unmarshal(res.Metadata, &meta); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}
	if p.Action != meta.Action || p.Count != meta.Count || p.Truncated != meta.Truncated {
		t.Fatalf("metadata mismatch output=%+v meta=%+v", p, meta)
	}
	return p
}

func execGit(t *testing.T, dir string, args map[string]any) (Result, error) {
	t.Helper()
	return NewGit().Execute(context.Background(), mustJSON(t, args), allowAll(dir))
}

func TestGitNameContractSchema(t *testing.T) {
	tl := NewGit()
	if tl.Name() != "git" {
		t.Fatalf("Name() = %q", tl.Name())
	}
	c := LookupContract(tl)
	if c.SideEffect != SideEffectRead || c.Idempotency != IdempotencySafeRetry {
		t.Fatalf("contract = %+v", c)
	}
	if len(tl.Schema()) == 0 || tl.Description() == "" {
		t.Fatal("missing schema/description")
	}
	if !strings.Contains(tl.Description(), "Read-only") {
		t.Fatalf("description should say read-only: %q", tl.Description())
	}
}

func TestGitStatusDiffLogBlameShow(t *testing.T) {
	dir := initGitRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("hello\nworld\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "extra.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	st, err := execGit(t, dir, map[string]any{"action": "status"})
	if err != nil {
		t.Fatal(err)
	}
	sp := parseGitPayload(t, st)
	if !sp.OK || sp.Action != "status" {
		t.Fatalf("status payload = %+v", sp)
	}
	if sp.Branch != "main" {
		t.Fatalf("branch = %q, want main", sp.Branch)
	}
	if sp.Count < 1 {
		t.Fatalf("status count = %d, want >= 1", sp.Count)
	}
	foundMod, foundUntracked := false, false
	for _, f := range sp.Files {
		if f.Path == "README" && f.Status == "modified" {
			foundMod = true
		}
		if f.Path == "extra.txt" && f.Status == "untracked" {
			foundUntracked = true
		}
	}
	if !foundMod || !foundUntracked {
		t.Fatalf("status files = %+v", sp.Files)
	}

	df, err := execGit(t, dir, map[string]any{"action": "diff", "path": "README"})
	if err != nil {
		t.Fatal(err)
	}
	dp := parseGitPayload(t, df)
	if dp.Action != "diff" || dp.Count != 1 {
		t.Fatalf("diff payload = %+v", dp)
	}
	if dp.Files[0].Path != "README" || len(dp.Files[0].Hunks) == 0 {
		t.Fatalf("diff files = %+v", dp.Files)
	}
	sawPlus := false
	for _, h := range dp.Files[0].Hunks {
		for _, line := range h.Lines {
			if line == "+world" {
				sawPlus = true
			}
		}
	}
	if !sawPlus {
		t.Fatalf("diff missing +world hunk line: %+v", dp.Files[0].Hunks)
	}

	lg, err := execGit(t, dir, map[string]any{"action": "log"})
	if err != nil {
		t.Fatal(err)
	}
	lp := parseGitPayload(t, lg)
	if lp.Action != "log" || lp.Count < 1 {
		t.Fatalf("log payload = %+v", lp)
	}
	if lp.Commits[0].Subject != "init" || lp.Commits[0].Hash == "" {
		t.Fatalf("log commits = %+v", lp.Commits)
	}

	bl, err := execGit(t, dir, map[string]any{"action": "blame", "path": "README"})
	if err != nil {
		t.Fatal(err)
	}
	bp := parseGitPayload(t, bl)
	if bp.Action != "blame" || bp.Count < 1 {
		t.Fatalf("blame payload = %+v", bp)
	}
	if bp.Lines[0].Line != 1 || bp.Lines[0].Hash == "" || bp.Lines[0].Content != "hello" {
		t.Fatalf("blame lines = %+v", bp.Lines)
	}
	if bp.Lines[0].Author == "" {
		t.Fatalf("blame missing author: %+v", bp.Lines[0])
	}

	sh, err := execGit(t, dir, map[string]any{"action": "show"})
	if err != nil {
		t.Fatal(err)
	}
	shp := parseGitPayload(t, sh)
	if shp.Action != "show" || shp.Commit == nil {
		t.Fatalf("show payload = %+v", shp)
	}
	if shp.Commit.Subject != "init" || shp.Commit.Hash == "" {
		t.Fatalf("show commit = %+v", shp.Commit)
	}
	if shp.Count < 1 || shp.Files[0].Path != "README" {
		t.Fatalf("show files = %+v", shp.Files)
	}
}

func TestGitLogBounds(t *testing.T) {
	dir := initGitRepo(t)
	for i := 0; i < 4; i++ {
		name := filepath.Join(dir, "n.txt")
		if err := os.WriteFile(name, []byte(strings.Repeat("x", i+1)+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		runGitOk(t, dir, "add", "n.txt")
		runGitOk(t, dir, "commit", "-m", "c"+string(rune('1'+i)))
	}
	res, err := execGit(t, dir, map[string]any{"action": "log", "maxResults": 2})
	if err != nil {
		t.Fatal(err)
	}
	p := parseGitPayload(t, res)
	if !p.Truncated {
		t.Fatalf("want truncated log, got %+v", p)
	}
	if p.Count != 2 {
		t.Fatalf("count = %d, want 2", p.Count)
	}
	if p.Total < 3 {
		t.Fatalf("total = %d, want >= 3", p.Total)
	}
}

func TestGitStatusBounds(t *testing.T) {
	dir := initGitRepo(t)
	for i := 0; i < 4; i++ {
		name := filepath.Join(dir, "f"+string(rune('a'+i))+".txt")
		if err := os.WriteFile(name, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	res, err := execGit(t, dir, map[string]any{"action": "status", "maxResults": 2})
	if err != nil {
		t.Fatal(err)
	}
	p := parseGitPayload(t, res)
	if !p.Truncated || p.Count != 2 || p.Total < 4 {
		t.Fatalf("status bounds = %+v", p)
	}
}

func TestGitNonRepo(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	_, err := execGit(t, dir, map[string]any{"action": "status"})
	if err == nil {
		t.Fatal("expected non-repo error")
	}
	if CodeOf(err) != string(CodePreconditionFailed) {
		t.Fatalf("code = %q, want precondition_failed: %v", CodeOf(err), err)
	}
	if !strings.Contains(err.Error(), "not a git repository") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(strings.ToLower(err.Error()), "bash") {
		t.Fatalf("must not mention bash: %v", err)
	}
}

func TestGitToplevelOutsideWorkspace(t *testing.T) {
	parent := initGitRepo(t)
	child := filepath.Join(parent, "sub")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := execGit(t, child, map[string]any{"action": "status"})
	if err == nil {
		t.Fatal("expected toplevel-outside error")
	}
	if CodeOf(err) != string(CodePreconditionFailed) {
		t.Fatalf("code = %q: %v", CodeOf(err), err)
	}
	if !strings.Contains(err.Error(), "outside the workspace") {
		t.Fatalf("err = %v", err)
	}
}

func TestGitMutationReject(t *testing.T) {
	dir := t.TempDir()
	for _, action := range []string{"commit", "push", "reset", "checkout", "config"} {
		_, err := execGit(t, dir, map[string]any{"action": action})
		if err == nil {
			t.Fatalf("action %q: want error", action)
		}
		if CodeOf(err) != string(CodeInvalidArgs) {
			t.Fatalf("action %q code = %q: %v", action, CodeOf(err), err)
		}
		if !strings.Contains(err.Error(), "read-only") {
			t.Fatalf("action %q err = %v", action, err)
		}
	}
}

func TestGitUnknownAction(t *testing.T) {
	_, err := execGit(t, t.TempDir(), map[string]any{"action": "stash"})
	if err == nil {
		t.Fatal("expected error")
	}
	// stash is classified as mutating
	if !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("err = %v", err)
	}
	_, err = execGit(t, t.TempDir(), map[string]any{"action": "foobar"})
	if err == nil || CodeOf(err) != string(CodeInvalidArgs) {
		t.Fatalf("unknown action: %v", err)
	}
}

func TestGitBlameRequiresPath(t *testing.T) {
	_, err := execGit(t, t.TempDir(), map[string]any{"action": "blame"})
	if err == nil || CodeOf(err) != string(CodeInvalidArgs) {
		t.Fatalf("err = %v", err)
	}
}

func TestGitRefRejectsDash(t *testing.T) {
	_, err := execGit(t, t.TempDir(), map[string]any{"action": "show", "ref": "--pretty=full"})
	if err == nil || !strings.Contains(err.Error(), "must not start") {
		t.Fatalf("err = %v", err)
	}
}

func TestGitPermissionNameDistinctFromBash(t *testing.T) {
	dir := initGitRepo(t)
	var got AskRequest
	tc := allowAll(dir)
	tc.Ask = func(_ context.Context, req AskRequest) error {
		got = req
		return nil
	}
	if _, err := NewGit().Execute(context.Background(), mustJSON(t, map[string]any{
		"action": "status",
	}), tc); err != nil {
		t.Fatal(err)
	}
	if got.Permission != "git" {
		t.Fatalf("permission = %q, want git", got.Permission)
	}
	if got.Permission == "bash" {
		t.Fatal("permission must not be bash")
	}
}

func TestGitPermissionDenied(t *testing.T) {
	dir := initGitRepo(t)
	tc := allowAll(dir)
	tc.Ask = func(context.Context, AskRequest) error {
		return ErrPermissionDenied("no")
	}
	_, err := NewGit().Execute(context.Background(), mustJSON(t, map[string]any{
		"action": "status",
	}), tc)
	if err == nil || CodeOf(err) != string(CodePermissionDenied) {
		t.Fatalf("err = %v", err)
	}
}

func TestGitStripsGITDIR(t *testing.T) {
	dir := initGitRepo(t)
	other := initGitRepo(t)
	if err := os.WriteFile(filepath.Join(other, "secret.txt"), []byte("nope\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitOk(t, other, "add", "secret.txt")
	runGitOk(t, other, "commit", "-m", "secret")

	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_WORK_TREE", other)

	res, err := execGit(t, dir, map[string]any{"action": "log", "maxResults": 5})
	if err != nil {
		t.Fatal(err)
	}
	p := parseGitPayload(t, res)
	for _, c := range p.Commits {
		if c.Subject == "secret" {
			t.Fatalf("used GIT_DIR/GIT_WORK_TREE from env: %+v", p.Commits)
		}
	}
	if p.Count < 1 || p.Commits[0].Subject != "init" {
		t.Fatalf("log = %+v", p.Commits)
	}
}

func TestGitPathEscape(t *testing.T) {
	dir := initGitRepo(t)
	_, err := execGit(t, dir, map[string]any{"action": "diff", "path": "../outside"})
	var esc *WorkspaceEscapeError
	if !errors.As(err, &esc) {
		t.Fatalf("want WorkspaceEscapeError, got %v", err)
	}
}

func TestGitInvalidJSON(t *testing.T) {
	_, err := NewGit().Execute(context.Background(), json.RawMessage(`{`), allowAll(t.TempDir()))
	if err == nil || CodeOf(err) != string(CodeInvalidArgs) {
		t.Fatalf("err = %v", err)
	}
}

func TestGitMissingAction(t *testing.T) {
	_, err := execGit(t, t.TempDir(), map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "action is required") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseGitStatusV2(t *testing.T) {
	raw := strings.Join([]string{
		"# branch.head main",
		"# branch.ab +2 -1",
		"1 M. N... 100644 100644 100644 abc def README",
		"? extra.txt",
	}, "\x00") + "\x00"
	branch, ahead, behind, files := parseGitStatusV2(raw)
	if branch != "main" || ahead != 2 || behind != 1 {
		t.Fatalf("branch=%s ahead=%d behind=%d", branch, ahead, behind)
	}
	if len(files) != 2 || files[0].Path != "README" || files[0].Status != "modified" {
		t.Fatalf("files = %+v", files)
	}
	if files[1].Status != "untracked" || files[1].Path != "extra.txt" {
		t.Fatalf("untracked = %+v", files[1])
	}
}

func TestParseUnifiedDiffBounds(t *testing.T) {
	text := "diff --git a/a.txt b/a.txt\n--- a/a.txt\n+++ b/a.txt\n@@ -1,1 +1,3 @@\n keep\n+one\n+two\n" +
		"diff --git a/b.txt b/b.txt\n--- a/b.txt\n+++ b/b.txt\n@@ -1,1 +1,1 @@\n-old\n+new\n"
	files, total, trunc := parseUnifiedDiff(text, 1, 200)
	if !trunc || total != 2 || len(files) != 1 {
		t.Fatalf("files=%d total=%d trunc=%v", len(files), total, trunc)
	}
	if files[0].Path != "a.txt" || len(files[0].Hunks) != 1 {
		t.Fatalf("file = %+v", files[0])
	}
}

func TestGitIgnoresRepoAlias(t *testing.T) {
	dir := initGitRepo(t)
	runGitOk(t, dir, "config", "alias.status", "!echo PWNED && false")
	res, err := execGit(t, dir, map[string]any{"action": "status"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Output, "PWNED") {
		t.Fatalf("repo alias executed: %s", res.Output)
	}
	p := parseGitPayload(t, res)
	if !p.OK || p.Action != "status" {
		t.Fatalf("payload = %+v", p)
	}
}

func TestGitIgnoresHomeAlias(t *testing.T) {
	dir := initGitRepo(t)
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[alias]\n\tstatus = !echo PWNED && false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	res, err := execGit(t, dir, map[string]any{"action": "status"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Output, "PWNED") {
		t.Fatalf("home alias executed: %s", res.Output)
	}
}
