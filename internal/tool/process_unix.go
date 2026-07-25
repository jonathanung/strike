//go:build unix

package tool

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

// processWaitDelay bounds how long Wait blocks after cancel when orphaned
// grandchildren keep I/O pipes open.
const processWaitDelay = 2 * time.Second

func configureProcessCmd(cmd *exec.Cmd) {
	// Own process group so cancel can kill shell + children together.
	// Default CommandContext only kills the direct child; grandchildren that
	// still hold stdout/stderr keep Wait blocked forever (pipe EOF never comes).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if err == nil {
			return nil
		}
		if err == syscall.ESRCH {
			return os.ErrProcessDone
		}
		if killErr := cmd.Process.Kill(); killErr == nil || killErr == os.ErrProcessDone {
			return killErr
		}
		return err
	}
	cmd.WaitDelay = processWaitDelay
}
