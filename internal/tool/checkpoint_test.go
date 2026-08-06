package tool

import (
	"fmt"
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
	s.MarkUncovered("bash")
	s.CommitTurn()
	if _, err := s.Pop(true); err != nil {
		t.Fatal(err)
	}
	if s.PeekHasFiles() {
		t.Fatal("nil PeekHasFiles")
	}
	if !s.Peek().Empty {
		t.Fatal("nil Peek should be empty")
	}
}

func TestCheckpointMultiFileRestoreOrder(t *testing.T) {
	dir := t.TempDir()
	// Lexically reverse create order so restore sort is observable.
	paths := []string{
		filepath.Join(dir, "z.txt"),
		filepath.Join(dir, "m.txt"),
		filepath.Join(dir, "a.txt"),
	}
	for i, p := range paths {
		if err := os.WriteFile(p, []byte(fmt.Sprintf("v0-%d\n", i)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s := NewCheckpointStore()
	s.BeginTurn("t-multi")
	for _, p := range paths {
		s.Snapshot(p)
	}
	for i, p := range paths {
		if err := os.WriteFile(p, []byte(fmt.Sprintf("v1-%d\n", i)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s.CommitTurn()

	peek := s.Peek()
	if peek.Empty || peek.TurnID != "t-multi" || len(peek.Restorable) != 3 {
		t.Fatalf("Peek = %+v", peek)
	}
	wantOrder := []string{
		filepath.Join(dir, "a.txt"),
		filepath.Join(dir, "m.txt"),
		filepath.Join(dir, "z.txt"),
	}
	for i, p := range wantOrder {
		if peek.Restorable[i] != p {
			t.Fatalf("Peek.Restorable[%d] = %q, want %q", i, peek.Restorable[i], p)
		}
	}

	res, err := s.Pop(true)
	if err != nil {
		t.Fatal(err)
	}
	if res.RestoredN != 3 {
		t.Fatalf("RestoredN = %d", res.RestoredN)
	}
	for i, p := range wantOrder {
		if res.Restored[i] != p {
			t.Fatalf("Restored[%d] = %q, want %q (stable path order)", i, res.Restored[i], p)
		}
		got, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		// Original content was written in reverse index order.
		origIdx := map[string]int{
			filepath.Join(dir, "z.txt"): 0,
			filepath.Join(dir, "m.txt"): 1,
			filepath.Join(dir, "a.txt"): 2,
		}[p]
		want := fmt.Sprintf("v0-%d\n", origIdx)
		if string(got) != want {
			t.Fatalf("%s = %q, want %q", p, got, want)
		}
	}
}

func TestCheckpointMarkUncovered(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("v0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewCheckpointStore()
	s.BeginTurn("t1")
	s.Snapshot(path)
	s.MarkUncovered("bash")
	s.MarkUncovered("bash") // collapse
	s.MarkUncovered("  ")
	if err := os.WriteFile(path, []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.CommitTurn()

	peek := s.Peek()
	if len(peek.Uncovered) != 1 || peek.Uncovered[0] != "bash" {
		t.Fatalf("Peek.Uncovered = %#v", peek.Uncovered)
	}
	res, err := s.Pop(true)
	if err != nil {
		t.Fatal(err)
	}
	if res.RestoredN != 1 {
		t.Fatalf("RestoredN = %d", res.RestoredN)
	}
	if len(res.Uncovered) != 1 || res.Uncovered[0] != "bash" {
		t.Fatalf("Pop.Uncovered = %#v", res.Uncovered)
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
		WorkDir:             dir,
		Checkpoint:          store.Snapshot,
		CheckpointUncovered: store.MarkUncovered,
	}
	tc.SnapshotPath(path)
	tc.MarkUncovered("bash")
	store.CommitTurn()
	if !store.PeekHasFiles() {
		t.Fatal("expected snapshot via Context")
	}
	peek := store.Peek()
	if len(peek.Uncovered) != 1 || peek.Uncovered[0] != "bash" {
		t.Fatalf("Peek.Uncovered = %#v", peek.Uncovered)
	}
}
