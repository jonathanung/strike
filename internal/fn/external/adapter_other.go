//go:build !unix

package external

import "os/exec"

func configureHarnessCmd(cmd *exec.Cmd) {}

func killHarnessProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
