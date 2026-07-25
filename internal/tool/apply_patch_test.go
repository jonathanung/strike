package tool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyPatchAdd(t *testing.T) {
	dir := t.TempDir()
	tc := allowAll(dir)
	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Add File: new.txt",
		"+hello",
		"+world",
		"*** End Patch",
	}, "\n")
	res, err := NewApplyPatch().Execute(context.Background(), mustJSON(t, map[string]any{"patch": patch}), tc)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "new.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello\nworld\n" {
		t.Errorf("content = %q", data)
	}
	if !strings.Contains(res.Output, "A new.txt") {
		t.Errorf("output = %q", res.Output)
	}
}

func TestApplyPatchDelete(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gone.txt")
	if err := os.WriteFile(path, []byte("bye\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Delete File: gone.txt",
		"*** End Patch",
	}, "\n")
	_, err := NewApplyPatch().Execute(context.Background(), mustJSON(t, map[string]any{"patch": patch}), allowAll(dir))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("file still exists: %v", err)
	}
}

func TestApplyPatchUpdate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "edit.txt")
	if err := os.WriteFile(path, []byte("line1\nold\nline3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: edit.txt",
		"@@",
		" line1",
		"-old",
		"+new",
		" line3",
		"*** End Patch",
	}, "\n")
	_, err := NewApplyPatch().Execute(context.Background(), mustJSON(t, map[string]any{"patch": patch}), allowAll(dir))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "line1\nnew\nline3\n" {
		t.Errorf("content = %q", data)
	}
}

func TestApplyPatchContextMismatchNoWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keep.txt")
	orig := "alpha\nbeta\n"
	if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	var asked bool
	tc := &Context{
		WorkDir: dir,
		Ask: func(context.Context, AskRequest) error {
			asked = true
			return nil
		},
	}
	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: keep.txt",
		"@@",
		"-missing-line",
		"+replacement",
		"*** End Patch",
	}, "\n")
	_, err := NewApplyPatch().Execute(context.Background(), mustJSON(t, map[string]any{"patch": patch}), tc)
	if err == nil {
		t.Fatal("expected context mismatch error")
	}
	if !strings.Contains(err.Error(), "not found") && !strings.Contains(err.Error(), "old lines") {
		t.Errorf("err = %v", err)
	}
	// Plan fails before Ask — nothing written.
	if asked {
		t.Error("Ask should not run when patch fails to plan")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != orig {
		t.Errorf("file mutated on failed patch: %q", data)
	}
}

func TestApplyPatchMultiFileOneAsk(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var asks []AskRequest
	tc := &Context{
		WorkDir: dir,
		Ask: func(_ context.Context, req AskRequest) error {
			asks = append(asks, req)
			return nil
		},
	}
	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: a.txt",
		"@@",
		"-a",
		"+A",
		"*** Add File: b.txt",
		"+b",
		"*** End Patch",
	}, "\n")
	_, err := NewApplyPatch().Execute(context.Background(), mustJSON(t, map[string]any{"patch": patch}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if len(asks) != 1 {
		t.Fatalf("Ask count = %d, want 1", len(asks))
	}
	if asks[0].Permission != "edit" {
		t.Errorf("permission = %q", asks[0].Permission)
	}
	joined := strings.Join(asks[0].Patterns, ",")
	if !strings.Contains(joined, "a.txt") || !strings.Contains(joined, "b.txt") {
		t.Errorf("patterns = %#v, want both paths", asks[0].Patterns)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "a.txt")); string(data) != "A\n" {
		t.Errorf("a.txt = %q", data)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "b.txt")); string(data) != "b\n" {
		t.Errorf("b.txt = %q", data)
	}
}

