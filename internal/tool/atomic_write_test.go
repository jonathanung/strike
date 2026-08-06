package tool

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicWriteFileCreatesAndOverwrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := atomicWriteFile(path, []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "one" {
		t.Fatalf("got %q err=%v", got, err)
	}
	if err := atomicWriteFile(path, []byte("two"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(path)
	if err != nil || string(got) != "two" {
		t.Fatalf("got %q err=%v", got, err)
	}
	// No leftover temp files.
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 1 || ents[0].Name() != "a.txt" {
		t.Fatalf("ents = %v", ents)
	}
}

func TestAtomicWriteFilePreservesMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mode.txt")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := atomicWriteFile(path, []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 0600", fi.Mode().Perm())
	}
}

func TestAtomicWriteFileRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "t")
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink: %v", err)
	}
	err := atomicWriteFile(link, []byte("nope"), 0o644)
	if err == nil {
		t.Fatal("want symlink error")
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("outside written: %v", statErr)
	}
}

func TestWorkspaceWriteFileAtomicNoTemps(t *testing.T) {
	root := t.TempDir()
	if err := workspaceWriteFile(root, "nested/f.txt", []byte("hello")); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, "nested", "f.txt"))
	if err != nil || string(got) != "hello" {
		t.Fatalf("got %q err=%v", got, err)
	}
	ents, err := os.ReadDir(filepath.Join(root, "nested"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if len(e.Name()) > 8 && e.Name()[:8] == ".strike-" {
			t.Fatalf("leftover temp %q", e.Name())
		}
	}
}
