//go:build unix

package tui

import (
	"os"
	"os/exec"
	"syscall"
)

func configurePluginPaneCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killPluginPaneProcess(cmd *exec.Cmd) error {
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
