package artifact

import (
	"os"
	"strings"
	"testing"

	"github.com/jonathanung/strike/pkg/redact"
)

func TestHistoryRetainedRedactedAndReopened(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, "proj")
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.Create(CreateInput{
		Type: TypeFindings, Title: "t", Content: "body sk-ant-abcdefghijklmnopqrstuvwxyz",
		OwnerSession: "s", OwnerRoot: "r",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(a.Content, "sk-ant-abcdefghijklmnopqrstuvwxyz") {
		t.Fatalf("create kept secret: %q", a.Content)
	}
	next := "second " + redact.Placeholder
	updated, err := s.Update(a.ID, "s", "r", a.Version, UpdateInput{Content: &next})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != 2 || len(updated.VersionList()) != 2 {
		t.Fatalf("versions = %v current %d", updated.VersionList(), updated.Version)
	}
	path := s.Path()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "sk-ant-abcdefghijklmnopqrstuvwxyz") {
		t.Fatalf("disk kept secret: %s", raw)
	}
	s2, err := Open(dir, "proj")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	old, ok, err := s2.GetVersion(a.ID, 1, "s", "r")
	if err != nil || !ok || strings.Contains(old.Content, "sk-ant-") {
		t.Fatalf("reopen v1 ok=%v content=%q err=%v", ok, old.Content, err)
	}
	cur, ok, err := s2.GetVersion(a.ID, 2, "s", "r")
	if err != nil || !ok || cur.Content != next {
		t.Fatalf("reopen v2 ok=%v content=%q err=%v", ok, cur.Content, err)
	}
}

func TestHistoryCapsAtMax(t *testing.T) {
	s, err := Open(t.TempDir(), "proj")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	a, err := s.Create(CreateInput{Type: TypePatch, Content: "v1", OwnerSession: "s", OwnerRoot: "r"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < MaxHistory+3; i++ {
		c := "v"
		a, err = s.Update(a.ID, "s", "r", a.Version, UpdateInput{Content: &c})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(a.History) != MaxHistory {
		t.Fatalf("history len = %d", len(a.History))
	}
	if _, ok, err := s.GetVersion(a.ID, 1, "s", "r"); err != nil || ok {
		t.Fatalf("version 1 should have rolled off: ok=%v err=%v", ok, err)
	}
}
