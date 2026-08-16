package tool

import (
	"errors"
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

// resolveAllowedPath resolves path under the session workspace root, or — when
// path is absolute — under the optional session temporary directory. Relative
// paths always resolve against workDir only so agents must use the exposed
// absolute session temp path (never a silent second relative root).
//
// display is workspace-relative when under workDir; for session-temp hits it is
// the absolute resolved path (session-scoped; no unrelated host paths).
func resolveAllowedPath(workDir, tempDir, path string) (resolved, display string, err error) {
	path = mapEvalMountPath(strings.TrimSpace(path), workDir)
	if path == "" {
		return "", "", fmt.Errorf("path is empty")
	}

	// Prefer workspace so a path that somehow lies under both stays workspace-scoped.
	resolved, rel, err := resolveInWorkspace(workDir, path)
	if err == nil {
		return resolved, rel, nil
	}
	workspaceErr := err
	var esc *WorkspaceEscapeError
	// Non-escape failures (empty workdir, resolve errors) are fatal as-is when
	// the path is not an absolute temp candidate.
	if !errors.As(err, &esc) {
		// Still allow absolute temp paths when workDir is misconfigured empty
		// only if we have a temp root — otherwise return the original error.
		if strings.TrimSpace(workDir) != "" || !filepath.IsAbs(path) {
			return "", "", err
		}
	}

	tempDir = strings.TrimSpace(tempDir)
	if tempDir == "" || !filepath.IsAbs(path) {
		return "", "", workspaceErr
	}

	resolved, _, err = resolveInWorkspace(tempDir, path)
	if err != nil {
		// Keep the workspace escape message for arbitrary outside paths so
		// callers still see "escapes workspace root" rather than a temp-root
		// message that could confuse agents.
		return "", "", workspaceErr
	}
	// Audit/display: absolute path under the session temp dir only.
	return resolved, resolved, nil
}

// workspaceWriteFile re-validates path under root immediately before writing,
// refuses a symlink leaf, and commits via temp+rename (atomic on local POSIX)
// so readers never see a partial file. Pair with resolveInWorkspace so a
// symlink planted after the initial resolve cannot redirect the write outside
// the workspace.
func workspaceWriteFile(root, path string, data []byte) error {
	return allowedWriteFile(root, "", path, data)
}

// allowedWriteFile is workspaceWriteFile plus the optional session temp root.
func allowedWriteFile(workDir, tempDir, path string, data []byte) error {
	resolved, _, err := resolveAllowedPath(workDir, tempDir, path)
	if err != nil {
		return err
	}
	dir := filepath.Dir(resolved)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// MkdirAll can race with a parent→symlink swap; re-resolve before open.
	resolved, _, err = resolveAllowedPath(workDir, tempDir, path)
	if err != nil {
		return err
	}
	// If the leaf is still a symlink after resolve (should not happen when
	// resolve returns the target), refuse rather than follow.
	if fi, lerr := os.Lstat(resolved); lerr == nil && fi.Mode()&os.ModeSymlink != 0 {
		root := workDir
		if tempDir != "" {
			if _, _, tErr := resolveInWorkspace(tempDir, path); tErr == nil {
				root = tempDir
			}
		}
		return &WorkspaceEscapeError{Path: path, Root: root}
	}
	return atomicWriteFile(resolved, data, 0o644)
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

// isSymlinkLeaf reports whether the user-named path's final component is a
// symlink (Lstat, no follow). Used by move/delete to refuse operating through
// a symlink leaf after resolveAllowedPath would otherwise return the target.
func isSymlinkLeaf(workDir, tempDir, path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	candidates := make([]string, 0, 2)
	if filepath.IsAbs(path) {
		candidates = append(candidates, filepath.Clean(path))
	} else if strings.TrimSpace(workDir) != "" {
		candidates = append(candidates, filepath.Join(workDir, filepath.Clean(path)))
	}
	// Absolute session-temp paths are already covered; relative never uses temp.
	for _, c := range candidates {
		if fi, err := os.Lstat(c); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			return true
		}
	}
	_ = tempDir // reserved for future dual-root leaf checks
	return false
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
