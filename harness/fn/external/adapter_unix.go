//go:build unix

package external

import (
	"os"
	"os/exec"
	"syscall"
)

// configureHarnessCmd puts the harness in its own process group so cancel can
// kill the tree (same contract as tool.RunProcess on unix).
func configureHarnessCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killHarnessProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
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
