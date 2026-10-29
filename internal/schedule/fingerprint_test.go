package schedule

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestFingerprintChangesWhenFileChanges(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	first, err := Fingerprint(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Fingerprint(dir)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("fingerprint not stable")
	}
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	third, err := Fingerprint(dir)
	if err != nil {
		t.Fatal(err)
	}
	if third == first {
		t.Fatal("edit did not change fingerprint")
	}
}

func TestFingerprintGitStatus(t *testing.T) {
	if _, err := os.Stat("/usr/bin/git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init")
	run("config", "user.email", "strike@example.com")
	run("config", "user.name", "strike")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "f.txt")
	run("commit", "-m", "init")
	first, err := Fingerprint(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := Fingerprint(dir)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("unstaged edit did not change git fingerprint")
	}
}
