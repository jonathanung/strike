package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jonathanung/strike/internal/daemon/bestofn"
	"github.com/jonathanung/strike/internal/product/project"
	"github.com/jonathanung/strike/internal/protocol"
)

// fakeBestOfNSpawner stands in for team.spawn / team.child_interrupt.
type fakeBestOfNSpawner struct {
	mu        sync.Mutex
	specs     []bestofn.SpawnSpec
	failModel map[string]bool
	cancelled []string
	n         int
}

func (f *fakeBestOfNSpawner) Spawn(_ context.Context, spec bestofn.SpawnSpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.specs = append(f.specs, spec)
	if f.failModel[spec.Model] {
		return "", errors.New("provider for " + spec.Model + " is not configured")
	}
	f.n++
	return fmt.Sprintf("child-%d", f.n), nil
}

func (f *fakeBestOfNSpawner) Cancel(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelled = append(f.cancelled, id)
	return nil
}

func bestOfNServer(t *testing.T, sp *fakeBestOfNSpawner, readOnly bool) *Server {
	t.Helper()
	srv, err := New(Options{
		SessionDir:     t.TempDir(),
		ReadOnly:       readOnly,
		BestOfNSpawner: func(string) bestofn.Spawner { return sp },
	})
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

func bestOfNDo(t *testing.T, srv *Server, method, path, body string) (int, bestOfNRunDTO, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	srv.Handler().ServeHTTP(rec, req)
	var dto bestOfNRunDTO
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
			t.Fatalf("decode %s %s: %v\n%s", method, path, err, rec.Body.String())
		}
	}
	return rec.Code, dto, rec.Body.String()
}

func TestBestOfNStartValidation(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"no prompt", `{"ownerRoot":"r","prompt":" ","candidates":[{"model":"a"},{"model":"b"}]}`},
		{"one model", `{"ownerRoot":"r","prompt":"p","candidates":[{"model":"a"}]}`},
		{"five models", `{"ownerRoot":"r","prompt":"p","candidates":[{"model":"a"},{"model":"b"},{"model":"c"},{"model":"d"},{"model":"e"}]}`},
		{"duplicate", `{"ownerRoot":"r","prompt":"p","candidates":[{"model":"a"},{"model":"a"}]}`},
		{"negative budget", `{"ownerRoot":"r","prompt":"p","candidates":[{"model":"a"},{"model":"b"}],"budget":{"timeoutSec":-1}}`},
		{"bad json", `{`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sp := &fakeBestOfNSpawner{}
			srv := bestOfNServer(t, sp, false)
			code, _, raw := bestOfNDo(t, srv, http.MethodPost, "/v1/bestofn", tt.body)
			if code != http.StatusBadRequest {
				t.Fatalf("code = %d, want 400: %s", code, raw)
			}
			if len(sp.specs) != 0 {
				t.Fatalf("spawned %d children on an invalid request", len(sp.specs))
			}
		})
	}
}

func TestBestOfNReadOnlyRefusesStart(t *testing.T) {
	sp := &fakeBestOfNSpawner{}
	srv := bestOfNServer(t, sp, true)
	code, _, raw := bestOfNDo(t, srv, http.MethodPost, "/v1/bestofn",
		`{"ownerRoot":"r","prompt":"p","candidates":[{"model":"a"},{"model":"b"}]}`)
	if code != http.StatusForbidden || !strings.Contains(raw, "read_only") {
		t.Fatalf("code = %d body %s, want 403 read_only", code, raw)
	}
	if len(sp.specs) != 0 {
		t.Fatal("read-only daemon spawned children")
	}
}