func TestApplyPatchInvalidGrammar(t *testing.T) {
	tc := allowAll(t.TempDir())
	cases := []struct {
		name  string
		patch string
	}{
		{"missing markers", "just text"},
		{"no ops", "*** Begin Patch\n*** End Patch\n"},
		{"bad add body", "*** Begin Patch\n*** Add File: x\nnot-a-plus-line\n*** End Patch\n"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewApplyPatch().Execute(context.Background(), mustJSON(t, map[string]any{"patch": tt.patch}), tc)
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

// Two Update File ops on the same path must chain in plan order so both
// changes land (second op sees the first op's planned content, not disk).
func TestApplyPatchChainedUpdatesSamePath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "chain.txt")
	if err := os.WriteFile(path, []byte("a\nb\nc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: chain.txt",
		"@@",
		"-a",
		"+A",
		" b",
		" c",
		"*** Update File: chain.txt",
		"@@",
		" A",
		" b",
		"-c",
		"+C",
		"*** End Patch",
	}, "\n")
	res, err := NewApplyPatch().Execute(context.Background(), mustJSON(t, map[string]any{"patch": patch}), allowAll(dir))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "A\nb\nC\n" {
		t.Errorf("content = %q, want both chained edits", data)
	}
	// Summary lists each planned op (two updates on same path).
	if strings.Count(res.Output, "M chain.txt") != 2 {
		t.Errorf("output = %q, want two M chain.txt lines", res.Output)
	}
}

// Move to an existing destination must fail during plan — before Ask and
// before any write — leaving source and dest untouched.
func TestApplyPatchMoveToExistingFailsBeforeWrite(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	dst := filepath.Join(dir, "dst.txt")
	srcOrig := "source-body\n"
	dstOrig := "dest-body\n"
	if err := os.WriteFile(src, []byte(srcOrig), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte(dstOrig), 0o644); err != nil {
		t.Fatal(err)
	}
	var asked bool
	tc := &Context{
		WorkDir: dir,
		Ask: func(context.Context, AskRequest) error {
			asked = true
			return nil
		},
	}
	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: src.txt",
		"*** Move to: dst.txt",
		"@@",
		"-source-body",
		"+moved-body",
		"*** End Patch",
	}, "\n")
	_, err := NewApplyPatch().Execute(context.Background(), mustJSON(t, map[string]any{"patch": patch}), tc)
	if err == nil {
		t.Fatal("expected move-to-existing error")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("err = %v, want already exists", err)
	}
	if asked {
		t.Error("Ask should not run when move destination already exists")
	}
	if data, err := os.ReadFile(src); err != nil || string(data) != srcOrig {
		t.Errorf("src mutated: %q err=%v", data, err)
	}
	if data, err := os.ReadFile(dst); err != nil || string(data) != dstOrig {
		t.Errorf("dst mutated: %q err=%v", data, err)
	}
}

// Multi-file patch with add + update + delete still applies atomically.
func TestApplyPatchMultiFileAddUpdateDelete(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "drop.txt"), []byte("gone\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: keep.txt",
		"@@",
		"-old",
		"+new",
		"*** Add File: fresh.txt",
		"+hello",
		"*** Delete File: drop.txt",
		"*** End Patch",
	}, "\n")
	res, err := NewApplyPatch().Execute(context.Background(), mustJSON(t, map[string]any{"patch": patch}), allowAll(dir))
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "keep.txt")); string(data) != "new\n" {
		t.Errorf("keep.txt = %q", data)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "fresh.txt")); string(data) != "hello\n" {
		t.Errorf("fresh.txt = %q", data)
	}
	if _, err := os.Stat(filepath.Join(dir, "drop.txt")); !os.IsNotExist(err) {
		t.Fatalf("drop.txt still exists: %v", err)
	}
	for _, want := range []string{"M keep.txt", "A fresh.txt", "D drop.txt"} {
		if !strings.Contains(res.Output, want) {
			t.Errorf("output %q missing %q", res.Output, want)
		}
	}
}

