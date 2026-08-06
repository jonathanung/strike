package tool

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

// sessionTempSubdir is the fixed first path segment under os.TempDir().
const sessionTempSubdir = "strike"

// defaultStaleSessionTempAge bounds crash leftover cleanup. Active sessions
// refresh their dir mtime on Ensure; dirs older than this are removed.
const defaultStaleSessionTempAge = 24 * time.Hour

// SessionTempDir returns the absolute path of the private scratch directory for
// sessionID beneath the platform temp root (os.TempDir()/strike/<id>/).
// The directory is not created. sessionID is sanitized for the filesystem.
func SessionTempDir(sessionID string) string {
	id := sanitizeSessionTempID(sessionID)
	if id == "" {
		return ""
	}
	return filepath.Join(os.TempDir(), sessionTempSubdir, id)
}

// EnsureSessionTemp creates the session scratch directory (0700) and returns
// its absolute physical path. Empty sessionID returns ("", nil) so callers can
// disable the allowance. Also runs a best-effort stale cleanup under the
// strike temp root.
func EnsureSessionTemp(sessionID string) (string, error) {
	dir := SessionTempDir(sessionID)
	if dir == "" {
		return "", nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create session temp dir: %w", err)
	}
	// Touch mtime so concurrent Ensure keeps the dir off the stale list.
	now := time.Now()
	_ = os.Chtimes(dir, now, now)
	if real, err := filepath.EvalSymlinks(dir); err == nil && real != "" {
		dir = real
	}
	CleanupStaleSessionTemps(defaultStaleSessionTempAge)
	return dir, nil
}

// CleanupSessionTemp removes the session scratch directory and its contents.
// Missing directories are not an error. Empty sessionID is a no-op.
func CleanupSessionTemp(sessionID string) error {
	dir := SessionTempDir(sessionID)
	if dir == "" {
		return nil
	}
	// Refuse to remove anything outside the strike temp root.
	root := filepath.Join(os.TempDir(), sessionTempSubdir)
	rootReal := root
	if real, err := filepath.EvalSymlinks(root); err == nil && real != "" {
		rootReal = real
	}
	dirReal := dir
	if real, err := filepath.EvalSymlinks(dir); err == nil && real != "" {
		dirReal = real
	} else if abs, err := filepath.Abs(dir); err == nil {
		dirReal = abs
	}
	rel, err := filepath.Rel(rootReal, dirReal)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == "." {
		// "." would be the strike root itself — never remove that wholesale here.
		if rel == "." {
			return fmt.Errorf("refusing to remove session temp root %q", dirReal)
		}
		return fmt.Errorf("refusing to remove path outside session temp root: %q", dirReal)
	}
	if err := os.RemoveAll(dirReal); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// CleanupStaleSessionTemps removes strike/<id> directories whose mtime is older
// than maxAge. maxAge <= 0 uses defaultStaleSessionTempAge. Best-effort: errors
// on individual entries are skipped. Never removes the strike root itself.
func CleanupStaleSessionTemps(maxAge time.Duration) {
	if maxAge <= 0 {
		maxAge = defaultStaleSessionTempAge
	}
	root := filepath.Join(os.TempDir(), sessionTempSubdir)
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-maxAge)
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		// Only names that look like sanitized session ids.
		name := ent.Name()
		if sanitizeSessionTempID(name) != name {
			continue
		}
		path := filepath.Join(root, name)
		info, err := ent.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(cutoff) {
			continue
		}
		_ = os.RemoveAll(path)
	}
}

// sanitizeSessionTempID keeps a portable filesystem-safe session id segment.
// Empty or all-invalid input yields "".
func sanitizeSessionTempID(sessionID string) string {
	s := strings.TrimSpace(sessionID)
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	// Reject "." / ".." and empty after sanitize.
	if out == "" || out == "." || out == ".." {
		return ""
	}
	// Cap length to avoid pathological path components.
	const maxID = 128
	if len(out) > maxID {
		out = out[:maxID]
	}
	return out
}
