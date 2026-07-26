package telemetry

import (
	"fmt"
	"strconv"
	"strings"
)

// Unavailable is the explicit label for missing metrics (never silent zero).
const Unavailable = "unavailable"

// FormatBytes renders a byte count with binary units (KiB/MiB/GiB/TiB) using
// one fractional digit when helpful. n is treated as unsigned capacity.
func FormatBytes(n uint64) string {
	const (
		kiB = 1024
		miB = 1024 * kiB
		giB = 1024 * miB
		tiB = 1024 * giB
	)
	switch {
	case n >= tiB:
		return formatFrac(float64(n)/float64(tiB), "TB")
	case n >= giB:
		return formatFrac(float64(n)/float64(giB), "GB")
	case n >= miB:
		return formatFrac(float64(n)/float64(miB), "MB")
	case n >= kiB:
		return formatFrac(float64(n)/float64(kiB), "KB")
	default:
		return strconv.FormatUint(n, 10) + " B"
	}
}

func formatFrac(v float64, unit string) string {
	if v >= 100 {
		return strconv.FormatInt(int64(v+0.5), 10) + " " + unit
	}
	// One decimal when under 100 for readability (10.1 GB).
	s := strconv.FormatFloat(v, 'f', 1, 64)
	s = strings.TrimSuffix(s, ".0")
	return s + " " + unit
}

// FormatPercent renders a 0–100 utilization with one decimal place.
func FormatPercent(pct float64) string {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	return fmt.Sprintf("%.1f%%", pct)
}

// FormatMemLine builds the RAM summary without a bar:
// "10.1 GB / 32 GB used · 31.6%" or Unavailable.
func FormatMemLine(s Sample) string {
	if !s.MemOK {
		return Unavailable
	}
	r, ok := s.MemRatio()
	if !ok {
		return Unavailable
	}
	return FormatBytes(s.MemUsedBytes) + " / " + FormatBytes(s.MemTotalBytes) +
		" used · " + FormatPercent(r*100)
}

// FormatCPULine builds the CPU summary without a bar.
// Primary is host total; process CPU is appended when known and includeProc.
func FormatCPULine(s Sample, includeProc bool) string {
	if !s.CPUHostOK {
		return Unavailable
	}
	line := FormatPercent(s.CPUHostPct)
	if includeProc && s.CPUProcOK {
		line += " · proc " + FormatPercent(s.CPUProcPct)
	}
	return line
}

// FormatDiskLine builds the disk summary without a bar:
// "287 GB / 494 GB used · 207 GB free · 58.1%" or Unavailable.
func FormatDiskLine(s Sample) string {
	if !s.DiskOK {
		return Unavailable
	}
	r, ok := s.DiskRatio()
	if !ok {
		return Unavailable
	}
	return FormatBytes(s.DiskUsedBytes) + " / " + FormatBytes(s.DiskTotalBytes) +
		" used · " + FormatBytes(s.DiskFreeBytes) + " free · " + FormatPercent(r*100)
}
