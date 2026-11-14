package workspacetrust

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestOpenMissingFileIsEmptyStore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := DefaultPath(filepath.Join(home, ".strike"))
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.Trusted(t.TempDir()) {
		t.Fatal("fresh store must trust nothing")
	}
	if got := s.Roots(); len(got) != 0 {
		t.Fatalf("Roots = %v, want empty", got)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Open must not create the file; stat err = %v", err)
	}
}

func TestOpenRejectsEmptyPathAndMalformedFile(t *testing.T) {
	if _, err := Open(""); err == nil {
		t.Fatal("empty path must fail")
	}
	dir := t.TempDir()
	bad := filepath.Join(dir, FileName)
	if err := os.WriteFile(bad, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(bad); err == nil {
		t.Fatal("malformed file must fail closed")
	}
	newer := filepath.Join(dir, "newer.json")
	if err := os.WriteFile(newer, []byte(`{"version":99,"workspaces":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(newer); err == nil || !strings.Contains(err.Error(), "newer") {
		t.Fatalf("newer version must fail, got %v", err)
	}
}

func TestTrustRevokePersistsAcrossOpen(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := DefaultPath(filepath.Join(home, ".strike"))
	repo := t.TempDir()

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	fixed := time.Date(2026, 11, 14, 7, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return fixed }
	if err := s.Trust(repo); err != nil {
		t.Fatal(err)
	}
	if !s.Trusted(repo) {
		t.Fatal("Trusted after Trust = false")
	}
	rec, ok := s.Record(repo)
	if !ok || rec.TrustedAt != "2026-11-14T07:00:00Z" {
		t.Fatalf("Record = %+v, %v", rec, ok)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("store perm = %o, want 0600", perm)
		}
		dirInfo, err := os.Stat(filepath.Dir(path))
		if err != nil {
			t.Fatal(err)
		}
		if perm := dirInfo.Mode().Perm(); perm != 0o700 {
			t.Fatalf("dir perm = %o, want 0700", perm)
		}
	}
	// No temp residue after the atomic rename.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}

	// On-disk shape: canonical key, version 1.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var f file
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	canon, _ := CanonicalRoot(repo)
	if f.Version != 1 {
		t.Fatalf("version = %d", f.Version)
	}
	if _, ok := f.Workspaces[canon]; !ok {
		t.Fatalf("workspaces = %v, want key %q", f.Workspaces, canon)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reopened.Trusted(repo) {
		t.Fatal("trust did not survive reopen")
	}
	if got := reopened.Roots(); len(got) != 1 || got[0] != canon {
		t.Fatalf("Roots = %v", got)
	}

	if err := reopened.Revoke(repo); err != nil {
		t.Fatal(err)
	}
	if reopened.Trusted(repo) {
		t.Fatal("Trusted after Revoke = true")
	}
	// Revoking again is a no-op.
	if err := reopened.Revoke(repo); err != nil {
		t.Fatal(err)
	}
	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if again.Trusted(repo) {
		t.Fatal("revoke did not persist")
	}
}

func TestTrustIsExactRootNotAncestor(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	s, err := Open(DefaultPath(filepath.Join(home, ".strike")))
	if err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	child := filepath.Join(parent, "repo")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.Trust(parent); err != nil {
		t.Fatal(err)
	}
	if s.Trusted(child) {
		t.Fatal("child of a trusted parent must not inherit trust")
	}
	if err := s.Trust(child); err != nil {
		t.Fatal(err)
	}
	if !s.Trusted(child) {
		t.Fatal("child trust missing")
	}
}

func TestTrustResolvesSymlinksAndRelativePaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	s, err := Open(DefaultPath(filepath.Join(home, ".strike")))
	if err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := s.Trust(link); err != nil {
		t.Fatal(err)
	}
	if !s.Trusted(real) {
		t.Fatal("trust via symlink must match the real path")
	}
	// Relative path resolves against cwd.
	t.Chdir(base)
	if !s.Trusted("real") || !s.Trusted("./link") {
		t.Fatal("relative roots must canonicalize")
	}
}

func TestTrustRefusesHomeAndFilesystemRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	s, err := Open(DefaultPath(filepath.Join(home, ".strike")))
	if err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{home, string(filepath.Separator)} {
		err := s.Trust(root)
		if !errors.Is(err, ErrUntrustable) {
			t.Fatalf("Trust(%q) err = %v, want ErrUntrustable", root, err)
		}
		if s.Trusted(root) {
			t.Fatalf("Trusted(%q) = true after refused grant", root)
		}
	}
	if _, err := os.Stat(s.Path()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refused grant must not write the store; stat err = %v", err)
	}
	// A directory under home is fine.
	if err := s.Trust(filepath.Join(home, "proj")); err != nil {
		t.Fatalf("Trust(home/proj) = %v", err)
	}
}

func TestTrustEmptyRootErrors(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	s, err := Open(DefaultPath(filepath.Join(home, ".strike")))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Trust(""); err == nil {
		t.Fatal("Trust(\"\") must fail")
	}
	if s.Trusted("") {
		t.Fatal("Trusted(\"\") must be false")
	}
	if err := s.Revoke(""); err == nil {
		t.Fatal("Revoke(\"\") must fail")
	}
}

func TestStoreConcurrentAccess(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	s, err := Open(DefaultPath(filepath.Join(home, ".strike")))
	if err != nil {
		t.Fatal(err)
	}
	roots := make([]string, 8)
	for i := range roots {
		roots[i] = t.TempDir()
	}
	var wg sync.WaitGroup
	for _, r := range roots {
		wg.Add(1)
		go func(root string) {
			defer wg.Done()
			if err := s.Trust(root); err != nil {
				t.Error(err)
			}
			_ = s.Trusted(root)
			_ = s.Roots()
		}(r)
	}
	wg.Wait()
	if got := len(s.Roots()); got != len(roots) {
		t.Fatalf("Roots len = %d, want %d", got, len(roots))
	}
	reopened, err := Open(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range roots {
		if !reopened.Trusted(r) {
			t.Fatalf("%s lost after concurrent writes", r)
		}
	}
}

func TestDefaultPath(t *testing.T) {
	if got := DefaultPath(""); got != "" {
		t.Fatalf("DefaultPath(\"\") = %q", got)
	}
	if got := DefaultPath("/x/.strike"); got != filepath.Join("/x/.strike", FileName) {
		t.Fatalf("DefaultPath = %q", got)
	}
}
