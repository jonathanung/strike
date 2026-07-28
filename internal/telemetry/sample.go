// Package telemetry collects local host resource samples (CPU, RAM, disk) for
// the TUI. Samples stay in-process only — never uploaded or attached to
// provider requests.
package telemetry

import (
	"context"
	"time"
)

// DefaultInterval is the default sampler period (~1 Hz).
const DefaultInterval = time.Second

// DefaultDiskInterval is how long Host reuses a disk Statfs result before the
// next probe. Disk changes slowly; caching avoids repeated Statfs on slow,
// network, or FUSE volumes (the main telemetry stall source when enabled).
const DefaultDiskInterval = 5 * time.Second

// Pressure thresholds for utilization ratios (used/total). Documented constants
// with sensible defaults; keep in sync with ui.Meter pressure bands.
const (
	// WarnRatio is the lower bound of warning pressure (inclusive).
	WarnRatio = 0.70
	// CritRatio is the exclusive lower bound of critical pressure.
	CritRatio = 0.90
)

// Level is utilization pressure for theme tone selection.
type Level int

const (
	LevelNormal Level = iota
	LevelWarning
	LevelCritical
	LevelUnknown
)

// Sample is one local resource snapshot. OK flags distinguish measured zeros
// from unavailable/unsupported values.
type Sample struct {
	// CPUHostPct is host-wide CPU utilization in [0, 100].
	CPUHostPct float64
	CPUHostOK  bool
	// CPUProcPct is this process's CPU utilization in [0, 100] when known.
	CPUProcPct float64
	CPUProcOK  bool

	MemUsedBytes  uint64
	MemTotalBytes uint64
	MemOK         bool

	DiskUsedBytes  uint64
	DiskTotalBytes uint64
	DiskFreeBytes  uint64
	DiskOK         bool
	// DiskRoot is the path whose filesystem was measured (project/worktree).
	DiskRoot string

	At time.Time
}

// Collector gathers one Sample. Implementations must respect ctx cancellation
// and must not upload data.
type Collector interface {
	Collect(ctx context.Context, root string) (Sample, error)
}

// Ratio returns used/total clamped to [0, 1]. ok is false when total is 0.
func Ratio(used, total uint64) (ratio float64, ok bool) {
	if total == 0 {
		return 0, false
	}
	r := float64(used) / float64(total)
	if r < 0 {
		r = 0
	}
	if r > 1 {
		r = 1
	}
	return r, true
}

// PercentRatio converts a 0–100 percent into a 0–1 ratio for meters.
func PercentRatio(pct float64, ok bool) (ratio float64, known bool) {
	if !ok {
		return -1, false
	}
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	return pct / 100, true
}

// LevelOf maps a utilization ratio to pressure. Unknown (ok=false) is LevelUnknown.
func LevelOf(ratio float64, ok bool) Level {
	if !ok {
		return LevelUnknown
	}
	switch {
	case ratio > CritRatio:
		return LevelCritical
	case ratio >= WarnRatio:
		return LevelWarning
	default:
		return LevelNormal
	}
}

// MemRatio is used/total memory when MemOK.
func (s Sample) MemRatio() (float64, bool) {
	if !s.MemOK {
		return 0, false
	}
	return Ratio(s.MemUsedBytes, s.MemTotalBytes)
}

// DiskRatio is used/total disk when DiskOK.
func (s Sample) DiskRatio() (float64, bool) {
	if !s.DiskOK {
		return 0, false
	}
	return Ratio(s.DiskUsedBytes, s.DiskTotalBytes)
}

// CPURatio is host CPU percent as a 0–1 ratio when known.
func (s Sample) CPURatio() (float64, bool) {
	return PercentRatio(s.CPUHostPct, s.CPUHostOK)
}
