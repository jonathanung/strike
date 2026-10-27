package knowledge

import (
	"os"
	"strings"
	"testing"
)

func TestPinRedactsAndSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, "proj")
	if err != nil {
		t.Fatal(err)
	}
	pin, err := s.Put("docs/theme.md", "password=supersecretvalue", "sess-9")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(pin.Note, "supersecretvalue") {
		t.Fatalf("note = %q", pin.Note)
	}
	if pin.Author != "sess-9" {
		t.Fatalf("author = %q", pin.Author)
	}
	path := s.Path()
	md := s.MarkdownPath()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "supersecretvalue") {
		t.Fatalf("disk kept secret: %s", raw)
	}
	text, err := os.ReadFile(md)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(text), "docs/theme.md") || strings.Contains(string(text), "supersecretvalue") {
		t.Fatalf("markdown = %s", text)
	}

	s2, err := Open(dir, "proj")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	pins, err := s2.List()
	if err != nil || len(pins) != 1 || pins[0].Path != "docs/theme.md" || pins[0].Author != "sess-9" {
		t.Fatalf("reopen = %#v err=%v", pins, err)
	}
	if strings.Contains(pins[0].Note, "supersecretvalue") {
		t.Fatalf("reopen note = %q", pins[0].Note)
	}
}

func TestGlobalPinsAreSeparate(t *testing.T) {
	dir := t.TempDir()
	project, err := Open(dir, "proj")
	if err != nil {
		t.Fatal(err)
	}
	global, err := Open(dir, GlobalKey)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = project.Close()
		_ = global.Close()
	})
	if _, err := project.Put("AGENTS.md", "project rules", "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := global.Put("AGENTS.md", "global rules", "b"); err != nil {
		t.Fatal(err)
	}
	got, err := project.List()
	if err != nil || len(got) != 1 || got[0].Note != "project rules" {
		t.Fatalf("project = %#v err=%v", got, err)
	}
	if global.Scope() != "global" {
		t.Fatalf("scope = %s", global.Scope())
	}
	text, omitted := FormatAutoLoad(global.Scope(), mustList(t, global))
	if omitted != 0 || !strings.Contains(text, "AGENTS.md") || !strings.Contains(text, "global") {
		t.Fatalf("autoload omitted=%d text=%s", omitted, text)
	}
}

func TestDeletePin(t *testing.T) {
	s, err := Open(t.TempDir(), "proj")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, err := s.Put("a.md", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("a.md"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("a.md"); err != ErrNotFound {
		t.Fatalf("second delete = %v", err)
	}
	pins, err := s.List()
	if err != nil || len(pins) != 0 {
		t.Fatalf("pins = %#v err=%v", pins, err)
	}
}

func mustList(t *testing.T, s *Store) []Pin {
	t.Helper()
	pins, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	return pins
}
