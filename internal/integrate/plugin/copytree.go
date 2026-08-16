package plugin

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// copyTree copies src directory tree into dst (dst must not exist or be empty).
// Symlinks are copied as symlinks only when the referent stays inside src;
// escaping symlinks are rejected. Does not follow symlinks out of the tree.
func copyTree(src, dst string) error {
	srcAbs, err := filepath.Abs(src)
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(srcAbs); err == nil {
		srcAbs = resolved
	}
	st, err := os.Stat(srcAbs)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return fmt.Errorf("source is not a directory: %s", src)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	return filepath.WalkDir(srcAbs, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcAbs, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		// Skip VCS metadata in the copy (digest also skips .git).
		if d.IsDir() && d.Name() == ".git" {
			return fs.SkipDir
		}
		target := filepath.Join(dst, rel)
		if d.Type()&fs.ModeSymlink != 0 {
			return copyConfinedSymlink(srcAbs, path, target, rel)
		}
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			// Skip sockets, devices, etc.
			return nil
		}
		return copyFile(path, target)
	})
}

// copyConfinedSymlink copies path as a symlink at target when the referent stays
// under srcAbs. Escaping symlinks are rejected (same policy as copyTree).
func copyConfinedSymlink(srcAbs, path, target, rel string) error {
	link, err := os.Readlink(path)
	if err != nil {
		return err
	}
	var referent string
	if filepath.IsAbs(link) {
		referent = filepath.Clean(link)
	} else {
		referent = filepath.Clean(filepath.Join(filepath.Dir(path), link))
	}
	if resolved, err := filepath.EvalSymlinks(referent); err == nil {
		referent = resolved
	}
	if !isUnder(srcAbs, referent) && referent != srcAbs {
		return fmt.Errorf("symlink %s escapes source root", rel)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	return os.Symlink(link, target)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// isGitURL reports whether s looks like a git remote rather than a local path.
func isGitURL(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	lower := strings.ToLower(s)
	if strings.HasPrefix(lower, "git@") {
		return true
	}
	if strings.HasPrefix(lower, "ssh://") {
		return true
	}
	if strings.HasPrefix(lower, "git://") {
		return true
	}
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		// Heuristic: github/gitlab style or ends with .git
		if strings.HasSuffix(lower, ".git") {
			return true
		}
		if strings.Contains(lower, "github.com/") || strings.Contains(lower, "gitlab.com/") ||
			strings.Contains(lower, "bitbucket.org/") || strings.Contains(lower, "codeberg.org/") {
			return true
		}
		// Explicit git host path with .git in middle
		if strings.Contains(lower, ".git/") {
			return true
		}
	}
	return false
}