func TestRestorePath(t *testing.T) {
	dir := t.TempDir()

	t.Run("remove when original missing", func(t *testing.T) {
		path := filepath.Join(dir, "added.txt")
		if err := os.WriteFile(path, []byte("temp\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := restorePath(path, pathOriginal{exists: false}); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("path still exists: %v", err)
		}
	})

	t.Run("rewrite original bytes", func(t *testing.T) {
		path := filepath.Join(dir, "edited.txt")
		orig := []byte("original\n")
		if err := os.WriteFile(path, []byte("mutated\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := restorePath(path, pathOriginal{exists: true, data: orig}); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != string(orig) {
			t.Errorf("content = %q, want %q", data, orig)
		}
	})

	t.Run("recreate deleted file", func(t *testing.T) {
		path := filepath.Join(dir, "deleted.txt")
		orig := []byte("bring-back\n")
		if err := restorePath(path, pathOriginal{exists: true, data: orig}); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != string(orig) {
			t.Errorf("content = %q, want %q", data, orig)
		}
	})
}

func TestRollbackPatchOps(t *testing.T) {
	dir := t.TempDir()
	keep := filepath.Join(dir, "keep.txt")
	added := filepath.Join(dir, "added.txt")
	deleted := filepath.Join(dir, "deleted.txt")
	src := filepath.Join(dir, "src.txt")
	dst := filepath.Join(dir, "dst.txt")

	keepOrig := []byte("keep-orig\n")
	delOrig := []byte("del-orig\n")
	srcOrig := []byte("src-orig\n")

	if err := os.WriteFile(keep, []byte("keep-mutated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(added, []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// deleted path is gone on disk
	if err := os.WriteFile(src, []byte("src-should-restore\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("dst-moved\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	originals := map[string]pathOriginal{
		keep:    {exists: true, data: keepOrig},
		added:   {exists: false},
		deleted: {exists: true, data: delOrig},
		src:     {exists: true, data: srcOrig},
		dst:     {exists: false},
	}
	applied := []plannedOp{
		{Type: "update", AbsPath: keep},
		{Type: "add", AbsPath: added},
		{Type: "delete", AbsPath: deleted},
		{Type: "move", AbsPath: src, AbsMove: dst},
	}
	if err := rollbackPatchOps(applied, originals); err != nil {
		t.Fatal(err)
	}

	if data, err := os.ReadFile(keep); err != nil || string(data) != string(keepOrig) {
		t.Errorf("keep = %q err=%v", data, err)
	}
	if _, err := os.Stat(added); !os.IsNotExist(err) {
		t.Errorf("added still exists: %v", err)
	}
	if data, err := os.ReadFile(deleted); err != nil || string(data) != string(delOrig) {
		t.Errorf("deleted = %q err=%v", data, err)
	}
	if data, err := os.ReadFile(src); err != nil || string(data) != string(srcOrig) {
		t.Errorf("src = %q err=%v", data, err)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Errorf("dst still exists: %v", err)
	}
}

// failAddPath makes a read-only directory and returns a child path that WriteFile
// cannot create (plan-time ReadFile still sees os.IsNotExist).
func failAddPath(t *testing.T, dir string) (roDir, target string) {
	t.Helper()
	roDir = filepath.Join(dir, "locked")
	if err := os.Mkdir(roDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(roDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(roDir, 0o755) })
	target = filepath.Join(roDir, "child.txt")
	// Skip if this environment can still write (e.g. root).
	f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY, 0o644)
	if err == nil {
		_ = f.Close()
		_ = os.Remove(target)
		t.Skip("filesystem allows write into 0555 dir; cannot inject commit failure")
	}
	return roDir, target
}

// Mid-commit failure must roll back earlier ops so the tree matches pre-patch state.
func TestCommitPatchOpsRollbackOnFailure(t *testing.T) {
	dir := t.TempDir()
	keepPath := filepath.Join(dir, "keep.txt")
	keepOrig := []byte("original-keep\n")
	if err := os.WriteFile(keepPath, keepOrig, 0o644); err != nil {
		t.Fatal(err)
	}
	_, target := failAddPath(t, dir)
	ops := []plannedOp{
		{
			Type:    "update",
			AbsPath: keepPath,
			RelPath: "keep.txt",
			Content: "mutated-keep\n",
		},
		{
			Type:    "add",
			AbsPath: target,
			RelPath: "locked/child.txt",
			Content: "nope\n",
		},
	}
	originals := map[string]pathOriginal{
		keepPath: {exists: true, data: keepOrig},
		target:   {exists: false},
	}

	err := commitPatchOps(ops, originals)
	if err == nil {
		t.Fatal("expected commit failure")
	}
	if !strings.Contains(err.Error(), "rolled back") && !strings.Contains(err.Error(), "rollback") {
		t.Errorf("err = %v, want rolled back message", err)
	}

	data, readErr := os.ReadFile(keepPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != string(keepOrig) {
		t.Errorf("keep.txt not rolled back: %q", data)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Errorf("partial add leaked: %v", statErr)
	}
}

// Full tool path: multi-file patch applies first op then fails second; tree restored.
func TestApplyPatchPartialCommitRollsBack(t *testing.T) {
	dir := t.TempDir()
	keepPath := filepath.Join(dir, "keep.txt")
	keepOrig := "line-a\n"
	if err := os.WriteFile(keepPath, []byte(keepOrig), 0o644); err != nil {
		t.Fatal(err)
	}
	_, target := failAddPath(t, dir)

	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: keep.txt",
		"@@",
		"-line-a",
		"+line-B",
		"*** Add File: locked/child.txt",
		"+should-fail",
		"*** End Patch",
	}, "\n")
	_, err := NewApplyPatch().Execute(context.Background(), mustJSON(t, map[string]any{"patch": patch}), allowAll(dir))
	if err == nil {
		t.Fatal("expected commit/rollback error")
	}
	if !strings.Contains(err.Error(), "rolled back") && !strings.Contains(err.Error(), "commit failed") {
		t.Errorf("err = %v", err)
	}
	data, readErr := os.ReadFile(keepPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != keepOrig {
		t.Errorf("keep.txt after rollback = %q, want %q", data, keepOrig)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Errorf("locked/child.txt should not exist: %v", statErr)
	}
}

// Delete then failing add: deleted file must be restored on rollback.
func TestCommitPatchOpsRollbackRestoresDelete(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim.txt")
	victimOrig := []byte("restore-me\n")
	if err := os.WriteFile(victim, victimOrig, 0o644); err != nil {
		t.Fatal(err)
	}
	_, target := failAddPath(t, dir)

	ops := []plannedOp{
		{Type: "delete", AbsPath: victim, RelPath: "victim.txt"},
		{Type: "add", AbsPath: target, RelPath: "locked/child.txt", Content: "nope\n"},
	}
	originals := map[string]pathOriginal{
		victim: {exists: true, data: victimOrig},
		target: {exists: false},
	}
	err := commitPatchOps(ops, originals)
	if err == nil {
		t.Fatal("expected failure")
	}
	data, readErr := os.ReadFile(victim)
	if readErr != nil {
		t.Fatalf("victim not restored: %v", readErr)
	}
	if string(data) != string(victimOrig) {
		t.Errorf("victim = %q", data)
	}
}
