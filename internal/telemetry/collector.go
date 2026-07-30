package telemetry

import (
	"context"
	"os"
	"sync"
	"time"
)

// maxHeldHostCPUSamples bounds how many consecutive samples may reuse the last
// host CPU percent while the kernel counters sit still. macOS flushes per-core
// ticks roughly once a second, so at the 1 Hz DefaultInterval one or two stalled
// samples are normal; beyond that the counter is not advancing for a reason the
// aggregation gap explains, and a stale number is worse than "unavailable".
const maxHeldHostCPUSamples = 3

// Host is the default platform collector for macOS/Linux. It is safe for
// concurrent Collect calls but keeps CPU deltas in internal state, so prefer
// one instance per process.
//
// Disk Statfs results are cached for DefaultDiskInterval and probed off-thread
// so slow/network/FUSE volumes cannot block Collect past ctx or stack
// unbounded refresh goroutines.
type Host struct {
	mu sync.Mutex

	// Previous host CPU counters for delta percent.
	prevHostTotal uint64
	prevHostIdle  uint64
	prevHostOK    bool

	// Last computed host percent, reused when the kernel counters have not
	// advanced since the previous sample. macOS aggregates per-core ticks
	// lazily (roughly once a second), so at the 1 Hz DefaultInterval a sample
	// can land inside a window with no movement. heldHostSamples bounds how
	// long that substitution may continue.
	prevHostPct     float64
	prevHostPctOK   bool
	heldHostSamples int

	// Previous process CPU time (ns) and wall time for process percent.
	prevProcNS int64
	prevWall   time.Time
	prevProcOK bool

	// pid is fixed at construction for process CPU.
	pid int

	// Disk cache: reuse last successful (or failed) probe for diskTTL.
	diskRoot            string
	diskUsed            uint64
	diskTotal           uint64
	diskFree            uint64
	diskOK              bool
	diskAt              time.Time
	diskTTL             time.Duration // 0 → DefaultDiskInterval
	diskRefreshInFlight bool

	// Optional hooks for tests. nil → platform defaults.
	readDiskFn    func(root string) (used, total, free uint64, ok bool)
	readHostCPUFn func() (idle, total uint64, ok bool)
	nowFn         func() time.Time
}

// NewHost builds a Host collector for the current process.
func NewHost() *Host {
	return &Host{pid: os.Getpid()}
}

func (h *Host) now() time.Time {
	if h.nowFn != nil {
		return h.nowFn()
	}
	return time.Now()
}

func (h *Host) diskCacheTTL() time.Duration {
	if h.diskTTL > 0 {
		return h.diskTTL
	}
	return DefaultDiskInterval
}

// Collect gathers CPU, memory, and disk for root. Partial failures mark the
// corresponding OK flag false rather than inventing zeros.
//
// Collect respects ctx: already-canceled contexts return immediately with
// ctx.Err(). Mid-collect cancellation returns any metrics gathered so far
// (err == nil) so the UI can keep CPU/RAM fresh when disk Statfs is slow.
// Disk probes run off-thread and are cached for DefaultDiskInterval so a
// hung Statfs cannot block the caller past ctx and cannot stack unbounded
// refresh goroutines.
func (h *Host) Collect(ctx context.Context, root string) (Sample, error) {
	if err := ctx.Err(); err != nil {
		return Sample{}, err
	}
	var s Sample
	s.At = h.now()
	s.DiskRoot = root

	h.sampleCPU(&s)
	if err := ctx.Err(); err != nil {
		return s, nil
	}

	h.sampleMem(&s)
	if err := ctx.Err(); err != nil {
		return s, nil
	}

	if root != "" {
		h.sampleDisk(ctx, &s, root)
	}
	return s, nil
}

