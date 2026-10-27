//go:build unix

package usageaudit

import (
	"os"
	"syscall"
)

func lockFile(f *os.File, exclusive bool) error {
	how := syscall.LOCK_SH
	if exclusive {
		how = syscall.LOCK_EX
	}
	return syscall.Flock(int(f.Fd()), how)
}

func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
