//go:build linux

package tool

import "golang.org/x/sys/unix"

// applyProcessResourceLimits sets RLIMIT_AS / RLIMIT_CPU on pid via prlimit(2).
// Zero fields are skipped. Returns the first error encountered; callers treat
// failures as best-effort (capability / kernel may refuse).
func applyProcessResourceLimits(pid int, lim ProcessLimits) error {
	if pid <= 0 {
		return nil
	}
	var first error
	if lim.MemoryBytes > 0 {
		rlim := unix.Rlimit{Cur: lim.MemoryBytes, Max: lim.MemoryBytes}
		if err := unix.Prlimit(pid, unix.RLIMIT_AS, &rlim, nil); err != nil && first == nil {
			first = err
		}
	}
	if lim.CPUSeconds > 0 {
		rlim := unix.Rlimit{Cur: lim.CPUSeconds, Max: lim.CPUSeconds}
		if err := unix.Prlimit(pid, unix.RLIMIT_CPU, &rlim, nil); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// processRlimitEnforced reports whether ProcessLimits are applied on this OS.
func processRlimitEnforced() bool { return true }
