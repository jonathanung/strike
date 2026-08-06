//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package tool

import (
	"fmt"
	"os"
)

// writeFileNoFollow is a best-effort fallback on platforms without O_NOFOLLOW:
// reject when the leaf is a symlink, then WriteFile. Still pair with
// workspaceWriteFile's exec-time re-resolve.
func writeFileNoFollow(path string, data []byte, perm os.FileMode) error {
	if perm == 0 {
		perm = 0o644
	}
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to write through symlink %q", path)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.WriteFile(path, data, perm)
}