func (h *Host) sampleCPU(s *Sample) {
	readCPU := readHostCPU
	if h.readHostCPUFn != nil {
		readCPU = h.readHostCPUFn
	}
	idle, total, hostOK := readCPU()
	procNS, procOK := readProcessCPUTime(h.pid)
	now := h.now()

	h.mu.Lock()
	defer h.mu.Unlock()

	if hostOK {
		switch {
		case !h.prevHostOK:
			// First reading only establishes the baseline.
		case total > h.prevHostTotal:
			deltaTotal := total - h.prevHostTotal
			deltaIdle := idle - h.prevHostIdle
			busy := float64(deltaTotal-deltaIdle) / float64(deltaTotal) * 100
			if busy < 0 {
				busy = 0
			}
			if busy > 100 {
				busy = 100
			}
			s.CPUHostPct = busy
			s.CPUHostOK = true
			h.prevHostPct = busy
			h.prevHostPctOK = true
			h.heldHostSamples = 0
		case total == h.prevHostTotal && h.prevHostPctOK && h.heldHostSamples < maxHeldHostCPUSamples:
			// Counters have not moved yet. Hold the last percent rather than
			// flickering the row to "unavailable" — see prevHostPct.
			h.heldHostSamples++
			s.CPUHostPct = h.prevHostPct
			s.CPUHostOK = true
		default:
			// Either the counters went backwards, or they have been frozen for
			// longer than the aggregation gap explains. Both mean the reading
			// is no longer measuring anything, so stop publishing a stale
			// number and let the row read "unavailable".
			h.prevHostPctOK = false
			h.heldHostSamples = 0
		}
		h.prevHostTotal = total
		h.prevHostIdle = idle
		h.prevHostOK = true
	}

	if procOK {
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
}

func (h *Host) sampleMem(s *Sample) {
	used, cached, total, ok, cachedOK := readMemory()
	if ok {
		s.MemUsedBytes = used
		s.MemTotalBytes = total
		s.MemOK = true
		if cachedOK {
			s.MemCachedBytes = cached
			s.MemCachedOK = true
		}
	}
	swapUsed, swapTotal, swapOK := readSwap()
	if swapOK {
		s.SwapUsedBytes = swapUsed
		s.SwapTotalBytes = swapTotal
		s.SwapOK = true
	}
}

// sampleDisk fills disk fields from cache and/or a non-blocking probe.
// Never blocks past ctx; never stacks multiple Statfs goroutines.
func (h *Host) sampleDisk(ctx context.Context, s *Sample, root string) {
	ttl := h.diskCacheTTL()
	now := h.now()

	h.mu.Lock()
	cacheHit := root == h.diskRoot && !h.diskAt.IsZero() && now.Sub(h.diskAt) < ttl
	if cacheHit {
		if h.diskOK {
			s.DiskUsedBytes = h.diskUsed
			s.DiskTotalBytes = h.diskTotal
			s.DiskFreeBytes = h.diskFree
			s.DiskOK = true
		}
		h.mu.Unlock()
		return
	}
	// Stale-while-revalidate: serve last value for this root while a refresh runs.
	staleOK := root == h.diskRoot && h.diskOK
	if staleOK {
		s.DiskUsedBytes = h.diskUsed
		s.DiskTotalBytes = h.diskTotal
		s.DiskFreeBytes = h.diskFree
		s.DiskOK = true
	}
	if h.diskRefreshInFlight {
		h.mu.Unlock()
		return
	}
	h.diskRefreshInFlight = true
	h.mu.Unlock()

	fn := readDisk
	if h.readDiskFn != nil {
		fn = h.readDiskFn
	}

	type diskRes struct {
		used, total, free uint64
		ok                bool
	}
	ch := make(chan diskRes, 1)
	go func() {
		u, t, f, ok := fn(root)
		h.mu.Lock()
		h.diskRoot = root
		h.diskUsed, h.diskTotal, h.diskFree = u, t, f
		h.diskOK = ok
		h.diskAt = h.now()
		h.diskRefreshInFlight = false
		h.mu.Unlock()
		ch <- diskRes{u, t, f, ok}
	}()

	select {
	case <-ctx.Done():
		// Keep stale fields already copied; first probe yields DiskOK=false.
		return
	case r := <-ch:
		if r.ok {
			s.DiskUsedBytes = r.used
			s.DiskTotalBytes = r.total
			s.DiskFreeBytes = r.free
			s.DiskOK = true
		} else {
			s.DiskOK = false
		}
	}
}
