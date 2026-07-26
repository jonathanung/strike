package tool

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckpointSnapshotRestoreEdit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewCheckpointStore()
	s.BeginTurn("t1")
	s.Snapshot(path)
	if err := os.WriteFile(path, []byte("after\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.CommitTurn()

	res, err := s.Pop(true)
	if err != nil {
		t.Fatal(err)
	}
	if res.TurnID != "t1" || res.RestoredN != 1 {
		t.Fatalf("Pop = %+v", res)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "before\n" {
		t.Fatalf("content = %q, want before", got)
	}
}

func TestCheckpointRestoreRemovesCreatedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new.txt")
	s := NewCheckpointStore()
	s.BeginTurn("t1")
	s.Snapshot(path) // missing
	if err := os.WriteFile(path, []byte("created\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.CommitTurn()

	res, err := s.Pop(true)
	if err != nil {
		t.Fatal(err)
	}
	if res.RestoredN != 1 {
		t.Fatalf("RestoredN = %d", res.RestoredN)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("file still exists: %v", err)
	}
}

func TestCheckpointPopWithoutRestoreKeepsDisk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewCheckpointStore()
	s.BeginTurn("t1")
	s.Snapshot(path)
	if err := os.WriteFile(path, []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.CommitTurn()

	res, err := s.Pop(false)
	if err != nil {
		t.Fatal(err)
	}
	if res.RestoredN != 0 || !res.HadFiles {
		t.Fatalf("Pop chat-only = %+v", res)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "v2\n" {
		t.Fatalf("disk = %q, want v2", got)
	}
}

func TestCheckpointFirstTouchWins(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("orig\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewCheckpointStore()
	s.BeginTurn("t1")
	s.Snapshot(path)
	if err := os.WriteFile(path, []byte("mid\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.Snapshot(path) // second touch must keep orig
	if err := os.WriteFile(path, []byte("end\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.CommitTurn()
	if _, err := s.Pop(true); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "orig\n" {
		t.Fatalf("got %q", got)
	}
}

func TestCheckpointSkipsHugeFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.bin")
	// Write a small file then set maxBytes tiny via direct field.
	if err := os.WriteFile(path, []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewCheckpointStore()
	s.maxBytes = 4
	s.BeginTurn("t1")
	s.Snapshot(path)
	if err := os.WriteFile(path, []byte("changed!!"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.CommitTurn()
	res, err := s.Pop(true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Skipped != 1 || res.RestoredN != 0 {
		t.Fatalf("Pop = %+v", res)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "changed!!" {
		t.Fatalf("huge file should not restore, got %q", got)
	}
}

func TestCheckpointTurnAlignment(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(a, []byte("a0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewCheckpointStore()

	// Turn 1 mutates a.
	s.BeginTurn("t1")
	s.Snapshot(a)
	if err := os.WriteFile(a, []byte("a1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.CommitTurn()

	// Turn 2 is chat-only (empty frame).
	s.BeginTurn("t2")
	s.CommitTurn()

	// Undo turn 2 with restore must not touch a.
	res, err := s.Pop(true)
	if err != nil {
		t.Fatal(err)
	}
	if res.TurnID != "t2" || res.RestoredN != 0 {
		t.Fatalf("undo t2 = %+v", res)
	}
	got, _ := os.ReadFile(a)
	if string(got) != "a1\n" {
		t.Fatalf("after t2 undo = %q", got)
	}

	// Undo turn 1 restores a.
	res, err = s.Pop(true)
	if err != nil {
		t.Fatal(err)
	}
	if res.TurnID != "t1" || res.RestoredN != 1 {
		t.Fatalf("undo t1 = %+v", res)
	}
	got, _ = os.ReadFile(a)
	if string(got) != "a0\n" {
		t.Fatalf("after t1 undo = %q", got)
	}
}

func TestCheckpointNilSafe(t *testing.T) {
	var s *CheckpointStore
	s.BeginTurn("t")
	s.Snapshot("/x")
	s.CommitTurn()
	if _, err := s.Pop(true); err != nil {
		t.Fatal(err)
	}
	if s.PeekHasFiles() {
		t.Fatal("nil PeekHasFiles")
	}
}

func TestContextSnapshotHelper(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := NewCheckpointStore()
	store.BeginTurn("t")
	tc := &Context{
		WorkDir:    dir,
		Checkpoint: store.Snapshot,
	}
	tc.SnapshotPath(path)
	store.CommitTurn()
	if !store.PeekHasFiles() {
		t.Fatal("expected snapshot via Context")
	}
}
