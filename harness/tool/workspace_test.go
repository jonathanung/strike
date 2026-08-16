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

func TestResolveInWorkspaceRejectsDanglingSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "secret-new.txt")
	link := filepath.Join(root, "evil")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink: %v", err)
	}
	_, _, err := resolveInWorkspace(root, "evil")
	if err == nil {
		t.Fatal("dangling symlink to outside: want escape error")
	}
	var esc *WorkspaceEscapeError
	if !errors.As(err, &esc) {
		t.Fatalf("err = %v, want WorkspaceEscapeError", err)
	}
}

func TestResolveInWorkspaceDanglingSymlinkInsideOK(t *testing.T) {
	root := t.TempDir()
	link := filepath.Join(root, "alias")
	// Dangling link to a not-yet-created in-workspace leaf.
	if err := os.Symlink("future.txt", link); err != nil {
		t.Skipf("symlink: %v", err)
	}
	resolved, rel, err := resolveInWorkspace(root, "alias")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "future.txt")
	if filepath.Clean(resolved) != filepath.Clean(want) {
		t.Fatalf("resolved = %q, want %q", resolved, want)
	}
	if rel != "future.txt" {
		t.Fatalf("rel = %q", rel)
	}
}

func TestWorkspaceWriteFileRejectsSymlinkLeaf(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "pwned.txt")
	link := filepath.Join(root, "planted")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink: %v", err)
	}
	err := workspaceWriteFile(root, "planted", []byte("pwned"))
	if err == nil {
		t.Fatal("write through outside symlink: want error")
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		data, _ := os.ReadFile(target)
		t.Fatalf("outside target written: %q (err=%v)", data, err)
	}
}

func TestWorkspaceWriteFileTOCTOUPlantedSymlink(t *testing.T) {
	// resolve allows missing path; plant symlink; secure write must not follow.
	root := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "pwned.txt")
	path, _, err := resolveInWorkspace(root, "race")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlink: %v", err)
	}
	err = workspaceWriteFile(root, "race", []byte("pwned"))
	if err == nil {
		t.Fatal("write after planted symlink: want error")
	}
	if data, rerr := os.ReadFile(target); rerr == nil {
		t.Fatalf("outside written via TOCTOU: %q", data)
	}
}

func TestWriteToolRejectsDanglingSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "pwned.txt")
	if err := os.Symlink(target, filepath.Join(root, "evil")); err != nil {
		t.Skipf("symlink: %v", err)
	}
	tc := allowAll(root)
	_, err := NewWrite().Execute(t.Context(), mustJSON(t, map[string]any{
		"filePath": "evil",
		"content":  "pwned",
	}), tc)
	if err == nil {
		t.Fatal("write via dangling symlink: want error")
	}
	if data, rerr := os.ReadFile(target); rerr == nil {
		t.Fatalf("outside mutated: %q", data)
	}
}

func TestEditToolRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(root, "leak")); err != nil {
		t.Skipf("symlink: %v", err)
	}
	tc := allowAll(root)
	// Record freshness on the real path would not apply; edit should fail resolve.
	_, err := NewEdit().Execute(t.Context(), mustJSON(t, map[string]any{
		"filePath":  "leak",
		"oldString": "keep",
		"newString": "pwned",
	}), tc)
	if err == nil {
		t.Fatal("edit via symlink escape: want error")
	}
	data, _ := os.ReadFile(secret)
	if string(data) != "keep" {
		t.Fatalf("outside edited: %q", data)
	}
}

func TestWriteToolHappyStillWorks(t *testing.T) {
	root := t.TempDir()
	tc := allowAll(root)
	_, err := NewWrite().Execute(t.Context(), mustJSON(t, map[string]any{
		"filePath": "ok.txt",
		"content":  "hello",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "ok.txt"))
	if err != nil || string(data) != "hello" {
		t.Fatalf("content = %q err=%v", data, err)
	}
	// Overwrite existing regular file.
	_, err = NewWrite().Execute(t.Context(), mustJSON(t, map[string]any{
		"filePath": "ok.txt",
		"content":  "world",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(filepath.Join(root, "ok.txt"))
	if string(data) != "world" {
		t.Fatalf("overwrite = %q", data)
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

func TestWorkspaceWriteFilePreservesExistingMode(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "script.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := workspaceWriteFile(root, "script.sh", []byte("#!/bin/sh\necho hi\n")); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %o, want 755 (os.WriteFile must not strip exec)", fi.Mode().Perm())
	}
}
