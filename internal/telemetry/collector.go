package telemetry

import (
	"context"
	"os"
	"sync"
	"time"
)

// Host is the default platform collector for macOS/Linux. It is safe for
// concurrent Collect calls but keeps CPU deltas in internal state, so prefer
// one instance per process.
type Host struct {
	mu sync.Mutex

	// Previous host CPU counters for delta percent.
	prevHostTotal uint64
	prevHostIdle  uint64
	prevHostOK    bool

	// Previous process CPU time (ns) and wall time for process percent.
	prevProcNS int64
	prevWall   time.Time
	prevProcOK bool

	// pid is fixed at construction for process CPU.
	pid int
}

// NewHost builds a Host collector for the current process.
func NewHost() *Host {
	return &Host{pid: os.Getpid()}
}

// Collect gathers CPU, memory, and disk for root. Partial failures mark the
// corresponding OK flag false rather than inventing zeros.
func (h *Host) Collect(ctx context.Context, root string) (Sample, error) {
	if err := ctx.Err(); err != nil {
		return Sample{}, err
	}
	var s Sample
	s.At = time.Now()
	s.DiskRoot = root

	h.mu.Lock()
	defer h.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return Sample{}, err
	}

	if idle, total, ok := readHostCPU(); ok {
		if h.prevHostOK && total > h.prevHostTotal {
			deltaTotal := total - h.prevHostTotal
			deltaIdle := idle - h.prevHostIdle
			if deltaTotal > 0 {
				busy := float64(deltaTotal-deltaIdle) / float64(deltaTotal) * 100
				if busy < 0 {
					busy = 0
				}
				if busy > 100 {
					busy = 100
				}
				s.CPUHostPct = busy
				s.CPUHostOK = true
			}
		}
		h.prevHostTotal = total
		h.prevHostIdle = idle
		h.prevHostOK = true
	}

	if procNS, ok := readProcessCPUTime(h.pid); ok {
		now := time.Now()
		if h.prevProcOK && !h.prevWall.IsZero() {
			wall := now.Sub(h.prevWall).Seconds()
			if wall > 0 && procNS >= h.prevProcNS {
				delta := float64(procNS-h.prevProcNS) / 1e9 // seconds of CPU
				pct := delta / wall * 100
				if pct < 0 {
					pct = 0
				}
				// Process can exceed 100% on multi-core; clamp display to 100*NumCPU later if needed.
				s.CPUProcPct = pct
				s.CPUProcOK = true
			}
		}
		h.prevProcNS = procNS
		h.prevWall = now
		h.prevProcOK = true
	}

	if used, total, ok := readMemory(); ok {
		s.MemUsedBytes = used
		s.MemTotalBytes = total
		s.MemOK = true
	}

	if root != "" {
		if used, total, free, ok := readDisk(root); ok {
			s.DiskUsedBytes = used
			s.DiskTotalBytes = total
			s.DiskFreeBytes = free
			s.DiskOK = true
		}
	}

	return s, nil
}
