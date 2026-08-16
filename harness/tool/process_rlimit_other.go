//go:build unix && !linux

package tool

// applyProcessResourceLimits is a documented no-op on non-Linux unix
// (macOS/BSD lack a portable prlimit equivalent for child PIDs).
// Wall-time Timeout still applies via context; see docs/isolation.md.
func applyProcessResourceLimits(pid int, lim ProcessLimits) error {
	_ = pid
	_ = lim
	return nil
}

func processRlimitEnforced() bool { return false }
