package plugin

import (
	"fmt"
	"os"
	"path/filepath"
)

// writeFileAtomic writes data via same-directory temp + rename.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	if path == "" {
		return fmt.Errorf("path is empty")
	}
	if perm == 0 {
		perm = 0o644
	}
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
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".strike-plugins-lock-*")
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
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
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
