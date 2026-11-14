//go:build windows

package keepawake

import "os/exec"

// setProcAttr is a no-op: no backend runs on windows yet (backendFor reports
// unsupported), so the exec path is never reached in production.
func setProcAttr(*exec.Cmd) {}

func killTree(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
