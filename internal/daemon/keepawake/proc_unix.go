//go:build !windows

package keepawake

import (
	"errors"
	"os/exec"
	"syscall"
)

// setProcAttr puts the inhibitor in its own process group so Kill reaches the
// wrapped command (systemd-inhibit → tail) and not only the wrapper.
func setProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killTree terminates the child's process group with SIGTERM. The group id
// equals the child pid because of Setpgid. ESRCH (already gone) is not an error.
func killTree(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	if err == nil || errors.Is(err, syscall.ESRCH) {
		return nil
	}
	// Fall back to the direct child when the group is not ours.
	return cmd.Process.Kill()
}
