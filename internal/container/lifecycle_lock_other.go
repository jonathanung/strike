//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package container

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const lifecycleLockName = "lifecycle.lock"

var (
	lifecycleLocksMu sync.Mutex
	lifecycleLocks   = map[string]chan struct{}{}
)

// lockLifecycle is process-local on platforms without flock support.
func lockLifecycle(ctx context.Context, path string) (func() error, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("container lifecycle lock: mkdir: %w", err)
	}
	lifecycleLocksMu.Lock()
	sem := lifecycleLocks[path]
	if sem == nil {
		sem = make(chan struct{}, 1)
		sem <- struct{}{}
		lifecycleLocks[path] = sem
	}
	lifecycleLocksMu.Unlock()
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("container lifecycle lock: %w", ctx.Err())
	case <-sem:
		return func() error {
			sem <- struct{}{}
			return nil
		}, nil
	}
}
