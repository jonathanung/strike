//go:build !unix

package tui

import "os/exec"

func configurePluginPaneCmd(cmd *exec.Cmd) {}

func killPluginPaneProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
