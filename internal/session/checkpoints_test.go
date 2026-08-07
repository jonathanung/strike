package session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckpointDirAndRemove(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := CheckpointDir("abc")
	want := filepath.Join(home, ".strike", "checkpoints", "abc")
	if dir != want {
		t.Fatalf("CheckpointDir = %q, want %q", dir, want)
	}
	if err := os.MkdirAll(filepath.Join(dir, "blobs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := RemoveCheckpoints("abc"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("still exists: %v", err)
	}
}

func TestDestroyRemovesCheckpoints(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	sessDir := t.TempDir()
	m := NewManager(sessDir)
	info, err := m.Create(CreateOptions{Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	cp := CheckpointDir(info.ID)
	if err := os.MkdirAll(cp, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cp, "stack.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.Destroy(info.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cp); !os.IsNotExist(err) {
		t.Fatalf("checkpoints remain: %v", err)
	}
}
