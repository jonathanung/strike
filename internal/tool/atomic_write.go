package tool

import (
	"fmt"
	"os"
	"path/filepath"
)

// atomicWriteFile writes data to path via temp file + rename so readers never
// observe a partial file on local POSIX filesystems (same-directory rename is
// atomic). Preserves existing file mode when path already exists; otherwise
// uses perm (default 0o644). Refuses to replace a symlink leaf (pair with
// resolveInWorkspace / O_NOFOLLOW defenses).
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	if path == "" {
		return fmt.Errorf("path is empty")
	}
	if perm == 0 {
		perm = 0o644
	}
	// Match os.WriteFile: mode applies on create; existing files keep mode.
	mode := perm
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to write through symlink %q", path)
		}
		if fi.Mode().IsRegular() {
			mode = fi.Mode().Perm()
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".strike-write-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	// Ensure cleanup on any failure path before successful rename.
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmpName)
		}
	}()

	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	n, err := tmp.Write(data)
	if err != nil {
		_ = tmp.Close()
		return err
	}
	if n < len(data) {
		_ = tmp.Close()
		return fmt.Errorf("short write to %s: %d/%d bytes", path, n, len(data))
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	// TOCTOU: leaf became a symlink after Lstat — refuse rather than rename
	// over it (rename would replace the symlink inode, but callers expect error).
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to write through symlink %q", path)
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}

	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	ok = true
	return nil
}
