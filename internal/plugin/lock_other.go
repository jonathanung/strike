//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// process-local fallback when flock is unavailable.
var (
	lockMu      sync.Mutex
	lockHolders = map[string]*os.File{}
)

func lockFile(path string) (unlock func() error, err error) {
	side := path + ".lock"
	lockMu.Lock()
	defer lockMu.Unlock()
	if err := os.MkdirAll(filepath.Dir(side), 0o755); err != nil {
		return nil, fmt.Errorf("create lockfile dir: %w", err)
	}
	f, err := os.OpenFile(side, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open lock side-car: %w", err)
	}
	lockHolders[side] = f
	return func() error {
		lockMu.Lock()
		defer lockMu.Unlock()
		f := lockHolders[side]
		delete(lockHolders, side)
		if f != nil {
			return f.Close()
		}
		return nil
	}, nil
}
