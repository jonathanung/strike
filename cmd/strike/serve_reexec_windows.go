//go:build windows

package main

import "errors"

// serveReexecSupported is false: Windows has no exec-in-place, so a
// workspace trust change keeps the manual-restart path (#1834).
const serveReexecSupported = false

func serveReexec() error {
	return errors.New("restart in place is not supported on Windows")
}
