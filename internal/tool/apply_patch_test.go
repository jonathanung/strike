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
