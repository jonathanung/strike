package tool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFileStateNilReceiverIsNoop(t *testing.T) {
	var s *FileState
	s.Record("/x", nil)
	s.MarkDirty("/x")
	if s.Hash("/x") != "" {
		t.Fatal("nil Hash should be empty")
	}
	if err := s.CheckFresh("/x", "x"); err != nil {
		t.Fatal(err)
	}
}

func TestEditRejectsStaleReadAfterExternalChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("hello world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	state := &FileState{}
	tc := allowAll(dir)
	tc.Files = state

	if _, err := NewRead().Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath": "a.txt",
	}), tc); err != nil {
		t.Fatal(err)
	}

	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(path, []byte("hello world\nchanged\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := NewEdit().Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath":  "a.txt",
		"oldString": "hello",
		"newString": "hi",
	}), tc)
	if err == nil || !strings.Contains(err.Error(), "modified externally") {
		t.Fatalf("want stale error, got %v", err)
	}
	if CodeOf(err) != string(CodePreconditionFailed) {
		t.Fatalf("code = %q, want %s", CodeOf(err), CodePreconditionFailed)
	}

	// MarkDirty (as FilesChanged would) then re-read and edit succeeds.
	state.MarkDirty(path)
	if _, err := NewRead().Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath": "a.txt",
	}), tc); err != nil {
		t.Fatal(err)
	}
	if _, err := NewEdit().Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath":  "a.txt",
		"oldString": "hello",
		"newString": "hi",
	}), tc); err != nil {
		t.Fatal(err)
	}
}

func TestWriteRejectsStaleReadAfterMarkDirty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "b.txt")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	state := &FileState{}
	tc := allowAll(dir)
	tc.Files = state

	if _, err := NewRead().Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath": "b.txt",
	}), tc); err != nil {
		t.Fatal(err)
	}

	state.MarkDirty(path)

	_, err := NewWrite().Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath": "b.txt",
		"content":  "agent",
	}), tc)
	if err == nil || !strings.Contains(err.Error(), "modified externally") {
		t.Fatalf("want stale error, got %v", err)
	}

	if _, err := NewRead().Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath": "b.txt",
	}), tc); err != nil {
		t.Fatal(err)
	}
	if _, err := NewWrite().Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath": "b.txt",
		"content":  "agent",
	}), tc); err != nil {
		t.Fatal(err)
	}
}

func TestMarkDirtyWithoutPriorReadDoesNotBlockEdit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fresh.txt")
	if err := os.WriteFile(path, []byte("one two"), 0o644); err != nil {
		t.Fatal(err)
	}
	state := &FileState{}
	tc := allowAll(dir)
	tc.Files = state
	state.MarkDirty(path)
	if _, err := NewEdit().Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath":  "fresh.txt",
		"oldString": "one",
		"newString": "1",
	}), tc); err != nil {
		t.Fatal(err)
	}
}

func TestEditWithoutPriorReadStillWorks(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "c.txt"), []byte("one two"), 0o644); err != nil {
		t.Fatal(err)
	}
	tc := allowAll(dir)
	tc.Files = &FileState{}
	if _, err := NewEdit().Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath":  "c.txt",
		"oldString": "one",
		"newString": "1",
	}), tc); err != nil {
		t.Fatal(err)
	}
}
