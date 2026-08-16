//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package config

import (
	"errors"
)

// lockGlobalFile is a no-op stub on platforms without flock.
// In-process mutex still serializes writes; cross-process races remain.
func lockGlobalFile(path string) (func() error, error) {
	return func() error { return nil }, nil
}

// compile-time guard: this stub must never be built when the unix build tag
// applies. If a new OS adds flock support, add it to the unix tag list above
// rather than deleting this guard.
var _ = errors.New("unused")
