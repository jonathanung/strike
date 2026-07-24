package project

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestResolveRejectsInvalidCWD(t *testing.T) {
	tests := []struct {
		name string
		cwd  string
	}{
		{name: "empty", cwd: ""},
		{name: "nonexistent", cwd: filepath.Join(t.TempDir(), "missing")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Resolve(context.Background(), tt.cwd); err == nil {
				t.Fatalf("Resolve(%q) error = nil, want an error", tt.cwd)
			}
		})
	}
}

func TestResolveFallsBackForNonGitDirectory(t *testing.T) {
	requireGit(t)
	cwd := t.TempDir()

	got, err := Resolve(context.Background(), cwd)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	want := canonicalPath(t, cwd)
	if got.Root != want || got.Key != want {
		t.Errorf("Resolve() = %+v, want Root and Key %q", got, want)
	}
}

func TestResolveCanonicalizesSymlinkedCWD(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks is not generally available on Windows")
	}

	physical := t.TempDir()
	link := filepath.Join(t.TempDir(), "project-link")
	if err := os.Symlink(physical, link); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	t.Setenv("PATH", "")

	got, err := Resolve(context.Background(), link)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	want := canonicalPath(t, physical)
	if got.Root != want || got.Key != want {
		t.Errorf("Resolve() = %+v, want physical Root and Key %q", got, want)
	}
}

func TestResolveGitRootAndStableKeyWithoutChangingCWD(t *testing.T) {
	git := requireGit(t)
	repo := t.TempDir()
	runGit(t, git, "init", "--quiet", repo)
	subdir := filepath.Join(repo, "a", "b")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	originalCWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	fromRoot, err := Resolve(context.Background(), repo)
	if err != nil {
		t.Fatalf("Resolve(repo) error = %v", err)
	}
	fromSubdir, err := Resolve(context.Background(), subdir)
	if err != nil {
		t.Fatalf("Resolve(subdir) error = %v", err)
	}
	want := canonicalPath(t, repo)
	if fromRoot.Root != want || fromSubdir.Root != want {
		t.Errorf("roots = %q and %q, want %q", fromRoot.Root, fromSubdir.Root, want)
	}
	if fromRoot.Key != want || fromSubdir.Key != want || fromRoot.Key != fromSubdir.Key {
		t.Errorf("keys = %q and %q, want stable key %q", fromRoot.Key, fromSubdir.Key, want)
	}
	if after, err := os.Getwd(); err != nil {
		t.Fatal(err)
	} else if after != originalCWD {
		t.Errorf("process cwd changed from %q to %q", originalCWD, after)
	}
}

func TestResolveGitDirectoryFile(t *testing.T) {
	git := requireGit(t)
	repo := t.TempDir()
	runGit(t, git, "init", "--quiet", repo)
	gitDir := filepath.Join(t.TempDir(), "project.git")
	if err := os.Rename(filepath.Join(repo, ".git"), gitDir); err != nil {
		t.Fatalf("moving Git directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".git"), []byte("gitdir: "+gitDir+"\n"), 0o644); err != nil {
		t.Fatalf("writing .git file: %v", err)
	}
	subdir := filepath.Join(repo, "nested")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := Resolve(context.Background(), subdir)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	want := canonicalPath(t, repo)
	if got.Root != want || got.Key != want {
		t.Errorf("Resolve() = %+v, want worktree Root and Key %q", got, want)
	}
}

func TestResolveFallsBackWhenGitCannotRun(t *testing.T) {
	t.Run("missing executable", func(t *testing.T) {
		cwd := t.TempDir()
		t.Setenv("PATH", "")

		assertFallback(t, context.Background(), cwd)
	})

	t.Run("failed command", func(t *testing.T) {
		cwd := t.TempDir()
		bin := fakeGit(t, "#!/bin/sh\nexit 42\n")
		t.Setenv("PATH", bin)

		assertFallback(t, context.Background(), cwd)
	})
}

func TestResolveCanceledContextReturnsPromptly(t *testing.T) {
	cwd := t.TempDir()
	bin := fakeGit(t, "#!/bin/sh\nexec /bin/sleep 30\n")
	t.Setenv("PATH", bin)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	started := time.Now()
	assertFallback(t, ctx, cwd)
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Errorf("Resolve() took %v with canceled context, want at most 1s", elapsed)
	}
}

func TestResolveTimesOutGitAndFallsBack(t *testing.T) {
	cwd := t.TempDir()
	bin := fakeGit(t, "#!/bin/sh\nexec /bin/sleep 30\n")
	t.Setenv("PATH", bin)

	started := time.Now()
	assertFallback(t, context.Background(), cwd)
	if elapsed := time.Since(started); elapsed < 350*time.Millisecond || elapsed > 1500*time.Millisecond {
		t.Errorf("Resolve() took %v for a hung Git command, want approximately the 500ms timeout", elapsed)
	}
}

func assertFallback(t *testing.T, ctx context.Context, cwd string) {
	t.Helper()
	got, err := Resolve(ctx, cwd)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	want := canonicalPath(t, cwd)
	if got.Root != want || got.Key != want {
		t.Errorf("Resolve() = %+v, want fallback Root and Key %q", got, want)
	}
}

func canonicalPath(t *testing.T, path string) string {
	t.Helper()
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	physical, err := filepath.EvalSymlinks(abs)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(physical)
}

func requireGit(t *testing.T) string {
	t.Helper()
	git, err := exec.LookPath("git")
	if errors.Is(err, exec.ErrNotFound) {
		t.Skip("git executable is unavailable")
	}
	if err != nil {
		t.Fatalf("looking up git: %v", err)
	}
	return git
}

func runGit(t *testing.T, git string, args ...string) {
	t.Helper()
	if output, err := exec.Command(git, args...).CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, output)
	}
}

func fakeGit(t *testing.T, contents string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("executable shell fixtures are not available on Windows")
	}
	bin := t.TempDir()
	path := filepath.Join(bin, "git")
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}
