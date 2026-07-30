//go:build darwin

package telemetry

import (
	"context"
	"testing"
	"time"
)

// macOS aggregates per-core CPU ticks lazily: the counters move for a few
// milliseconds, freeze for roughly a second, then flush in bulk. Any live
// assertion has to wait for a flush rather than assume a short sleep moves them.
func waitForTickAdvance(t *testing.T, prev [cpuStateMax]uint32) [cpuStateMax]uint32 {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		cur, ok := machHostCPUTicks()
		if !ok {
			t.Fatal("machHostCPUTicks not ok")
		}
		if cur != prev {
			return cur
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("CPU tick counters did not advance within 5s")
	return prev
}

// The Mach call reaches libSystem through //go:linkname plus an asm trampoline.
// That is a supported pattern (golang.org/x/sys/unix uses it) but it is not
// type-checked against libSystem, so a toolchain change could break it at
// runtime rather than at build time. This asserts it really returns kernel
// ticks — the whole point of #602 was a CPU reading that silently never worked.
func TestMachHostCPUTicksLive(t *testing.T) {
	ticks, ok := machHostCPUTicks()
	if !ok {
		t.Fatal("machHostCPUTicks not ok — libSystem trampoline or host port broken")
	}
	var total uint64
	for _, v := range ticks {
		total += uint64(v)
	}
	if total == 0 {
		t.Fatal("all CPU tick counters are zero; the Mach call returned no data")
	}
	// Any booted machine has accumulated idle time.
	if ticks[cpuStateIdle] == 0 {
		t.Error("idle ticks are zero, which no booted machine reports")
	}

	// Counters must advance, and must not run backwards when they do.
	next := waitForTickAdvance(t, ticks)
	if next[cpuStateIdle]-ticks[cpuStateIdle] > 1<<31 {
		t.Errorf("idle ticks moved backwards: %d -> %d", ticks[cpuStateIdle], next[cpuStateIdle])
	}
}

func TestReadHostCPUMonotonicAndAdvancing(t *testing.T) {
	// First read establishes the widener baseline and returns zeroes.
	if _, _, ok := readHostCPU(); !ok {
		t.Fatal("readHostCPU not ok")
	}
	prevTicks, _ := machHostCPUTicks()
	_, prevTotal, ok := readHostCPU()
	if !ok {
		t.Fatal("readHostCPU not ok on second call")
	}
	for i := 0; i < 3; i++ {
		prevTicks = waitForTickAdvance(t, prevTicks)
		idle, total, ok := readHostCPU()
		if !ok {
			t.Fatalf("call %d: readHostCPU not ok", i)
		}
		if total < prevTotal {
			t.Fatalf("call %d: total went backwards %d -> %d", i, prevTotal, total)
		}
		if idle > total {
			t.Fatalf("call %d: idle %d exceeds total %d", i, idle, total)
		}
		// Guards against the counters being stuck, which would make the
		// monotonicity checks above pass vacuously.
		if total == prevTotal {
			t.Errorf("call %d: total did not advance past %d after a tick flush", i, prevTotal)
		}
		prevTotal = total
	}
}

// Regression for #602: the CPU row read "unavailable" on every Mac.
func TestCollectReportsHostCPUOnDarwin(t *testing.T) {
	h := NewHost()
	ctx := context.Background()
	if _, err := h.Collect(ctx, "."); err != nil {
		t.Fatalf("first Collect: %v", err)
	}
	ticks, _ := machHostCPUTicks()
	waitForTickAdvance(t, ticks)

	s, err := h.Collect(ctx, ".")
	if err != nil {
		t.Fatalf("second Collect: %v", err)
	}
	if !s.CPUHostOK {
		t.Fatal("CPUHostOK = false on macOS — #602 regression")
	}
	if s.CPUHostPct < 0 || s.CPUHostPct > 100 {
		t.Errorf("CPUHostPct = %v, want within [0, 100]", s.CPUHostPct)
	}
	if got := FormatCPULine(s, false); got == Unavailable {
		t.Error("CPU line renders unavailable")
	}
}
