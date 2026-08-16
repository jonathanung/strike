//go:build unix

package update

import (
	"fmt"
	"os"
	"syscall"
)

// reexec replaces the current process with exe and the given argv (without argv0).
func reexec(exe string, args []string) error {
	argv := make([]string, 0, 1+len(args))
	argv = append(argv, exe)
	argv = append(argv, args...)
	if err := syscall.Exec(exe, argv, os.Environ()); err != nil {
		return fmt.Errorf("exec %s: %w", exe, err)
	}
	return nil
}
