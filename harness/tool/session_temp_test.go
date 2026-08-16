package tool

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSessionTempDirUsesPlatformTemp(t *testing.T) {
	// Point TMPDIR at an isolated root so we never touch the real host temp tree
	// beyond this test's sandbox (os.TempDir follows TMPDIR on Unix).
	base := t.TempDir()
	t.Setenv("TMPDIR", base)
	// Ensure os.TempDir picks up TMPDIR (Go caches nothing for TempDir).
	if got := os.TempDir(); filepath.Clean(got) != filepath.Clean(base) {
		// Some platforms may resolve; require prefix at least.
		if !strings.HasPrefix(filepath.Clean(got), filepath.Clean(base)) {
			t.Fatalf("os.TempDir() = %q, want under %q", got, base)
		}
	}

	dir := SessionTempDir("sess-abc")
	wantPrefix := filepath.Join(os.TempDir(), "strike", "sess-abc")
	if filepath.Clean(dir) != filepath.Clean(wantPrefix) {
		t.Fatalf("SessionTempDir = %q, want %q", dir, wantPrefix)
	}
	if SessionTempDir("") != "" {
		t.Fatal("empty session id should yield empty path")
	}
	if SessionTempDir("..") != "" {
		t.Fatal("dangerous id should sanitize to empty or safe")
	}
}

func TestEnsureAndCleanupSessionTemp(t *testing.T) {
	base := t.TempDir()
	t.Setenv("TMPDIR", base)

	dir, err := EnsureSessionTemp("cleanup-me")
	if err != nil {
		t.Fatal(err)
	}
	if dir == "" {
		t.Fatal("expected dir")
	}
	marker := filepath.Join(dir, "scratch.txt")
	if err := os.WriteFile(marker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CleanupSessionTemp("cleanup-me"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("dir still exists after cleanup: %v", err)
	}
	// Idempotent.
	if err := CleanupSessionTemp("cleanup-me"); err != nil {
		t.Fatal(err)
	}
}

func TestSessionTempIsolation(t *testing.T) {
	base := t.TempDir()
	t.Setenv("TMPDIR", base)

	a, err := EnsureSessionTemp("session-a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := EnsureSessionTemp("session-b")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = CleanupSessionTemp("session-a")
		_ = CleanupSessionTemp("session-b")
	})
	if a == b {
		t.Fatalf("sessions share temp dir: %q", a)
	}
	if err := os.WriteFile(filepath.Join(a, "only-a"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(b, "only-a")); !os.IsNotExist(err) {
		t.Fatal("session B can see session A file")
	}
}

func TestCleanupStaleSessionTemps(t *testing.T) {
	base := t.TempDir()
	t.Setenv("TMPDIR", base)

	stale, err := EnsureSessionTemp("stale-old")
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := EnsureSessionTemp("fresh-new")
	if err != nil {
		t.Fatal(err)
	}
	// Backdate stale dir.
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	CleanupStaleSessionTemps(24 * time.Hour)
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale dir not removed: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("fresh dir removed: %v", err)
	}
	_ = CleanupSessionTemp("fresh-new")
}

