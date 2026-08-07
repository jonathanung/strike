package safefile

import (
	"os"
	"path/filepath"
	"strings"
)

// Identity returns a normalized absolute path suitable for grant matching and
// path-equality checks. It cleans the path, resolves the existing prefix with
// EvalSymlinks when possible, and uses a stable slash-free cleaned absolute
// form. Missing leaves keep the cleaned absolute path under the resolved parent.
//
// Empty path returns CodeInvalidPath.
func Identity(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errf(CodeInvalidPath, path, "path is empty")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", errf(CodeInvalidPath, path, "abs: %v", err)
	}
	abs = filepath.Clean(abs)

	// Resolve as much as exists.
	real, err := filepath.EvalSymlinks(abs)
	if err == nil {
		return filepath.Clean(real), nil
	}
	if !os.IsNotExist(err) {
		// Dangling symlink leaf: resolve parent + keep base name.
		if fi, lerr := os.Lstat(abs); lerr == nil && fi.Mode()&os.ModeSymlink != 0 {
			parent, perr := filepath.EvalSymlinks(filepath.Dir(abs))
			if perr != nil {
				parent = filepath.Clean(filepath.Dir(abs))
			}
			return filepath.Join(parent, filepath.Base(abs)), nil
		}
		return "", errf(CodeInvalidPath, path, "eval symlinks: %v", err)
	}

	// Walk up to an existing ancestor, resolve it, rejoin the tail.
	cur := abs
	var tail []string
	for {
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		if realParent, err := filepath.EvalSymlinks(parent); err == nil {
			tail = append([]string{filepath.Base(cur)}, tail...)
			return filepath.Clean(filepath.Join(append([]string{realParent}, tail...)...)), nil
		} else if !os.IsNotExist(err) {
			return "", errf(CodeInvalidPath, path, "eval parent: %v", err)
		}
		tail = append([]string{filepath.Base(cur)}, tail...)
		cur = parent
	}
	return abs, nil
}

// SameIdentity reports whether a and b normalize to the same identity.
func SameIdentity(a, b string) (bool, error) {
	ia, err := Identity(a)
	if err != nil {
		return false, err
	}
	ib, err := Identity(b)
	if err != nil {
		return false, err
	}
	return ia == ib, nil
}
