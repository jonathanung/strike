package telemetry

import (
	"context"
	"testing"
)

// scriptedCPU returns a readHostCPU stand-in that walks a fixed sequence of
// (idle, total) counter readings, repeating the last one once exhausted.
func scriptedCPU(readings [][2]uint64) func() (uint64, uint64, bool) {
	i := 0
	return func() (uint64, uint64, bool) {
		r := readings[i]
		if i < len(readings)-1 {
			i++
		}
		return r[0], r[1], true
	}
}

func TestSampleCPUHostPercent(t *testing.T) {
	h := NewHost()
	h.readHostCPUFn = scriptedCPU([][2]uint64{
		{0, 0},       // baseline
		{750, 1000},  // 25% busy
		{1500, 3000}, // delta idle 750 of 2000 -> 62.5% busy
	})
	ctx := context.Background()

	s, _ := h.Collect(ctx, "")
	if s.CPUHostOK {
		t.Error("first sample cannot produce a percent; want CPUHostOK false")
	}

	s, _ = h.Collect(ctx, "")
	if !s.CPUHostOK {
		t.Fatal("second sample: CPUHostOK = false")
	}
	if s.CPUHostPct != 25 {
		t.Errorf("CPUHostPct = %v, want 25", s.CPUHostPct)
	}

	s, _ = h.Collect(ctx, "")
	if s.CPUHostPct != 62.5 {
		t.Errorf("CPUHostPct = %v, want 62.5", s.CPUHostPct)
	}
}

// macOS flushes per-core ticks about once a second, so at the 1 Hz
// DefaultInterval a sample can land in a window where nothing moved. Reporting
// unavailable there would flicker the row between a number and "unavailable".
func TestSampleCPUHoldsPercentWhenCountersStall(t *testing.T) {
	h := NewHost()
	h.readHostCPUFn = scriptedCPU([][2]uint64{
		{0, 0},
		{750, 1000}, // 25% busy
		{750, 1000}, // stalled — identical reading
	})
	ctx := context.Background()

	h.Collect(ctx, "")
	if s, _ := h.Collect(ctx, ""); s.CPUHostPct != 25 {
		t.Fatalf("CPUHostPct = %v, want 25", s.CPUHostPct)
	}

	s, _ := h.Collect(ctx, "")
	if !s.CPUHostOK {
		t.Error("stalled counters should hold the last percent, not report unavailable")
	}
	if s.CPUHostPct != 25 {
		t.Errorf("CPUHostPct = %v, want the held value 25", s.CPUHostPct)
	}
}

// A stall before any percent has been computed still has nothing to report.
func TestSampleCPUStallBeforeFirstPercentStaysUnavailable(t *testing.T) {
	h := NewHost()
	h.readHostCPUFn = scriptedCPU([][2]uint64{{500, 1000}})
	ctx := context.Background()

	h.Collect(ctx, "")
	if s, _ := h.Collect(ctx, ""); s.CPUHostOK {
		t.Error("CPUHostOK = true with no delta and no prior percent")
	}
}

// A failing platform read must stay unavailable — the hold-last path applies
// only when the read succeeded but the counters did not move.
func TestSampleCPUUnavailableWhenReadFails(t *testing.T) {
	h := NewHost()
	h.readHostCPUFn = func() (uint64, uint64, bool) { return 0, 0, false }
	ctx := context.Background()

	h.Collect(ctx, "")
	if s, _ := h.Collect(ctx, ""); s.CPUHostOK {
		t.Error("CPUHostOK = true when the platform read failed")
	}
}

// Idle should never exceed total, but the subtraction is unsigned: without a
// clamp it underflows to ~1.8e19 and reports a maximally *busy* CPU for a
// sample whose idle time exceeded its total — the opposite of the truth.
func TestSampleCPUIdleExceedingTotalReadsAsIdle(t *testing.T) {
	h := NewHost()
	h.readHostCPUFn = scriptedCPU([][2]uint64{
		{0, 0},
		{5000, 1000}, // idle grew 5x faster than total
	})
	ctx := context.Background()

	h.Collect(ctx, "")
	s, _ := h.Collect(ctx, "")
	if !s.CPUHostOK {
		t.Fatal("CPUHostOK = false")
	}
	if s.CPUHostPct != 0 {
		t.Errorf("CPUHostPct = %v, want 0 — more idle than total cannot mean busy", s.CPUHostPct)
	}
}

