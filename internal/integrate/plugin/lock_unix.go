//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package plugin

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// lockFile acquires an exclusive advisory lock on a side-car lock path.
// The side-car is never renamed, so flock remains valid across atomic
// rewrites of the real lockfile content.
func lockFile(path string) (unlock func() error, err error) {
	side := path + ".lock"
	if err := os.MkdirAll(filepath.Dir(side), 0o755); err != nil {
		return nil, fmt.Errorf("create lockfile dir: %w", err)
	}
	fd, err := unix.Open(side, unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open lock side-car: %w", err)
	}
	if err := unix.Flock(fd, unix.LOCK_EX); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("lock lockfile: %w", err)
	}
	return func() error {
		_ = unix.Flock(fd, unix.LOCK_UN)
		return os.NewFile(uintptr(fd), side).Close()
	}, nil
}
