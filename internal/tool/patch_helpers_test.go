package tool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sampleAddPatch(path, body string) string {
	lines := []string{"*** Begin Patch", "*** Add File: " + path}
	for _, l := range strings.Split(strings.TrimSuffix(body, "\n"), "\n") {
		lines = append(lines, "+"+l)
	}
	lines = append(lines, "*** End Patch")
	return strings.Join(lines, "\n")
}

func sampleUpdatePatch(path, old, neu string) string {
	return strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: " + path,
		"@@",
		"-" + old,
		"+" + neu,
		"*** End Patch",
	}, "\n")
}

func TestPreviewPatchAddAndUpdate(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	add := sampleAddPatch("b.txt", "hello\n")
	prev := PreviewPatch(dir, add)
	if !prev.Valid || len(prev.Files) != 1 || prev.Files[0] != "b.txt" {
		t.Fatalf("add preview = %#v", prev)
	}
	upd := sampleUpdatePatch("a.txt", "old", "new")
	prev2 := PreviewPatch(dir, upd)
	if !prev2.Valid || prev2.Files[0] != "a.txt" {
		t.Fatalf("update preview = %#v", prev2)
	}
	// Stale base fails validation.
	bad := sampleUpdatePatch("a.txt", "missing", "x")
	prev3 := PreviewPatch(dir, bad)
	if prev3.Valid {
		t.Fatalf("expected invalid, got %#v", prev3)
	}
}

func TestPathSetOverlapAndDetectConflicts(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p1 := sampleUpdatePatch("a.txt", "a", "A")
	p2 := sampleUpdatePatch("b.txt", "b", "B")
	p3 := sampleUpdatePatch("a.txt", "a", "Z") // overlaps p1 on a.txt

	overlap := PathSetOverlap(map[string][]string{
		"p1": {"a.txt"},
		"p2": {"b.txt"},
		"p3": {"a.txt", "c.txt"},
	})
	if len(overlap) != 1 || len(overlap["a.txt"]) != 2 {
		t.Fatalf("overlap = %#v", overlap)
	}

	ok := DetectPatchConflicts(dir, []NamedPatch{
		{ID: "p1", Patch: p1},
		{ID: "p2", Patch: p2},
	})
	if ok.HasConflict {
		t.Fatalf("non-overlapping should be clean: %#v", ok)
	}

	bad := DetectPatchConflicts(dir, []NamedPatch{
		{ID: "p1", Patch: p1},
		{ID: "p3", Patch: p3},
	})
	if !bad.HasConflict || len(bad.PathOverlap["a.txt"]) != 2 {
		t.Fatalf("expected path overlap: %#v", bad)
	}
}

func TestApplyOnePatchAndSequential(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p1 := sampleUpdatePatch("a.txt", "a", "A")
	p2 := sampleUpdatePatch("b.txt", "b", "B")

	sum, files, err := ApplyOnePatch(dir, p1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sum, "a.txt") || len(files) != 1 {
		t.Fatalf("sum=%q files=%v", sum, files)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	if string(data) != "A\n" {
		t.Fatalf("a.txt = %q", data)
	}

	// Reset a for sequential test on fresh dir.
	dir2 := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir2, "a.txt"), []byte("a\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir2, "b.txt"), []byte("b\n"), 0o644)
	sums, all, err := ApplyPatchesSequential(dir2, []string{p1, p2})
	if err != nil {
		t.Fatal(err)
	}
	if len(sums) != 2 || len(all) != 2 {
		t.Fatalf("sums=%v all=%v", sums, all)
	}
	a, _ := os.ReadFile(filepath.Join(dir2, "a.txt"))
	b, _ := os.ReadFile(filepath.Join(dir2, "b.txt"))
	if string(a) != "A\n" || string(b) != "B\n" {
		t.Fatalf("got a=%q b=%q", a, b)
	}
}

func TestApplyPatchesSequentialRejectsStaleSecond(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644)
	p1 := sampleUpdatePatch("a.txt", "a", "A")
	// Second still expects old base line "a" — fails after first apply.
	p2 := sampleUpdatePatch("a.txt", "a", "Z")
	_, _, err := ApplyPatchesSequential(dir, []string{p1, p2})
	if err == nil {
		t.Fatal("expected second patch to fail against updated base")
	}
}