// A stalled counter may substitute the last percent only briefly. Past that the
// reading is not measuring anything and a stale number is worse than
// "unavailable" — the #602/#521 failure mode.
func TestSampleCPUHoldExpires(t *testing.T) {
	stalled := [2]uint64{750, 1000}
	readings := [][2]uint64{{0, 0}, stalled}
	for i := 0; i < maxHeldHostCPUSamples+2; i++ {
		readings = append(readings, stalled)
	}
	h := NewHost()
	h.readHostCPUFn = scriptedCPU(readings)
	ctx := context.Background()

	h.Collect(ctx, "")
	if s, _ := h.Collect(ctx, ""); s.CPUHostPct != 25 {
		t.Fatalf("CPUHostPct = %v, want 25", s.CPUHostPct)
	}
	for i := 0; i < maxHeldHostCPUSamples; i++ {
		s, _ := h.Collect(ctx, "")
		if !s.CPUHostOK {
			t.Fatalf("hold %d: CPUHostOK = false, want the held value", i)
		}
		if s.CPUHostPct != 25 {
			t.Errorf("hold %d: CPUHostPct = %v, want 25", i, s.CPUHostPct)
		}
	}
	if s, _ := h.Collect(ctx, ""); s.CPUHostOK {
		t.Errorf("hold did not expire after %d stalled samples", maxHeldHostCPUSamples)
	}
}

// A sample whose counters sit below the previous one is a stale read, not a
// dead counter: overlapping Collect calls reach h.mu in an order independent of
// when they read the counters. Blanking the row there would reintroduce exactly
// the flicker the hold exists to remove.
func TestSampleCPUStaleSampleHoldsRatherThanBlanking(t *testing.T) {
	h := NewHost()
	h.readHostCPUFn = scriptedCPU([][2]uint64{
		{0, 0},
		{1500, 3000}, // 50% busy
		{750, 1000},  // an older read landing late
	})
	ctx := context.Background()

	h.Collect(ctx, "")
	if s, _ := h.Collect(ctx, ""); s.CPUHostPct != 50 {
		t.Fatalf("CPUHostPct = %v, want 50", s.CPUHostPct)
	}
	s, _ := h.Collect(ctx, "")
	if !s.CPUHostOK {
		t.Error("a stale sample blanked the CPU row instead of holding")
	}
	if s.CPUHostPct != 50 {
		t.Errorf("CPUHostPct = %v, want the held value 50", s.CPUHostPct)
	}
}

// A stale sample must not drag the baseline backwards, or the next real sample
// spans an interval that has already been reported and double-counts it.
func TestSampleCPUStaleSampleDoesNotRewindBaseline(t *testing.T) {
	h := NewHost()
	h.readHostCPUFn = scriptedCPU([][2]uint64{
		{0, 0},
		{1500, 3000}, // 50% busy, baseline now (1500, 3000)
		{750, 1000},  // stale read
		{2000, 4000}, // real next sample: delta from (1500,3000) is 500 idle of 1000 -> 50%
	})
	ctx := context.Background()

	h.Collect(ctx, "")
	h.Collect(ctx, "")
	h.Collect(ctx, "")
	s, _ := h.Collect(ctx, "")
	if !s.CPUHostOK {
		t.Fatal("CPUHostOK = false after a real sample following a stale one")
	}
	// Had the stale sample rewound the baseline to (750, 1000), the delta would
	// be 1250 idle of 3000 -> ~58.3% busy instead of 50%.
	if s.CPUHostPct != 50 {
		t.Errorf("CPUHostPct = %v, want 50 — baseline was rewound by the stale sample", s.CPUHostPct)
	}
}

// A recovered counter must start reporting again.
func TestSampleCPURecoversAfterStall(t *testing.T) {
	h := NewHost()
	h.readHostCPUFn = scriptedCPU([][2]uint64{
		{0, 0},
		{750, 1000},  // 25%
		{750, 1000},  // stall
		{1500, 3000}, // resumes: delta idle 750 of 2000 -> 62.5%
	})
	ctx := context.Background()

	h.Collect(ctx, "")
	h.Collect(ctx, "")
	h.Collect(ctx, "")
	s, _ := h.Collect(ctx, "")
	if !s.CPUHostOK {
		t.Fatal("CPUHostOK = false after counters resumed")
	}
	if s.CPUHostPct != 62.5 {
		t.Errorf("CPUHostPct = %v, want 62.5", s.CPUHostPct)
	}
}