// One spawn fails, one candidate finishes, one is cancelled: the card still
// completes with the remaining runs, and adopting the finished one archives
// the rest.
func TestBestOfNLifecycleOverHTTP(t *testing.T) {
	sp := &fakeBestOfNSpawner{failModel: map[string]bool{"broken": true}}
	srv := bestOfNServer(t, sp, false)
	code, dto, raw := bestOfNDo(t, srv, http.MethodPost, "/v1/bestofn",
		`{"ownerRoot":"root-a","prompt":"explain the bug","candidates":[{"model":"a"},{"model":"b","effort":"high"},{"model":"broken"}],"budget":{"maxTurns":12,"timeoutSec":600}}`)
	if code != http.StatusOK {
		t.Fatalf("start = %d: %s", code, raw)
	}
	if dto.CostMultiplier != 3 || dto.OwnerRoot != "root-a" || dto.Edits || dto.Complete {
		t.Fatalf("start dto = %+v", dto)
	}
	if dto.Budget.MaxTurns != 12 || dto.Budget.TimeoutSec != 600 {
		t.Fatalf("budget = %+v", dto.Budget)
	}
	wantStatus := []string{"running", "running", "failed"}
	for i, c := range dto.Candidates {
		if c.Status != wantStatus[i] {
			t.Fatalf("candidate %d status = %q, want %q", i, c.Status, wantStatus[i])
		}
	}
	if dto.Candidates[1].Label != "b (high)" || dto.Candidates[2].Error == "" {
		t.Fatalf("candidates = %+v", dto.Candidates)
	}
	for _, spec := range sp.specs {
		if spec.Isolation != bestofn.IsolationShared || spec.Budget.MaxTurns != 12 {
			t.Fatalf("spec = %+v", spec)
		}
	}
	if sp.specs[1].Model != "b" || sp.specs[1].Effort != "high" {
		t.Fatalf("spec 2 = %+v", sp.specs[1])
	}

	e := srv.bestOfN.get(dto.ID)
	if e == nil {
		t.Fatal("run not registered")
	}
	srv.applyBestOfNChild(e, protocol.ChildCompleted{
		Correlation: protocol.Correlation{SessionID: "child-1"},
		Status:      protocol.ChildStatusCompleted,
		Handoff:     protocol.CompletionHandoff{Summary: "the race is in Close"},
	})
	// A completion for an unrelated child is ignored.
	srv.applyBestOfNChild(e, protocol.ChildCompleted{
		Correlation: protocol.Correlation{SessionID: "someone-else"},
		Status:      protocol.ChildStatusCompleted,
	})

	if code, _, raw := bestOfNDo(t, srv, http.MethodPost, "/v1/bestofn/"+dto.ID+"/adopt", `{"childId":"child-1"}`); code != http.StatusConflict {
		t.Fatalf("adopt while running = %d: %s", code, raw)
	}
	code, dto, raw = bestOfNDo(t, srv, http.MethodPost, "/v1/bestofn/"+dto.ID+"/cancel", `{}`)
	if code != http.StatusOK {
		t.Fatalf("cancel = %d: %s", code, raw)
	}
	if !dto.Complete || dto.Candidates[0].Status != "done" || dto.Candidates[1].Status != "cancelled" {
		t.Fatalf("after cancel = %+v", dto.Candidates)
	}
	if len(sp.cancelled) != 1 || sp.cancelled[0] != "child-2" {
		t.Fatalf("cancelled = %v, want only the running child", sp.cancelled)
	}
	if code, _, raw := bestOfNDo(t, srv, http.MethodPost, "/v1/bestofn/"+dto.ID+"/adopt", `{"childId":"child-2"}`); code != http.StatusConflict {
		t.Fatalf("adopting a cancelled run = %d: %s", code, raw)
	}
	code, dto, raw = bestOfNDo(t, srv, http.MethodPost, "/v1/bestofn/"+dto.ID+"/adopt", `{"childId":"child-1"}`)
	if code != http.StatusOK {
		t.Fatalf("adopt = %d: %s", code, raw)
	}
	if dto.Decision == nil || dto.Decision.Keep != "child-1" || len(dto.Decision.Archived) != 1 || dto.Decision.Archived[0] != "child-2" {
		t.Fatalf("decision = %+v", dto.Decision)
	}
	if code, _, _ := bestOfNDo(t, srv, http.MethodPost, "/v1/bestofn/"+dto.ID+"/keep-none", `{}`); code != http.StatusConflict {
		t.Fatalf("second decision = %d, want 409", code)
	}

	code, got, _ := bestOfNDo(t, srv, http.MethodGet, "/v1/bestofn/"+dto.ID, "")
	if code != http.StatusOK || got.Decision == nil || got.Candidates[0].Summary != "the race is in Close" {
		t.Fatalf("get = %d %+v", code, got)
	}
	if code, _, _ := bestOfNDo(t, srv, http.MethodGet, "/v1/bestofn/"+dto.ID+"?root=other", ""); code != http.StatusForbidden {
		t.Fatalf("foreign root get = %d, want 403", code)
	}
	if code, _, _ := bestOfNDo(t, srv, http.MethodGet, "/v1/bestofn/nope", ""); code != http.StatusNotFound {
		t.Fatalf("unknown id = %d, want 404", code)
	}

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/bestofn?root=root-a", nil))
	var list struct {
		Runs []bestOfNRunDTO `json:"runs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Runs) != 1 || list.Runs[0].ID != dto.ID {
		t.Fatalf("list = %s (%v)", rec.Body.String(), err)
	}
}

func TestBestOfNOutcome(t *testing.T) {
	passed, failed := true, false
	patch := "diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1 +1,2 @@\n x\n+y\n"
	tests := []struct {
		name     string
		cc       protocol.ChildCompleted
		wantErr  string
		files    int
		added    int
		verified *bool
	}{
		{
			name:  "completed with patch",
			cc:    protocol.ChildCompleted{Status: protocol.ChildStatusCompleted, Handoff: protocol.CompletionHandoff{Summary: "ok", Patch: patch}},
			files: 1, added: 1,
		},
		{
			name:  "files from handoff when no patch",
			cc:    protocol.ChildCompleted{Status: protocol.ChildStatusCompleted, Handoff: protocol.CompletionHandoff{FilesChanged: []string{"a", "b"}}},
			files: 2,
		},
		{
			name:     "verified",
			cc:       protocol.ChildCompleted{Status: protocol.ChildStatusCompleted, Verification: &protocol.VerificationReport{Passed: true}},
			verified: &passed,
		},
		{
			name:     "blocked by failed gate",
			cc:       protocol.ChildCompleted{Status: protocol.ChildStatusBlocked, Verification: &protocol.VerificationReport{Passed: false}},
			wantErr:  "blocked: verification failed",
			verified: &failed,
		},
		{
			name:    "blocked with blockers",
			cc:      protocol.ChildCompleted{Status: protocol.ChildStatusBlocked, Handoff: protocol.CompletionHandoff{Blockers: []string{"no creds"}}},
			wantErr: "blocked: no creds",
		},
		{
			name:    "cancelled elsewhere",
			cc:      protocol.ChildCompleted{Status: protocol.ChildStatusCanceled},
			wantErr: "cancelled",
		},
		{
			name:    "failed on budget",
			cc:      protocol.ChildCompleted{Status: protocol.ChildStatusFailed, Summary: "ran out", BudgetKind: "tokens"},
			wantErr: "failed: ran out (budget: tokens)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := bestOfNOutcome(tt.cc)
			if out.Err != tt.wantErr || out.FilesChanged != tt.files || out.Added != tt.added {
				t.Fatalf("outcome = %+v", out)
			}
			if (out.Verified == nil) != (tt.verified == nil) || (out.Verified != nil && *out.Verified != *tt.verified) {
				t.Fatalf("verified = %v, want %v", out.Verified, tt.verified)
			}
		})
	}
}

type bestOfNRepo struct {
	t    *testing.T
	root string
}

func (r bestOfNRepo) git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", r.root}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func newBestOfNRepo(t *testing.T) bestOfNRepo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	r := bestOfNRepo{t: t, root: t.TempDir()}
	r.git("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(r.root, "alpha.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.root, ".gitignore"), []byte(".strike/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.git("add", "alpha.txt", ".gitignore")
	r.git("commit", "-m", "initial")
	return r
}

// addChildWorktree creates the strike-managed worktree a child would own.
func (r bestOfNRepo) addChildWorktree(id string) string {
	r.t.Helper()
	repo, err := project.MainRoot(context.Background(), r.root)
	if err != nil {
		r.t.Fatal(err)
	}
	path := project.WorktreePath(repo, id)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		r.t.Fatal(err)
	}
	r.git("worktree", "add", "-b", project.BranchName(id), path, "HEAD")
	return path
}

// editsRun starts an edit comparison over the fake spawner with all three
// candidates finished; child-1 carries patch.
func editsRun(t *testing.T, srv *Server, cwd, patch string, worktrees map[string]string) *bestOfNEntry {
	t.Helper()
	e, err := srv.startBestOfN(context.Background(), "root-a", cwd, &fakeBestOfNSpawner{}, bestofn.Request{
		Prompt:     "fix it",
		Candidates: []bestofn.Candidate{{Model: "a"}, {Model: "b"}, {Model: "c"}},
		Edits:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"child-1", "child-2", "child-3"} {
		h := protocol.CompletionHandoff{Summary: id, Isolation: "worktree", WorktreePath: worktrees[id]}
		if id == "child-1" {
			h.Patch = patch
		}
		srv.applyBestOfNChild(e, protocol.ChildCompleted{
			Correlation: protocol.Correlation{SessionID: id},
			Status:      protocol.ChildStatusCompleted,
			Handoff:     h,
		})
	}
	if !e.run.Complete() {
		t.Fatal("run should be complete")
	}
	return e
}

const bestOfNAlphaPatch = "diff --git a/alpha.txt b/alpha.txt\n--- a/alpha.txt\n+++ b/alpha.txt\n@@ -1 +1,2 @@\n one\n+two\n"

func TestBestOfNAdoptAppliesWinnerAndCleansLoserWorktrees(t *testing.T) {
	repo := newBestOfNRepo(t)
	srv := bestOfNServer(t, &fakeBestOfNSpawner{}, false)
	// child-2 reported its worktree; child-3's path is derived from its id.
	wt2 := repo.addChildWorktree("child-2")
	wt3 := repo.addChildWorktree("child-3")
	e := editsRun(t, srv, repo.root, bestOfNAlphaPatch, map[string]string{"child-2": wt2})

	if err := srv.adoptBestOfN(context.Background(), e, "child-1"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(repo.root, "alpha.txt"))
	if err != nil || string(got) != "one\ntwo\n" {
		t.Fatalf("alpha.txt = %q (%v), want the winner's change applied", got, err)
	}
	for _, p := range []string{wt2, wt3} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("loser worktree %s still exists (%v)", p, err)
		}
	}
	branches := repo.git("branch", "--list", "strike/*")
	if strings.TrimSpace(branches) != "" {
		t.Fatalf("candidate branches left behind: %q", branches)
	}
	d, ok := e.run.Decision()
	if !ok || d.Keep != "child-1" {
		t.Fatalf("decision = %+v %v", d, ok)
	}
	dto := srv.bestOfNDTO(e)
	if !strings.Contains(dto.Note, "Removed 3 candidate worktree(s)") || !dto.Candidates[0].HasPatch {
		t.Fatalf("dto = %+v", dto)
	}
}

func TestBestOfNAdoptConflictLeavesRunUndecided(t *testing.T) {
	repo := newBestOfNRepo(t)
	srv := bestOfNServer(t, &fakeBestOfNSpawner{}, false)
	wt2 := repo.addChildWorktree("child-2")
	bad := "diff --git a/alpha.txt b/alpha.txt\n--- a/alpha.txt\n+++ b/alpha.txt\n@@ -1 +1 @@\n-not what is there\n+new\n"
	e := editsRun(t, srv, repo.root, bad, map[string]string{"child-2": wt2})

	err := srv.adoptBestOfN(context.Background(), e, "child-1")
	if err == nil || !strings.Contains(err.Error(), "do not apply") {
		t.Fatalf("adopt err = %v, want apply conflict", err)
	}
	if _, ok := e.run.Decision(); ok {
		t.Fatal("a failed apply must not record a decision")
	}
	if _, err := os.Stat(wt2); err != nil {
		t.Fatalf("worktrees must survive a failed adopt: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(repo.root, "alpha.txt"))
	if string(got) != "one\n" {
		t.Fatalf("alpha.txt = %q, want untouched", got)
	}
	if err := srv.keepNoneBestOfN(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(wt2); !os.IsNotExist(err) {
		t.Fatalf("keep none should remove worktrees (%v)", err)
	}
}

func TestBestOfNWatcher(t *testing.T) {
	newRun := func(t *testing.T, srv *Server, sp *fakeBestOfNSpawner) *bestOfNEntry {
		t.Helper()
		e, err := srv.startBestOfN(context.Background(), "root-a", "", sp, bestofn.Request{
			Prompt:     "p",
			Candidates: []bestofn.Candidate{{Model: "a"}, {Model: "b"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	t.Run("completions finish the run", func(t *testing.T) {
		srv := bestOfNServer(t, &fakeBestOfNSpawner{}, false)
		e := newRun(t, srv, &fakeBestOfNSpawner{})
		sub := make(chan protocol.Event, 4)
		sub <- protocol.ChildCompleted{Correlation: protocol.Correlation{SessionID: "child-1"}, Status: protocol.ChildStatusCompleted}
		sub <- protocol.ChildCompleted{Correlation: protocol.Correlation{SessionID: "child-2"}, Status: protocol.ChildStatusFailed}
		ctx, cancel := context.WithCancel(context.Background())
		srv.watchBestOfN(ctx, cancel, e, sub, 0)
		if ctx.Err() == nil {
			t.Fatal("watcher should release its subscription when done")
		}
		c := e.run.Candidates()
		if !e.run.Complete() || c[0].Status != bestofn.StatusDone || c[1].Status != bestofn.StatusFailed {
			t.Fatalf("candidates = %+v", c)
		}
	})
	t.Run("timeout cancels running candidates", func(t *testing.T) {
		srv := bestOfNServer(t, &fakeBestOfNSpawner{}, false)
		sp := &fakeBestOfNSpawner{}
		e := newRun(t, srv, sp)
		sub := make(chan protocol.Event, 1)
		sub <- protocol.ChildCompleted{Correlation: protocol.Correlation{SessionID: "child-1"}, Status: protocol.ChildStatusCompleted}
		ctx, cancel := context.WithCancel(context.Background())
		srv.watchBestOfN(ctx, cancel, e, sub, 50*time.Millisecond)
		c := e.run.Candidates()
		if !e.run.Complete() || c[0].Status != bestofn.StatusDone || c[1].Status != bestofn.StatusCancelled {
			t.Fatalf("candidates = %+v", c)
		}
		if len(sp.cancelled) != 1 || sp.cancelled[0] != "child-2" {
			t.Fatalf("cancelled = %v", sp.cancelled)
		}
		if !strings.Contains(srv.bestOfNDTO(e).Note, "time budget") {
			t.Fatalf("note = %q", srv.bestOfNDTO(e).Note)
		}
	})
}

func TestBestOfNAdoptRefusesUncapturedWinnerChanges(t *testing.T) {
	repo := newBestOfNRepo(t)
	srv := bestOfNServer(t, &fakeBestOfNSpawner{}, false)
	wt2 := repo.addChildWorktree("child-2")
	e := editsRun(t, srv, repo.root, "", map[string]string{"child-2": wt2})
	// child-1 reported edits but carried no patch (export failed / too big).
	e2, err := srv.startBestOfN(context.Background(), "root-a", repo.root, &fakeBestOfNSpawner{}, bestofn.Request{
		Prompt:     "fix it",
		Candidates: []bestofn.Candidate{{Model: "a"}, {Model: "b"}},
		Edits:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv.applyBestOfNChild(e2, protocol.ChildCompleted{
		Correlation: protocol.Correlation{SessionID: "child-1"},
		Status:      protocol.ChildStatusCompleted,
		Handoff:     protocol.CompletionHandoff{FilesChanged: []string{"alpha.txt"}},
	})
	_ = e2.run.Cancel("child-2")
	if err := srv.adoptBestOfN(context.Background(), e2, "child-1"); err == nil || !strings.Contains(err.Error(), "not captured") {
		t.Fatalf("adopt err = %v, want refusal", err)
	}
	if _, ok := e2.run.Decision(); ok {
		t.Fatal("refused adopt must not record a decision")
	}
	// A winner with no file changes adopts fine (answer-only result).
	if err := srv.adoptBestOfN(context.Background(), e, "child-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(wt2); !os.IsNotExist(err) {
		t.Fatalf("loser worktree should be removed (%v)", err)
	}
}
