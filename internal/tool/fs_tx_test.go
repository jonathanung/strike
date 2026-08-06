package tool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestEditBaseHashPrecondition(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "h.txt")
	content := []byte("alpha beta\n")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	tc := allowAll(dir)
	good := ContentHash(content)
	// Wrong hash fails closed.
	_, err := NewEdit().Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath":  "h.txt",
		"oldString": "alpha",
		"newString": "ALPHA",
		"baseHash":  strings.Repeat("0", 64),
	}), tc)
	if err == nil || CodeOf(err) != string(CodePreconditionFailed) {
		t.Fatalf("want precondition_failed, got %v (code=%q)", err, CodeOf(err))
	}
	// Matching hash succeeds.
	if _, err := NewEdit().Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath":  "h.txt",
		"oldString": "alpha",
		"newString": "ALPHA",
		"baseHash":  good,
	}), tc); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), "ALPHA") {
		t.Fatalf("content = %q", got)
	}
}

func TestEditBaseHashInvalidArgs(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "h.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	tc := allowAll(dir)
	_, err := NewEdit().Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath":  "h.txt",
		"oldString": "x",
		"newString": "y",
		"baseHash":  "not-a-hash",
	}), tc)
	if err == nil || CodeOf(err) != string(CodeInvalidArgs) {
		t.Fatalf("want invalid_args, got %v", err)
	}
}

func TestApplyPatchBaseHashes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "p.txt")
	orig := []byte("line1\nold\nline3\n")
	if err := os.WriteFile(path, orig, 0o644); err != nil {
		t.Fatal(err)
	}
	tc := allowAll(dir)
	patch := "*** Begin Patch\n*** Update File: p.txt\n@@\n line1\n-old\n+new\n line3\n*** End Patch\n"
	_, err := NewApplyPatch().Execute(context.Background(), mustJSON(t, map[string]any{
		"patch":      patch,
		"baseHashes": map[string]string{"p.txt": strings.Repeat("a", 64)},
	}), tc)
	if err == nil || CodeOf(err) != string(CodePreconditionFailed) {
		t.Fatalf("want precondition_failed, got %v", err)
	}
	// Unchanged on disk.
	got, _ := os.ReadFile(path)
	if string(got) != string(orig) {
		t.Fatalf("disk mutated on failed precondition: %q", got)
	}
	if _, err := NewApplyPatch().Execute(context.Background(), mustJSON(t, map[string]any{
		"patch":      patch,
		"baseHashes": map[string]string{"p.txt": ContentHash(orig)},
	}), tc); err != nil {
		t.Fatal(err)
	}
}

func TestEditDetectsConcurrentModificationRace(t *testing.T) {
	// Simulate TOCTOU: content changes after the tool's plan-time read but
	// before atomic commit. Ask is delayed so we can mutate mid-flight.
	dir := t.TempDir()
	path := filepath.Join(dir, "race.txt")
	if err := os.WriteFile(path, []byte("hello world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	tc := allowAll(dir)
	tc.Ask = func(ctx context.Context, req AskRequest) error {
		once.Do(func() {
			// External writer changes the file after plan-time read.
			time.Sleep(5 * time.Millisecond)
			_ = os.WriteFile(path, []byte("hello world\nEXTERNAL\n"), 0o644)
		})
		return nil
	}
	_, err := NewEdit().Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath":  "race.txt",
		"oldString": "hello",
		"newString": "hi",
	}), tc)
	if err == nil || CodeOf(err) != string(CodePreconditionFailed) {
		t.Fatalf("want concurrent precondition_failed, got %v (code=%q)", err, CodeOf(err))
	}
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), "EXTERNAL") {
		t.Fatalf("want external content preserved, got %q", got)
	}
	if strings.HasPrefix(string(got), "hi ") {
		t.Fatalf("edit should not have applied: %q", got)
	}
}

func TestWriteAndEditRecordTurnDiff(t *testing.T) {
	dir := t.TempDir()
	td := &TurnDiff{}
	tc := allowAll(dir)
	tc.TurnDiff = td

	if _, err := NewWrite().Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath": "new.txt",
		"content":  "created\n",
	}), tc); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "old.txt"), []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewEdit().Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath":  "old.txt",
		"oldString": "one",
		"newString": "two",
	}), tc); err != nil {
		t.Fatal(err)
	}
	// Delete via apply_patch.
	if err := os.WriteFile(filepath.Join(dir, "gone.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n*** Delete File: gone.txt\n*** End Patch\n"
	if _, err := NewApplyPatch().Execute(context.Background(), mustJSON(t, map[string]any{
		"patch": patch,
	}), tc); err != nil {
		t.Fatal(err)
	}

	snap := td.Snapshot()
	byPath := map[string]ChangeKind{}
	for _, c := range snap {
		byPath[c.Path] = c.Kind
	}
	if byPath["new.txt"] != ChangeCreate {
		t.Fatalf("new.txt kind = %q in %#v", byPath["new.txt"], snap)
	}
	if byPath["old.txt"] != ChangeUpdate {
		t.Fatalf("old.txt kind = %q in %#v", byPath["old.txt"], snap)
	}
	if byPath["gone.txt"] != ChangeDelete {
		t.Fatalf("gone.txt kind = %q in %#v", byPath["gone.txt"], snap)
	}
}

func TestCheckBaseHashHelpers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "z")
	data := []byte("payload")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CheckBaseHash(path, ContentHash(data), "z"); err != nil {
		t.Fatal(err)
	}
	if err := CheckBaseHash(path, "", "z"); err != nil {
		t.Fatal(err)
	}
	if err := CheckContentUnchanged(path, data, "z"); err != nil {
		t.Fatal(err)
	}
	if err := CheckContentUnchanged(path, []byte("other"), "z"); err == nil || CodeOf(err) != string(CodePreconditionFailed) {
		t.Fatalf("want mismatch, got %v", err)
	}
}
