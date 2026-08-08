//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package container

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const lifecycleLockName = "lifecycle.lock"
const attachLockName = "attach.lock"

var (
	lifecycleLocksMu sync.Mutex
	lifecycleLocks   = map[string]chan struct{}{}
	attachLocksMu    sync.Mutex
	attachLocks      = map[string]*localAttachLock{}
)

type localAttachLock struct {
	readers int
	writer  bool
	changed chan struct{}
}

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

func lockAttachShared(ctx context.Context, path string) (func() error, error) {
	return lockLocalAttach(ctx, path, false)
}

func lockAttachExclusive(ctx context.Context, path string) (func() error, error) {
	return lockLocalAttach(ctx, path, true)
}

func lockLocalAttach(ctx context.Context, path string, exclusive bool) (func() error, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("container attach lock: mkdir: %w", err)
	}
	for {
		attachLocksMu.Lock()
		state := attachLocks[path]
		if state == nil {
			state = &localAttachLock{changed: make(chan struct{})}
			attachLocks[path] = state
		}
		available := !state.writer && (!exclusive || state.readers == 0)
		if available {
			if exclusive {
				state.writer = true
			} else {
				state.readers++
			}
			attachLocksMu.Unlock()
			return func() error {
				attachLocksMu.Lock()
				if exclusive {
					state.writer = false
				} else {
					state.readers--
				}
				close(state.changed)
				state.changed = make(chan struct{})
				attachLocksMu.Unlock()
				return nil
			}, nil
		}
		changed := state.changed
		attachLocksMu.Unlock()
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("container attach lock: %w", ctx.Err())
		case <-changed:
		}
	}
}
