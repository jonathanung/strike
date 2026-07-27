package tool

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveInWorkspaceHappy(t *testing.T) {
	root := t.TempDir()
	inner := filepath.Join(root, "pkg")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(inner, "a.go")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	resolved, rel, err := resolveInWorkspace(root, "pkg/a.go")
	if err != nil {
		t.Fatal(err)
	}
	if resolved != f {
		// EvalSymlinks may canonicalize; compare via same-file if needed.
		if filepath.Clean(resolved) != filepath.Clean(f) {
			t.Fatalf("resolved = %q, want %q", resolved, f)
		}
	}
	if rel != "pkg/a.go" {
		t.Fatalf("rel = %q", rel)
	}

	// Absolute path still inside root.
	_, rel, err = resolveInWorkspace(root, f)
	if err != nil {
		t.Fatal(err)
	}
	if rel != "pkg/a.go" {
		t.Fatalf("abs rel = %q", rel)
	}

	// Missing leaf under existing parent is OK.
	_, rel, err = resolveInWorkspace(root, "pkg/new.txt")
	if err != nil {
		t.Fatal(err)
	}
	if rel != "pkg/new.txt" {
		t.Fatalf("missing rel = %q", rel)
	}
}

func TestResolveInWorkspaceRejectsEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []string{
		secret,
		filepath.Join(root, "..", filepath.Base(outside), "secret.txt"),
		"../" + filepath.Base(outside) + "/secret.txt",
		"/etc/passwd",
		"/tmp",
	}
	for _, p := range cases {
		_, _, err := resolveInWorkspace(root, p)
		if err == nil {
			t.Fatalf("resolveInWorkspace(%q) = nil, want escape error", p)
		}
		var esc *WorkspaceEscapeError
		if !errors.As(err, &esc) && !strings.Contains(err.Error(), "escapes") {
			t.Fatalf("resolveInWorkspace(%q) = %v, want WorkspaceEscapeError", p, err)
		}
	}
}

func TestResolveInWorkspaceRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "outlink")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink: %v", err)
	}
	for _, p := range []string{
		"outlink/secret.txt",
		filepath.Join(link, "secret.txt"),
	} {
		_, _, err := resolveInWorkspace(root, p)
		if err == nil {
			t.Fatalf("symlink escape %q allowed", p)
		}
	}
}

func TestWorkspaceEscapeErrorMessage(t *testing.T) {
	err := &WorkspaceEscapeError{Path: "/tmp/x", Root: "/proj"}
	if got := err.Error(); !strings.Contains(got, "/tmp/x") || !strings.Contains(got, "/proj") {
		t.Fatalf("Error() = %q", got)
	}
}

func TestWriteAndEditRejectOutsideWorkspace(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	tc := allowAll(root)

	_, err := NewWrite().Execute(t.Context(), mustJSON(t, map[string]any{
		"filePath": secret,
		"content":  "pwned",
	}), tc)
	if err == nil {
		t.Fatal("write outside: want error")
	}
	data, _ := os.ReadFile(secret)
	if string(data) != "keep" {
		t.Fatalf("outside file mutated: %q", data)
	}

	_, err = NewEdit().Execute(t.Context(), mustJSON(t, map[string]any{
		"filePath":  secret,
		"oldString": "keep",
		"newString": "pwned",
	}), tc)
	if err == nil {
		t.Fatal("edit outside: want error")
	}
	data, _ = os.ReadFile(secret)
	if string(data) != "keep" {
		t.Fatalf("outside file edited: %q", data)
	}

	// Symlink escape.
	link := filepath.Join(root, "leak")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink: %v", err)
	}
	_, err = NewWrite().Execute(t.Context(), mustJSON(t, map[string]any{
		"filePath": "leak/secret.txt",
		"content":  "pwned",
	}), tc)
	if err == nil {
		t.Fatal("write via symlink escape: want error")
	}
	data, _ = os.ReadFile(secret)
	if string(data) != "keep" {
		t.Fatalf("symlink escape mutated outside: %q", data)
	}
}
