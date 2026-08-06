//go:build !unix

package tool

import (
	"os/exec"
	"time"
)

// processWaitDelay bounds how long Wait blocks after cancel when orphaned
// grandchildren keep I/O pipes open.
const processWaitDelay = 2 * time.Second

func configureProcessCmd(cmd *exec.Cmd) {
	// No portable process-group kill; WaitDelay still unblocks Wait if pipes
	// stay open after the direct child exits.
	cmd.WaitDelay = processWaitDelay
}

// applyProcessResourceLimits is unsupported outside unix (documented no-op).
func applyProcessResourceLimits(pid int, lim ProcessLimits) error {
	_ = pid
	_ = lim
	return nil
}

func processRlimitEnforced() bool { return false }
