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
		// EvalSymlinks reports NotExist for dangling symlinks too. A final
		// component that is a symlink must still be checked — otherwise
		// os.WriteFile would follow it outside the workspace.
		if fi, lerr := os.Lstat(candidate); lerr == nil && fi.Mode()&os.ModeSymlink != 0 {
			return resolveDanglingSymlink(rootReal, path, candidate)
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

// resolveDanglingSymlink checks a final-component symlink whose target does not
// exist yet. The link target (absolute or relative to the link's parent) must
// stay inside rootReal; mutation tools never write through the symlink itself.
func resolveDanglingSymlink(rootReal, userPath, candidate string) (resolved, rel string, err error) {
	target, err := os.Readlink(candidate)
	if err != nil {
		return "", "", &WorkspaceEscapeError{Path: userPath, Root: rootReal}
	}
	var absTarget string
	if filepath.IsAbs(target) {
		absTarget = filepath.Clean(target)
	} else {
		absTarget = filepath.Join(filepath.Dir(candidate), target)
	}
	// Re-enter resolve for the target path (may itself be missing). Cap depth
	// by rejecting if the target path lexically escapes before recursion.
	if err := ensureWorkspacePrefix(rootReal, absTarget); err != nil {
		return "", "", &WorkspaceEscapeError{Path: userPath, Root: rootReal}
	}
	// Target is still missing: accept the cleaned target only if under root.
	rel, relErr := filepath.Rel(rootReal, absTarget)
	if relErr != nil || !relInside(rel) {
		return "", "", &WorkspaceEscapeError{Path: userPath, Root: rootReal}
	}
	// Return the physical target path (not the symlink) so writers open the
	// in-workspace leaf with O_NOFOLLOW rather than following the link.
	return absTarget, filepath.ToSlash(rel), nil
}

// workspaceWriteFile re-validates path under root immediately before writing and
// opens the leaf with O_NOFOLLOW (Unix) so a symlink planted after the initial
// resolve cannot redirect the write outside the workspace.
func workspaceWriteFile(root, path string, data []byte) error {
	resolved, _, err := resolveInWorkspace(root, path)
	if err != nil {
		return err
	}
	dir := filepath.Dir(resolved)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// MkdirAll can race with a parent→symlink swap; re-resolve before open.
	resolved, _, err = resolveInWorkspace(root, path)
	if err != nil {
		return err
	}
	// If the leaf is still a symlink after resolve (should not happen when
	// resolve returns the target), refuse rather than follow.
	if fi, lerr := os.Lstat(resolved); lerr == nil && fi.Mode()&os.ModeSymlink != 0 {
		return &WorkspaceEscapeError{Path: path, Root: root}
	}
	return writeFileNoFollow(resolved, data, 0o644)
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
