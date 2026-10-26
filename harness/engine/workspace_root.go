package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// childWorkDirAllowed reports whether dir may be a delegated child's working
// directory. Empty roots means the session is not bound to a workspace
// (plain strike serve, empty registry): every directory is allowed.
// A symlink that resolves outside the roots is rejected.
func childWorkDirAllowed(roots []string, dir string) error {
	if len(roots) == 0 {
		return nil
	}
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return fmt.Errorf("child working directory is empty")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("child working directory: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("child working directory: %w", err)
		}
		resolved = abs
	}
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		rootAbs, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		rootResolved, err := filepath.EvalSymlinks(rootAbs)
		if err != nil {
			rootResolved = rootAbs
		}
		if dirWithin(rootResolved, resolved) {
			return nil
		}
	}
	return fmt.Errorf("child working directory %s is outside the workspace roots", resolved)
}

func dirWithin(root, target string) bool {
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	if root == "" || target == "" {
		return false
	}
	if root == target {
		return true
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
