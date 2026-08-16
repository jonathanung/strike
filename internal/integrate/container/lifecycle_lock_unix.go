//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package container

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

const lifecycleLockName = "lifecycle.lock"
const attachLockName = "attach.lock"

// lockLifecycle serializes managed-container discovery and mutation across
// Strike processes for one repository.
func lockLifecycle(ctx context.Context, path string) (func() error, error) {
	return lockContainerFile(ctx, path, unix.LOCK_EX)
}

func lockAttachShared(ctx context.Context, path string) (func() error, error) {
	return lockContainerFile(ctx, path, unix.LOCK_SH)
}

func lockAttachExclusive(ctx context.Context, path string) (func() error, error) {
	return lockContainerFile(ctx, path, unix.LOCK_EX)
}

func lockContainerFile(ctx context.Context, path string, mode int) (func() error, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("container lifecycle lock: mkdir: %w", err)
	}
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC, 0o644)
	if err != nil {
		return nil, fmt.Errorf("container lifecycle lock: open: %w", err)
	}
	for {
		err = unix.Flock(fd, mode|unix.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EINTR) {
			_ = unix.Close(fd)
			return nil, fmt.Errorf("container lifecycle lock: %w", err)
		}
		select {
		case <-ctx.Done():
			_ = unix.Close(fd)
			return nil, fmt.Errorf("container lifecycle lock: %w", ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	return func() error {
		_ = unix.Flock(fd, unix.LOCK_UN)
		return os.NewFile(uintptr(fd), path).Close()
	}, nil
}
