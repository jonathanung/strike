package telemetry

// Indices into host_cpu_load_info_data_t.cpu_ticks (mach/host_info.h), which is
// natural_t[CPU_STATE_MAX]. macOS folds nice into user, so cpuStateNice reads 0
// in practice; it is still summed so total matches the kernel's own accounting.
const (
	cpuStateUser   = 0
	cpuStateSystem = 1
	cpuStateIdle   = 2
	cpuStateNice   = 3
	cpuStateMax    = 4
)

// machTickWidener widens the kernel's 32-bit CPU tick counters to monotonic
// 64-bit totals.
//
// cpu_ticks are natural_t (uint32) accumulated across every core, so they roll
// over — on a 10-core Mac at 100 Hz per core the idle counter wraps after about
// 50 days of uptime. Feeding raw values to the delta logic in sampleCPU would
// underflow deltaIdle and report a bogus 100% for that sample. uint32
// subtraction wraps correctly, so accumulating per-state deltas stays exact
// across the rollover as long as sampling is far more frequent than the wrap.
//
// Pure (no syscalls) so Linux CI can lock the arithmetic.
type machTickWidener struct {
	prev  [cpuStateMax]uint32
	acc   [cpuStateMax]uint64
	begun bool
}

// add folds a fresh tick snapshot in and returns monotonic idle and total
// counters. The first call establishes the baseline and returns zeroes: the
// caller needs two samples to compute a percentage regardless.
func (w *machTickWidener) add(cur [cpuStateMax]uint32) (idle, total uint64) {
	if w.begun {
		for i := range cur {
			w.acc[i] += uint64(cur[i] - w.prev[i])
		}
	}
	w.prev = cur
	w.begun = true
	for _, v := range w.acc {
		total += v
	}
	return w.acc[cpuStateIdle], total
}
