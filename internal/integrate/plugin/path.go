package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ResolveUnderRoot resolves rel against root and ensures the result stays
// inside root after cleaning and symlink evaluation. Returns the absolute path.
func ResolveUnderRoot(root, rel string) (string, error) {
	if err := validateRelPathSyntax(rel); err != nil {
		return "", err
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(rootAbs); err == nil {
		rootAbs = resolved
	}
	// Clean relative path with forward slashes normalized.
	rel = filepath.FromSlash(strings.ReplaceAll(rel, `\`, "/"))
	joined := filepath.Join(rootAbs, rel)
	clean := filepath.Clean(joined)

	// Before symlink eval: cleaned path must still be under root.
	if !isUnder(rootAbs, clean) {
		return "", fmt.Errorf("path %q escapes plugin root", rel)
	}

	// If the target exists, evaluate symlinks and re-check confinement.
	if fi, err := os.Lstat(clean); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 || fi.IsDir() {
			resolved, err := filepath.EvalSymlinks(clean)
			if err != nil {
				// Dangling symlink or intermediate missing — treat as escape/fail closed.
				return "", fmt.Errorf("path %q: %w", rel, err)
			}
			if !isUnder(rootAbs, resolved) {
				return "", fmt.Errorf("path %q escapes plugin root via symlink", rel)
			}
			return resolved, nil
		}
		// Regular file: eval parent for symlink parents.
		parent := filepath.Dir(clean)
		if resolvedParent, err := filepath.EvalSymlinks(parent); err == nil {
			resolved := filepath.Join(resolvedParent, filepath.Base(clean))
			if !isUnder(rootAbs, resolved) {
				return "", fmt.Errorf("path %q escapes plugin root via symlink", rel)
			}
			return resolved, nil
		}
	}
	return clean, nil
}

func isUnder(root, path string) bool {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if root == path {
		return true
	}
	sep := string(os.PathSeparator)
	return strings.HasPrefix(path, root+sep)
}
