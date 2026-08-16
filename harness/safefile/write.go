package safefile

import (
	"fmt"
	"os"
	"path/filepath"
)

// WriteFile writes data to path via temp+rename after refusing symlink leaves
// and special files. Creates parent directories as needed (0755). Mode defaults
// to 0o644; existing regular files keep their permission bits.
func WriteFile(path string, data []byte, perm os.FileMode) error {
	if path == "" {
		return errf(CodeInvalidPath, path, "path is empty")
	}
	if perm == 0 {
		perm = 0o644
	}
	if err := CheckLeaf(path, true); err != nil {
		return err
	}
	mode := perm
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode().IsRegular() {
			mode = fi.Mode().Perm()
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// Re-check after MkdirAll (parent could have been swapped to a symlink).
	if err := CheckLeaf(path, true); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, ".strike-safe-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
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
	// Final TOCTOU check before rename.
	if err := CheckLeaf(path, true); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	ok = true
	return nil
}
