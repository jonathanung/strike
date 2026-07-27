package tool

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WorkspaceEscapeError is a hard boundary failure: the path resolves outside
// the session workspace root. Permission skip / yolo cannot override this.
type WorkspaceEscapeError struct {
	Path string
	Root string
}

func (e *WorkspaceEscapeError) Error() string {
	if e == nil {
		return "path escapes workspace root"
	}
	if e.Path == "" {
		return fmt.Sprintf("path escapes workspace root %q", e.Root)
	}
	return fmt.Sprintf("path %q escapes workspace root %q", e.Path, e.Root)
}

// resolveInWorkspace joins path under root and requires the final EvalSymlinks
// target to stay inside the physical root. Rejects absolute paths outside root,
// ".." escapes, and symlink escapes. Missing leaves are allowed when every
// existing parent stays under root.
func resolveInWorkspace(root, path string) (resolved, rel string, err error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", "", fmt.Errorf("work directory is empty")
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return "", "", fmt.Errorf("path is empty")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", "", fmt.Errorf("resolve work directory: %w", err)
	}
	rootReal, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		// Workspace root must exist (session workdir). Fall back to Abs only
		// when the root itself is missing so unit tests with fresh temps work
		// before Mkdir — but EvalSymlinks failure for other reasons is fatal.
		if !os.IsNotExist(err) {
			return "", "", fmt.Errorf("resolve work directory: %w", err)
		}
		rootReal = rootAbs
	}

	var candidate string
	if filepath.IsAbs(path) {
		candidate = filepath.Clean(path)
	} else {
		cleaned := filepath.Clean(path)
		if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
			return "", "", &WorkspaceEscapeError{Path: path, Root: rootReal}
		}
		candidate = filepath.Join(rootReal, cleaned)
	}

	real, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		if !os.IsNotExist(err) {
			return "", "", &WorkspaceEscapeError{Path: path, Root: rootReal}
		}
		if err := ensureWorkspacePrefix(rootReal, candidate); err != nil {
			return "", "", err
		}
		rel, relErr := filepath.Rel(rootReal, candidate)
		if relErr != nil || !relInside(rel) {
			return "", "", &WorkspaceEscapeError{Path: path, Root: rootReal}
		}
		return candidate, filepath.ToSlash(rel), nil
	}
	rel, err = filepath.Rel(rootReal, real)
	if err != nil || !relInside(rel) {
		return "", "", &WorkspaceEscapeError{Path: path, Root: rootReal}
	}
	return real, filepath.ToSlash(rel), nil
}

// ensureWorkspacePrefix checks that every existing prefix of candidate stays
// under rootReal when the leaf does not exist yet.
func ensureWorkspacePrefix(rootReal, candidate string) error {
	cur := candidate
	for {
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		realParent, err := filepath.EvalSymlinks(parent)
		if err == nil {
			rel, relErr := filepath.Rel(rootReal, realParent)
			if relErr != nil || !relInside(rel) {
				return &WorkspaceEscapeError{Path: candidate, Root: rootReal}
			}
			return nil
		}
		if !os.IsNotExist(err) {
			return &WorkspaceEscapeError{Path: candidate, Root: rootReal}
		}
		cur = parent
	}
	rel, err := filepath.Rel(rootReal, filepath.Clean(candidate))
	if err != nil || !relInside(rel) {
		return &WorkspaceEscapeError{Path: candidate, Root: rootReal}
	}
	return nil
}

func relInside(rel string) bool {
	if rel == "" || rel == "." {
		return true
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return !filepath.IsAbs(rel)
}

// workspaceRootReal returns the physical absolute workspace root.
func workspaceRootReal(root string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", fmt.Errorf("work directory is empty")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve work directory: %w", err)
	}
	rootReal, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		if os.IsNotExist(err) {
			return rootAbs, nil
		}
		return "", fmt.Errorf("resolve work directory: %w", err)
	}
	return rootReal, nil
}
