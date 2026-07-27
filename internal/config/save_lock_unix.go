//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package config

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// lockGlobalFile acquires an exclusive advisory lock on the global config
// file. Ensures the parent directory exists. Returns a cleanup function that
// releases the lock and closes the file descriptor. The lock protects
// cross-process read-modify-write on ~/.strike/config.
func lockGlobalFile(path string) (func() error, error) {
	// Ensure the parent directory exists so O_CREAT can succeed.
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create config dir: %w", err)
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CREAT|unix.O_CLOEXEC, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open config for lock: %w", err)
	}
	if err := unix.Flock(fd, unix.LOCK_EX); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("lock config: %w", err)
	}
	unlock := func() error {
		// Best-effort unlock; close always follows.
		_ = unix.Flock(fd, unix.LOCK_UN)
		return os.NewFile(uintptr(fd), path).Close()
	}
	return unlock, nil
}
