package telemetry

import "testing"

func ticks(user, system, idle, nice uint32) [cpuStateMax]uint32 {
	var t [cpuStateMax]uint32
	t[cpuStateUser] = user
	t[cpuStateSystem] = system
	t[cpuStateIdle] = idle
	t[cpuStateNice] = nice
	return t
}

func TestMachTickWidenerBaselineThenAccumulate(t *testing.T) {
	var w machTickWidener

	// First call only establishes the baseline — a percentage needs two samples.
	if idle, total := w.add(ticks(1000, 500, 8000, 0)); idle != 0 || total != 0 {
		t.Fatalf("first add = (%d, %d), want (0, 0)", idle, total)
	}

	idle, total := w.add(ticks(1010, 505, 8085, 0))
	if want := uint64(85); idle != want {
		t.Errorf("idle = %d, want %d", idle, want)
	}
	if want := uint64(10 + 5 + 85); total != want {
		t.Errorf("total = %d, want %d", total, want)
	}

	// Deltas accumulate rather than reset.
	idle, total = w.add(ticks(1020, 510, 8170, 0))
	if want := uint64(170); idle != want {
		t.Errorf("idle = %d, want %d", idle, want)
	}
	if want := uint64(20 + 10 + 170); total != want {
		t.Errorf("total = %d, want %d", total, want)
	}
}

// The kernel's cpu_ticks are natural_t (uint32) summed across every core, so on
// a busy many-core Mac they roll over in roughly two months of uptime. Feeding
// raw values to sampleCPU's delta logic would underflow deltaIdle and report a
// bogus 100%.
func TestMachTickWidenerSurvivesRollover(t *testing.T) {
	var w machTickWidener
	const max = ^uint32(0)

	w.add(ticks(max-5, max-2, max-10, 0))

	// Every counter wraps past zero between samples.
	idle, total := w.add(ticks(4, 7, 9, 0))
	if want := uint64(20); idle != want { // (max-10) -> 9 is 20 ticks
		t.Errorf("idle across wrap = %d, want %d", idle, want)
	}
	if want := uint64(10 + 10 + 20); total != want {
		t.Errorf("total across wrap = %d, want %d", total, want)
	}

	// Busy fraction stays in range — the bug this guards against produced a
	// deltaIdle larger than deltaTotal.
	if idle > total {
		t.Fatalf("idle %d exceeds total %d", idle, total)
	}
}

func TestMachTickWidenerMonotonicAcrossManyWraps(t *testing.T) {
	var w machTickWidener
	const step = 1 << 30 // four steps per wrap

	var prevIdle, prevTotal uint64
	cur := ticks(0, 0, 0, 0)
	for i := 0; i < 40; i++ {
		cur[cpuStateUser] += step / 4
		cur[cpuStateIdle] += step
		idle, total := w.add(cur)
		if idle < prevIdle {
			t.Fatalf("step %d: idle went backwards %d -> %d", i, prevIdle, idle)
		}
		if total < prevTotal {
			t.Fatalf("step %d: total went backwards %d -> %d", i, prevTotal, total)
		}
		if idle > total {
			t.Fatalf("step %d: idle %d exceeds total %d", i, idle, total)
		}
		prevIdle, prevTotal = idle, total
	}
	// 39 accumulated deltas of step idle plus step/4 user.
	if want := uint64(39) * step; prevIdle != want {
		t.Errorf("idle = %d, want %d", prevIdle, want)
	}
}

// macOS folds nice into user, so nice reads 0 in practice — but it is still
// summed so total matches the kernel's own accounting if that ever changes.
func TestMachTickWidenerCountsAllStates(t *testing.T) {
	var w machTickWidener
	w.add(ticks(0, 0, 0, 0))
	_, total := w.add(ticks(1, 2, 4, 8))
	if want := uint64(15); total != want {
		t.Errorf("total = %d, want %d (every CPU_STATE must be summed)", total, want)
	}
}
