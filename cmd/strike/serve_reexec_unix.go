//go:build !windows

package main

import (
	"os"
	"syscall"
)

// serveReexecSupported reports that strike serve can replace itself in
// place to apply a workspace trust change (#1834).
const serveReexecSupported = true

// serveReexec replaces this process with the same binary, argv and
// environment. The PID is kept, so a supervisor (strike daemon, systemd)
// sees no exit. Go opens every descriptor close-on-exec, so the listener and
// project pipes do not leak into the new image.
func serveReexec() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return syscall.Exec(exe, os.Args, os.Environ())
}
