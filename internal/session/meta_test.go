package session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMetaRoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := Meta{PRURL: "https://github.com/acme/repo/pull/42", PRNumber: 42}
	if err := WriteMeta(dir, "sess-1", want); err != nil {
		t.Fatal(err)
	}
	got, err := ReadMeta(dir, "sess-1")
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("ReadMeta = %+v, want %+v", got, want)
	}
	if _, err := os.Stat(MetaPath(dir, "sess-1")); err != nil {
		t.Fatal(err)
	}
}

func TestReadMetaMissingIsZero(t *testing.T) {
	got, err := ReadMeta(t.TempDir(), "missing")
	if err != nil {
		t.Fatal(err)
	}
	if got != (Meta{}) {
		t.Fatalf("got %+v, want zero", got)
	}
}

func TestUpdateMetaMerges(t *testing.T) {
	dir := t.TempDir()
	if err := WriteMeta(dir, "s", Meta{
		Title:           "ship it",
		ParentSessionID: "parent-1",
		PRURL:           "https://example.com/pull/1",
		PRNumber:        1,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := UpdateMeta(dir, "s", func(m *Meta) {
		m.PRURL = "https://github.com/acme/repo/pull/9"
		m.PRNumber = 9
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.PRURL != "https://github.com/acme/repo/pull/9" || got.PRNumber != 9 {
		t.Fatalf("UpdateMeta = %+v", got)
	}
	if got.Title != "ship it" || got.ParentSessionID != "parent-1" {
		t.Fatalf("UpdateMeta dropped fields: %+v", got)
	}
	// Malformed existing file surfaces.
	if err := os.WriteFile(filepath.Join(dir, "bad.meta.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadMeta(dir, "bad"); err == nil {
		t.Fatal("expected error for malformed meta")
	}
}
