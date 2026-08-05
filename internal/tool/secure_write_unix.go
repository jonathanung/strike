//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package tool

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// writeFileNoFollow writes data to path without following a final-component
// symlink. O_NOFOLLOW closes the TOCTOU window where a leaf is replaced with a
// symlink between resolveInWorkspace and open.
func writeFileNoFollow(path string, data []byte, perm os.FileMode) error {
	if perm == 0 {
		perm = 0o644
	}
	flags := unix.O_WRONLY | unix.O_CREAT | unix.O_TRUNC | unix.O_NOFOLLOW | unix.O_CLOEXEC
	fd, err := unix.Open(path, flags, uint32(perm.Perm()))
	if err != nil {
		if err == unix.ELOOP {
			return fmt.Errorf("refusing to write through symlink %q", path)
		}
		return err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	if err := f.Chmod(perm); err != nil {
		return err
	}
	n, err := f.Write(data)
	if err != nil {
		return err
	}
	if n < len(data) {
		return fmt.Errorf("short write to %s: %d/%d bytes", path, n, len(data))
	}
	return f.Sync()
}
