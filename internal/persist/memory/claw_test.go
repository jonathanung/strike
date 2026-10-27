package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonathanung/strike/pkg/redact"
)

func TestPutRedactsAndSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, "proj-a")
	if err != nil {
		t.Fatal(err)
	}
	secret := "note sk-ant-abcdefghijklmnopqrstuvwxyz"
	if err := s.PutAuthored("lesson", secret, "sess-1", []string{"preference"}); err != nil {
		t.Fatal(err)
	}
	jsonPath := s.Path()
	mdPath := s.MarkdownPath()
	auditPath := s.AuditPath()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "sk-ant-abcdefghijklmnopqrstuvwxyz") {
		t.Fatalf("json kept the secret: %s", raw)
	}
	md, err := os.ReadFile(mdPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(md), "sk-ant-abcdefghijklmnopqrstuvwxyz") {
		t.Fatal("markdown kept the secret")
	}
	if !strings.Contains(string(md), "sess-1") || !strings.Contains(string(md), "preference") {
		t.Fatalf("markdown missing attribution: %s", md)
	}
	audit, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(audit), "sk-ant-abcdefghijklmnopqrstuvwxyz") || !strings.Contains(string(audit), `"op":"put"`) {
		t.Fatalf("audit = %s", audit)
	}

	s2, err := Open(dir, "proj-a")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	got, ok, err := s2.Get("lesson")
	if err != nil || !ok {
		t.Fatalf("reopen get ok=%v err=%v", ok, err)
	}
	if got.Author != "sess-1" {
		t.Fatalf("author = %q", got.Author)
	}
	if strings.Contains(got.Value, "sk-ant-") || !strings.Contains(got.Value, redact.Placeholder) && !strings.Contains(got.Value, "REDACTED") {
		t.Fatalf("value = %q", got.Value)
	}
	if s2.Scope() != "project" {
		t.Fatalf("scope = %s", s2.Scope())
	}
}

func TestGlobalStoreIsSeparate(t *testing.T) {
	dir := t.TempDir()
	project, err := Open(dir, "proj-a")
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
	if err := project.Put("k", "project-value", nil); err != nil {
		t.Fatal(err)
	}
	if err := global.PutAuthored("k", "global-value", "ops", []string{"instruction"}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := project.Get("k")
	if err != nil || !ok || got.Value != "project-value" {
		t.Fatalf("project = %#v ok=%v err=%v", got, ok, err)
	}
	got, ok, err = global.Get("k")
	if err != nil || !ok || got.Value != "global-value" || got.Author != "ops" {
		t.Fatalf("global = %#v ok=%v err=%v", got, ok, err)
	}
	if global.Scope() != "global" {
		t.Fatalf("scope = %s", global.Scope())
	}
	_ = global.Close()
	reopened, err := Open(dir, GlobalKey)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	got, ok, err = reopened.Get("k")
	if err != nil || !ok || got.Value != "global-value" {
		t.Fatalf("reopen global = %#v ok=%v err=%v", got, ok, err)
	}
}

func TestOpenScrubsLegacyFile(t *testing.T) {
	dir := t.TempDir()
	memDir := filepath.Join(dir, "memory")
	if err := os.MkdirAll(memDir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{
  "version": 1,
  "entries": [{"key":"legacy","value":"api_key=supersecretvalue","tags":["note"],"updated_at":"2026-01-02T03:04:05Z"}]
}
`
	if err := os.WriteFile(filepath.Join(memDir, fileBase("proj-a")), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir, "proj-a")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	got, ok, err := s.Get("legacy")
	if err != nil || !ok {
		t.Fatal(err)
	}
	if strings.Contains(got.Value, "supersecretvalue") {
		t.Fatalf("legacy value kept secret: %q", got.Value)
	}
	raw, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "supersecretvalue") {
		t.Fatalf("disk kept secret: %s", raw)
	}
}

func TestNormalizeMode(t *testing.T) {
	cases := []struct {
		in    string
		want  string
		read  bool
		write bool
		bad   bool
	}{
		{"", ModeNormal, true, true, false},
		{"Normal", ModeNormal, true, true, false},
		{"incognito", ModeIncognito, true, false, false},
		{"temporary", ModeTemporary, false, false, false},
		{"yolo", "", false, false, true},
	}
	for _, tc := range cases {
		got, err := NormalizeMode(tc.in)
		if tc.bad {
			if err == nil {
				t.Fatalf("%q: expected error", tc.in)
			}
			continue
		}
		if err != nil || got != tc.want || AllowsRead(tc.in) != tc.read || AllowsWrite(tc.in) != tc.write {
			t.Fatalf("%q: got %q err=%v read=%v write=%v", tc.in, got, err, AllowsRead(tc.in), AllowsWrite(tc.in))
		}
	}
}
