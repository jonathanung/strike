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
	_, prevTotal, ok := readHostCPU()
	if !ok {
		t.Fatal("readHostCPU not ok on second call")
	}
	for i := 0; i < 3; i++ {
		// Poll readHostCPU itself rather than waiting on a separate snapshot the
		// widener never consumed: doing the latter can return a tick the next
		// read has already folded in, landing inside the same stall window.
		var idle, total uint64
		deadline := time.Now().Add(5 * time.Second)
		for {
			idle, total, ok = readHostCPU()
			if !ok {
				t.Fatalf("call %d: readHostCPU not ok", i)
			}
			if total < prevTotal {
				t.Fatalf("call %d: total went backwards %d -> %d", i, prevTotal, total)
			}
			if idle > total {
				t.Fatalf("call %d: idle %d exceeds total %d", i, idle, total)
			}
			// Advancing at all is the real assertion — a stuck counter would
			// make the monotonicity checks above pass vacuously.
			if total > prevTotal {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("call %d: total stuck at %d for 5s", i, prevTotal)
			}
			time.Sleep(20 * time.Millisecond)
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

// Regression for the lock-scope bug found in review: readHostCPU used to take
// the Mach snapshot outside hostCPUTicks.mu and only lock around the fold. Two
// concurrent Collect calls could then hand the widener snapshots in reverse
// order; the uint32 delta underflows and adds ~2^32 ticks, pinning the CPU row
// at 100% on an idle machine. -race cannot see it — the mutex was present, its
// scope was wrong.
//
// Asserting the invariant directly rather than hammering: a probabilistic
// concurrency test is load-dependent and did not reliably fail against the
// pre-fix code. If the mutex can be acquired while the snapshot is being taken,
// the snapshot is unprotected and the reorder is possible.
func TestReadHostCPUTakesSnapshotUnderLock(t *testing.T) {
	restore := hostCPUSnapshot
	defer func() { hostCPUSnapshot = restore }()

	var lockedDuringSnapshot, called bool
	hostCPUSnapshot = func() ([cpuStateMax]uint32, bool) {
		called = true
		// TryLock succeeding means the mutex is free, i.e. the snapshot is
		// running outside the critical section.
		if hostCPUTicks.mu.TryLock() {
			hostCPUTicks.mu.Unlock()
			lockedDuringSnapshot = false
		} else {
			lockedDuringSnapshot = true
		}
		return machHostCPUTicks()
	}

	readHostCPU()

	if !called {
		t.Fatal("hostCPUSnapshot was not used; the indirection is wired wrong")
	}
	if !lockedDuringSnapshot {
		t.Error("snapshot taken outside hostCPUTicks.mu — concurrent Collect calls can fold snapshots out of order")
	}
}

// The fold must reject a snapshot older than the previous one even if it somehow
// arrives, so corruption is impossible independently of the lock. Paired with
// TestMachTickWidenerRejectsOutOfOrderSnapshot, which pins the arithmetic.
func TestReadHostCPURejectsRegressedSnapshot(t *testing.T) {
	restore := hostCPUSnapshot
	defer func() { hostCPUSnapshot = restore }()

	cur, ok := machHostCPUTicks()
	if !ok {
		t.Fatal("machHostCPUTicks not ok")
	}
	older := cur
	older[cpuStateIdle] -= 500

	hostCPUSnapshot = func() ([cpuStateMax]uint32, bool) { return cur, true }
	readHostCPU()
	_, before, _ := readHostCPU()

	hostCPUSnapshot = func() ([cpuStateMax]uint32, bool) { return older, true }
	idle, total, ok := readHostCPU()
	if !ok {
		return // rejected outright, which is the preferred outcome
	}
	// Folding a 500-tick regression underflows to just under 2^32, so compare
	// the jump rather than the absolute value.
	if total < before {
		t.Fatalf("total went backwards: %d -> %d", before, total)
	}
	if jump := total - before; jump > uint64(maxPlausibleTickDelta) {
		t.Fatalf("regressed snapshot inflated counters by %d ticks (%d -> %d)", jump, before, total)
	}
	if idle > total {
		t.Fatalf("idle %d exceeds total %d", idle, total)
	}
}
