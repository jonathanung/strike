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

func TestSampleCPUClampsBogusDeltas(t *testing.T) {
	h := NewHost()
	// Idle grows faster than total, which would drive busy negative.
	h.readHostCPUFn = scriptedCPU([][2]uint64{
		{0, 0},
		{5000, 1000},
	})
	ctx := context.Background()

	h.Collect(ctx, "")
	s, _ := h.Collect(ctx, "")
	if !s.CPUHostOK {
		t.Fatal("CPUHostOK = false")
	}
	if s.CPUHostPct < 0 || s.CPUHostPct > 100 {
		t.Errorf("CPUHostPct = %v, want clamped within [0, 100]", s.CPUHostPct)
	}
}