func TestSanitizeSessionTempID(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"abc-123", "abc-123"},
		{"a/b", "a_b"},
		{"", ""},
		{".", ""},
		{"..", ""},
		{"  x  ", "x"},
	}
	for _, tc := range cases {
		if got := sanitizeSessionTempID(tc.in); got != tc.want {
			t.Fatalf("sanitize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestResolveAllowedPathSessionTemp(t *testing.T) {
	base := t.TempDir()
	t.Setenv("TMPDIR", base)
	work := t.TempDir()
	temp, err := EnsureSessionTemp("resolve-temp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = CleanupSessionTemp("resolve-temp") })

	// Create + resolve under session temp.
	target := filepath.Join(temp, "body.json")
	if err := os.WriteFile(target, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, display, err := resolveAllowedPath(work, temp, target)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(resolved) != filepath.Clean(target) {
		t.Fatalf("resolved = %q, want %q", resolved, target)
	}
	if display != resolved {
		t.Fatalf("display = %q, want absolute %q", display, resolved)
	}

	// Workspace relative still works.
	if err := os.WriteFile(filepath.Join(work, "in.txt"), []byte("w"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, rel, err := resolveAllowedPath(work, temp, "in.txt")
	if err != nil || rel != "in.txt" {
		t.Fatalf("workspace rel: rel=%q err=%v", rel, err)
	}

	// Sibling under system temp (not session dir) denied.
	sibling := filepath.Join(os.TempDir(), "not-strike-sibling.txt")
	if err := os.WriteFile(sibling, []byte("no"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(sibling) })
	_, _, err = resolveAllowedPath(work, temp, sibling)
	if err == nil {
		t.Fatal("sibling temp path allowed")
	}
	var esc *WorkspaceEscapeError
	if !errors.As(err, &esc) && !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("want escape error, got %v", err)
	}

	// Other session's temp denied.
	other, err := EnsureSessionTemp("other-sess")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = CleanupSessionTemp("other-sess") })
	otherFile := filepath.Join(other, "secret")
	if err := os.WriteFile(otherFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err = resolveAllowedPath(work, temp, otherFile)
	if err == nil {
		t.Fatal("other session temp allowed")
	}

	// Relative paths always bind to the workspace (missing leaf OK), never to temp.
	relResolved, relDisplay, err := resolveAllowedPath(work, temp, "body.json")
	if err != nil {
		t.Fatalf("relative workspace path: %v", err)
	}
	wantRel := filepath.Join(work, "body.json")
	if filepath.Clean(relResolved) != filepath.Clean(wantRel) {
		t.Fatalf("relative resolved to %q, want workspace %q (not temp)", relResolved, wantRel)
	}
	if relDisplay != "body.json" {
		t.Fatalf("relative display = %q", relDisplay)
	}
	// Ensure we did not pick the temp file of the same name.
	if filepath.Clean(relResolved) == filepath.Clean(target) {
		t.Fatal("relative path resolved to session temp file")
	}

	// Traversal out of temp denied.
	_, _, err = resolveAllowedPath(work, temp, filepath.Join(temp, "..", "escape.txt"))
	if err == nil {
		t.Fatal(".. traversal from temp allowed")
	}
}

func TestResolveAllowedPathSymlinkEscapeFromTemp(t *testing.T) {
	base := t.TempDir()
	t.Setenv("TMPDIR", base)
	work := t.TempDir()
	temp, err := EnsureSessionTemp("symlink-temp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = CleanupSessionTemp("symlink-temp") })

	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(temp, "outlink")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink: %v", err)
	}
	_, _, err = resolveAllowedPath(work, temp, filepath.Join(link, "secret.txt"))
	if err == nil {
		t.Fatal("symlink escape from session temp allowed")
	}
}

func TestWriteAndEditSessionTemp(t *testing.T) {
	base := t.TempDir()
	t.Setenv("TMPDIR", base)
	work := t.TempDir()
	temp, err := EnsureSessionTemp("write-edit")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = CleanupSessionTemp("write-edit") })

	tc := allowAll(work)
	tc.SessionTempDir = temp

	target := filepath.Join(temp, "req.json")
	_, err = NewWrite().Execute(t.Context(), mustJSON(t, map[string]any{
		"filePath": target,
		"content":  `{"ok":true}`,
	}), tc)
	if err != nil {
		t.Fatalf("write temp: %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != `{"ok":true}` {
		t.Fatalf("content = %q err=%v", data, err)
	}

	_, err = NewEdit().Execute(t.Context(), mustJSON(t, map[string]any{
		"filePath":  target,
		"oldString": `true`,
		"newString": `false`,
	}), tc)
	if err != nil {
		t.Fatalf("edit temp: %v", err)
	}
	data, _ = os.ReadFile(target)
	if string(data) != `{"ok":false}` {
		t.Fatalf("edited = %q", data)
	}

	// Sibling still denied via write tool.
	sibling := filepath.Join(os.TempDir(), "strike-sibling-deny.txt")
	_, err = NewWrite().Execute(t.Context(), mustJSON(t, map[string]any{
		"filePath": sibling,
		"content":  "nope",
	}), tc)
	if err == nil {
		t.Fatal("write sibling: want error")
	}
	if _, statErr := os.Stat(sibling); !os.IsNotExist(statErr) {
		_ = os.Remove(sibling)
		t.Fatal("sibling file was created")
	}
}

func TestWriteSessionTempAuditUsesAbsoluteDisplay(t *testing.T) {
	base := t.TempDir()
	t.Setenv("TMPDIR", base)
	work := t.TempDir()
	temp, err := EnsureSessionTemp("audit-path")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = CleanupSessionTemp("audit-path") })

	var patterns []string
	tc := &Context{
		WorkDir:        work,
		SessionTempDir: temp,
		Ask: func(_ context.Context, req AskRequest) error {
			patterns = append(patterns, req.Patterns...)
			return nil
		},
	}
	target := filepath.Join(temp, "audit.txt")
	res, err := NewWrite().Execute(t.Context(), mustJSON(t, map[string]any{
		"filePath": target,
		"content":  "hi",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if len(patterns) != 1 || patterns[0] != target && filepath.Clean(patterns[0]) != filepath.Clean(target) {
		// Allow symlink canonicalization of temp root.
		if len(patterns) != 1 || !strings.Contains(patterns[0], "audit.txt") {
			t.Fatalf("patterns = %v, want absolute under session temp", patterns)
		}
	}
	if !strings.Contains(res.Title, "audit.txt") {
		t.Fatalf("title = %q", res.Title)
	}
	// Must not leak arbitrary sibling host paths in title.
	if strings.Contains(res.Title, filepath.Join(os.TempDir(), "not-ours")) {
		t.Fatalf("title leaked unrelated path: %q", res.Title)
	}
}
