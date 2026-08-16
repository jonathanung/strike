//go:build windows

package container

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
)

const lifecycleLockName = "lifecycle.lock"
const attachLockName = "attach.lock"

func lockLifecycle(ctx context.Context, path string) (func() error, error) {
	return lockContainerFile(ctx, path, true)
}

func lockAttachShared(ctx context.Context, path string) (func() error, error) {
	return lockContainerFile(ctx, path, false)
}

func lockAttachExclusive(ctx context.Context, path string) (func() error, error) {
	return lockContainerFile(ctx, path, true)
}

func lockContainerFile(ctx context.Context, path string, exclusive bool) (func() error, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("container lock: mkdir: %w", err)
	}
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("container lock: open: %w", err)
	}
	flags := uint32(windows.LOCKFILE_FAIL_IMMEDIATELY)
	if exclusive {
		flags |= windows.LOCKFILE_EXCLUSIVE_LOCK
	}
	overlapped := new(windows.Overlapped)
	for {
		err = windows.LockFileEx(windows.Handle(file.Fd()), flags, 0, 1, 0, overlapped)
		if err == nil {
			break
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			_ = file.Close()
			return nil, fmt.Errorf("container lock: %w", err)
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, fmt.Errorf("container lock: %w", ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	return func() error {
		_ = windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, overlapped)
		return file.Close()
	}, nil
}
