package terminal

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ResolveCWD resolves where a terminal may start. workspaceRoot is the
// registered workspace directory. requested is empty (the workspace itself),
// a relative path inside it, or an absolute path that must still resolve
// inside it. Symlinks that escape the workspace are rejected.
func ResolveCWD(workspaceRoot, requested string) (string, error) {
	root, err := canonicalDir(workspaceRoot)
	if err != nil {
		return "", fmt.Errorf("workspace: %w", err)
	}
	requested = strings.TrimSpace(requested)
	var abs string
	if requested == "" {
		abs = root
	} else if filepath.IsAbs(requested) {
		abs = filepath.Clean(requested)
	} else {
		if filepath.IsAbs(requested) || strings.HasPrefix(requested, "/") {
			return "", fmt.Errorf("cwd is outside the workspace")
		}
		abs = filepath.Clean(filepath.Join(root, requested))
	}
	if !inside(root, abs) {
		return "", fmt.Errorf("cwd is outside the workspace")
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("cwd: %w", err)
	}
	resolved = filepath.Clean(resolved)
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("cwd: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("cwd is not a directory")
	}
	if !inside(root, resolved) {
		return "", fmt.Errorf("cwd is outside the workspace")
	}
	return resolved, nil
}

func canonicalDir(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("path is empty")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	resolved = filepath.Clean(resolved)
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", resolved)
	}
	return resolved, nil
}

// inside reports whether target is root or a directory under root.
// Both paths must already be cleaned. Relative escape uses filepath.Rel
// so a sibling prefix (root "/ws" vs "/ws-other") does not count.
func inside(root, target string) bool {
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	if root == target {
		return true
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// SameDirectory reports whether two directories are the same path after
// symlink resolution. Empty inputs are not the same.
func SameDirectory(a, b string) bool {
	aa, errA := canonicalDir(a)
	bb, errB := canonicalDir(b)
	if errA != nil || errB != nil {
		return false
	}
	return aa == bb
}
