package project

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeWorktreeMode(t *testing.T) {
	cases := map[string]string{
		"":        WorktreeOff,
		"off":     WorktreeOff,
		"OFF":     WorktreeOff,
		"auto":    WorktreeAuto,
		" Always": WorktreeAlways,
		"nope":    WorktreeOff,
	}
	for in, want := range cases {
		if got := NormalizeWorktreeMode(in); got != want {
			t.Errorf("NormalizeWorktreeMode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeCleanup(t *testing.T) {
	if got := NormalizeCleanup(""); got != CleanupKeep {
		t.Errorf("default = %q", got)
	}
	if got := NormalizeCleanup("delete"); got != CleanupDelete {
		t.Errorf("delete = %q", got)
	}
	if got := NormalizeCleanup("KEEP"); got != CleanupKeep {
		t.Errorf("KEEP = %q", got)
	}
}

func TestWantWorktree(t *testing.T) {
	tests := []struct {
		mode  string
		force bool
		open  int
		want  bool
	}{
		{mode: "off", open: 0, want: false},
		{mode: "off", open: 5, want: false},
		{mode: "off", force: true, want: true},
		{mode: "always", open: 0, want: true},
		{mode: "auto", open: 0, want: false},
		{mode: "auto", open: 1, want: true},
		{mode: "auto", open: 2, want: true},
	}
	for _, tt := range tests {
		if got := WantWorktree(tt.mode, tt.force, tt.open); got != tt.want {
			t.Errorf("WantWorktree(%q, force=%v, open=%d) = %v, want %v",
				tt.mode, tt.force, tt.open, got, tt.want)
		}
	}
}

func TestWorktreePathAndBranch(t *testing.T) {
	got := WorktreePath("/repo", "sess-1")
	want := filepath.Join("/repo", ".strike", "worktrees", "sess-1")
	if got != want {
		t.Errorf("WorktreePath = %q, want %q", got, want)
	}
	if br := BranchName("sess-1"); br != "strike/sess-1" {
		t.Errorf("BranchName = %q", br)
	}
}

func TestAddRemoveIsolation(t *testing.T) {
	git := requireGit(t)
	repo := initRepo(t, git)

	// Seed a tracked file in the main tree.
	mainFile := filepath.Join(repo, "shared.txt")
	if err := os.WriteFile(mainFile, []byte("main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, git, "-C", repo, "add", "shared.txt")
	runGit(t, git, "-C", repo, "commit", "--quiet", "-m", "seed")

	ctx := context.Background()
	wt1, err := Add(ctx, repo, "sess-a")
	if err != nil {
		t.Fatalf("Add sess-a: %v", err)
	}
	wt2, err := Add(ctx, repo, "sess-b")
	if err != nil {
		t.Fatalf("Add sess-b: %v", err)
	}
	if wt1.Path == wt2.Path {
		t.Fatal("worktrees share path")
	}
	if wt1.RepoRoot != canonicalPath(t, repo) {
		t.Errorf("RepoRoot = %q, want %q", wt1.RepoRoot, repo)
	}

	// Edit same relative path in each worktree — no clobber.
	f1 := filepath.Join(wt1.Path, "shared.txt")
	f2 := filepath.Join(wt2.Path, "shared.txt")
	if err := os.WriteFile(f1, []byte("from-a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f2, []byte("from-b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mainBytes, err := os.ReadFile(mainFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(mainBytes) != "main\n" {
		t.Errorf("primary checkout clobbered: %q", mainBytes)
	}
	aBytes, _ := os.ReadFile(f1)
	bBytes, _ := os.ReadFile(f2)
	if string(aBytes) != "from-a\n" || string(bBytes) != "from-b\n" {
		t.Errorf("worktree contents = %q / %q", aBytes, bBytes)
	}

	// Tools resolve inside session worktree: relative write stays there.
	nested := filepath.Join(wt1.Path, "pkg", "x.go")
	if err := os.MkdirAll(filepath.Dir(nested), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nested, []byte("package pkg\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repo, "pkg", "x.go")); !os.IsNotExist(err) {
		t.Errorf("file leaked into primary checkout: %v", err)
	}

	if err := Remove(ctx, wt1.RepoRoot, wt1.Path, wt1.Branch); err != nil {
		t.Fatalf("Remove wt1: %v", err)
	}
	if _, err := os.Stat(wt1.Path); !os.IsNotExist(err) {
		t.Errorf("wt1 path still exists: %v", err)
	}
	// Primary checkout untouched.
	if _, err := os.Stat(mainFile); err != nil {
		t.Fatalf("primary file missing after remove: %v", err)
	}
	if err := Remove(ctx, wt2.RepoRoot, wt2.Path, wt2.Branch); err != nil {
		t.Fatalf("Remove wt2: %v", err)
	}
}

func TestAddNotAGitRepo(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	_, err := Add(context.Background(), dir, "s1")
	if err == nil {
		t.Fatal("Add non-git: want error")
	}
	if !strings.Contains(err.Error(), "not a git repository") {
		t.Errorf("error = %v", err)
	}
}

func TestAddRejectsBadSessionID(t *testing.T) {
	git := requireGit(t)
	repo := initRepo(t, git)
	runGit(t, git, "-C", repo, "commit", "--quiet", "--allow-empty", "-m", "init")

	_, err := Add(context.Background(), repo, "../escape")
	if err == nil {
		t.Fatal("want error for path segment")
	}
}

func TestAddDuplicatePathFailsClean(t *testing.T) {
	git := requireGit(t)
	repo := initRepo(t, git)
	runGit(t, git, "-C", repo, "commit", "--quiet", "--allow-empty", "-m", "init")

	ctx := context.Background()
	wt, err := Add(ctx, repo, "dup")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = Remove(context.Background(), wt.RepoRoot, wt.Path, wt.Branch) })

	_, err = Add(ctx, repo, "dup")
	if err == nil {
		t.Fatal("second Add: want error")
	}
}

func TestRemoveRefusesPrimaryCheckout(t *testing.T) {
	git := requireGit(t)
	repo := initRepo(t, git)
	runGit(t, git, "-C", repo, "commit", "--quiet", "--allow-empty", "-m", "init")
	root := canonicalPath(t, repo)
	err := Remove(context.Background(), root, root, "")
	if err == nil {
		t.Fatal("want refuse primary")
	}
	if !strings.Contains(err.Error(), "refusing") {
		t.Errorf("error = %v", err)
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("primary deleted: %v", err)
	}
}

func TestRemoveIdempotentMissing(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "gone")
	if err := Remove(context.Background(), dir, missing, ""); err != nil {
		t.Fatal(err)
	}
}

func TestMainRootFromLinkedWorktree(t *testing.T) {
	git := requireGit(t)
	repo := initRepo(t, git)
	runGit(t, git, "-C", repo, "commit", "--quiet", "--allow-empty", "-m", "init")

	ctx := context.Background()
	wt, err := Add(ctx, repo, "linked")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = Remove(context.Background(), wt.RepoRoot, wt.Path, wt.Branch) })

	main, err := MainRoot(ctx, wt.Path)
	if err != nil {
		t.Fatal(err)
	}
	want := canonicalPath(t, repo)
	if main != want {
		t.Errorf("MainRoot from worktree = %q, want %q", main, want)
	}
}

func initRepo(t *testing.T, git string) string {
	t.Helper()
	repo := t.TempDir()
	runGit(t, git, "-C", repo, "init", "--quiet")
	runGit(t, git, "-C", repo, "config", "user.email", "strike@test")
	runGit(t, git, "-C", repo, "config", "user.name", "strike")
	return repo
}
