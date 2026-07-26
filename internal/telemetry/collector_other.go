//go:build !linux && !darwin

package telemetry

// Unsupported platforms return unavailable metrics (ok=false).

func readHostCPU() (idle, total uint64, ok bool) { return 0, 0, false }

func readProcessCPUTime(pid int) (ns int64, ok bool) { return 0, false }

func readMemory() (used, total uint64, ok bool) { return 0, 0, false }

func readDisk(root string) (used, total, free uint64, ok bool) {
	return 0, 0, 0, false
}
