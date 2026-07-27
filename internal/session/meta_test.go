package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMetaRoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := Meta{
		ProjectKey:  "/repos/acme",
		PRURL:       "https://github.com/acme/repo/pull/42",
		PRNumber:    42,
		PRState:     PRStateMerged,
		PRUpdatedAt: "2026-07-25T12:00:00Z",
	}
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

func TestMetaJSONProjectKeyFirst(t *testing.T) {
	dir := t.TempDir()
	if err := WriteMeta(dir, "top", Meta{
		ProjectKey: "/home/me/proj",
		Title:      "hello",
		CreatedAt:  "2026-07-27T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(MetaPath(dir, "top"))
	if err != nil {
		t.Fatal(err)
	}
	// Sidecar should embed workspace path at the top of the JSON object.
	body := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(body, "{\n  \"projectKey\": \"/home/me/proj\"") {
		t.Fatalf("meta JSON should start with projectKey, got:\n%s", body)
	}
}

func TestNormalizePRState(t *testing.T) {
	cases := map[string]string{
		"":         "",
		"open":     PRStateOpen,
		"OPEN":     PRStateOpen,
		" merged ": PRStateMerged,
		"CLOSED":   PRStateClosed,
		"draft":    "",
	}
	for in, want := range cases {
		if got := NormalizePRState(in); got != want {
			t.Errorf("NormalizePRState(%q) = %q, want %q", in, got, want)
		}
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
		m.PRState = PRStateOpen
		m.PRUpdatedAt = "2026-07-25T15:00:00Z"
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.PRURL != "https://github.com/acme/repo/pull/9" || got.PRNumber != 9 || got.PRState != PRStateOpen {
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
