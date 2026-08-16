package telemetry

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestHostCollectSlowDiskHonorsContext(t *testing.T) {
	h := NewHost()
	var started atomic.Int32
	release := make(chan struct{})
	h.readDiskFn = func(root string) (used, total, free uint64, ok bool) {
		started.Add(1)
		<-release
		return 1, 4, 3, true
	}
	h.nowFn = func() time.Time { return time.Unix(100, 0) }

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	start := time.Now()
	s, err := h.Collect(ctx, "/slow")
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Collect err = %v (want partial sample, nil err)", err)
	}
	if s.DiskOK {
		t.Fatal("DiskOK on timed-out first probe")
	}
	if elapsed > 200*time.Millisecond {
		t.Fatalf("Collect blocked %v despite ctx timeout", elapsed)
	}
	// Wait until the probe is observed so we can release without leaking.
	deadline := time.Now().Add(time.Second)
	for started.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if started.Load() == 0 {
		t.Fatal("disk probe never started")
	}
	close(release)
	// Allow refresh goroutine to finish and cache the result.
	deadline = time.Now().Add(time.Second)
	for {
		h.mu.Lock()
		inflight := h.diskRefreshInFlight
		ok := h.diskOK && h.diskRoot == "/slow"
		h.mu.Unlock()
		if !inflight && ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("refresh goroutine did not finish")
		}
		time.Sleep(time.Millisecond)
	}

	// Next collect should use cache without blocking on disk again.
	var secondCalls atomic.Int32
	h.readDiskFn = func(string) (uint64, uint64, uint64, bool) {
		secondCalls.Add(1)
		t.Error("disk probed again within cache TTL")
		return 0, 0, 0, false
	}
	h.nowFn = func() time.Time { return time.Unix(101, 0) } // +1s < DefaultDiskInterval
	s2, err := h.Collect(context.Background(), "/slow")
	if err != nil {
		t.Fatal(err)
	}
	if !s2.DiskOK || s2.DiskUsedBytes != 1 || s2.DiskTotalBytes != 4 {
		t.Fatalf("cached disk = %+v", s2)
	}
	if secondCalls.Load() != 0 {
		t.Fatalf("unexpected disk probes: %d", secondCalls.Load())
	}
}

func TestHostCollectDiskCacheTTL(t *testing.T) {
	h := NewHost()
	h.diskTTL = 100 * time.Millisecond
	var calls atomic.Int32
	h.readDiskFn = func(root string) (uint64, uint64, uint64, bool) {
		n := calls.Add(1)
		return uint64(n), 10, 10 - uint64(n), true
	}
	base := time.Unix(0, 0)
	var mu sync.Mutex
	now := base
	h.nowFn = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}

	s1, err := h.Collect(context.Background(), "/vol")
	if err != nil || !s1.DiskOK || s1.DiskUsedBytes != 1 {
		t.Fatalf("first = %+v err=%v", s1, err)
	}

	mu.Lock()
	now = base.Add(50 * time.Millisecond)
	mu.Unlock()
	s2, err := h.Collect(context.Background(), "/vol")
	if err != nil || s2.DiskUsedBytes != 1 {
		t.Fatalf("cached = %+v err=%v", s2, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls during TTL = %d, want 1", calls.Load())
	}

	mu.Lock()
	now = base.Add(150 * time.Millisecond)
	mu.Unlock()
	s3, err := h.Collect(context.Background(), "/vol")
	if err != nil || !s3.DiskOK || s3.DiskUsedBytes != 2 {
		t.Fatalf("after TTL = %+v err=%v calls=%d", s3, err, calls.Load())
	}
	if calls.Load() != 2 {
		t.Fatalf("calls after TTL = %d, want 2", calls.Load())
	}
}

func TestHostCollectDiskSingleFlight(t *testing.T) {
	h := NewHost()
	var started atomic.Int32
	release := make(chan struct{})
	h.readDiskFn = func(string) (uint64, uint64, uint64, bool) {
		started.Add(1)
		<-release
		return 2, 8, 6, true
	}
	h.nowFn = func() time.Time { return time.Unix(0, 0) }

	// First collect starts a probe and times out before Statfs returns.
	ctx1, cancel1 := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel1()
	if _, err := h.Collect(ctx1, "/sf"); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(time.Second)
	for started.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}

	// Concurrent collects while probe in flight must not start another Statfs.
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = h.Collect(context.Background(), "/sf")
		}()
	}
	wg.Wait()
	if got := started.Load(); got != 1 {
		close(release)
		t.Fatalf("stacked disk probes: %d", got)
	}
	close(release)

	deadline = time.Now().Add(time.Second)
	for {
		h.mu.Lock()
		done := !h.diskRefreshInFlight && h.diskOK
		h.mu.Unlock()
		if done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("probe did not complete")
		}
		time.Sleep(time.Millisecond)
	}
	s, err := h.Collect(context.Background(), "/sf")
	if err != nil || !s.DiskOK || s.DiskUsedBytes != 2 {
		t.Fatalf("after singleflight = %+v err=%v", s, err)
	}
	if started.Load() != 1 {
		t.Fatalf("probes after complete = %d", started.Load())
	}
}

func TestHostCollectRootChangeBypassesCache(t *testing.T) {
	h := NewHost()
	var roots []string
	h.readDiskFn = func(root string) (uint64, uint64, uint64, bool) {
		roots = append(roots, root)
		if root == "/a" {
			return 1, 2, 1, true
		}
		return 3, 4, 1, true
	}
	h.nowFn = func() time.Time { return time.Unix(0, 0) }

	s1, _ := h.Collect(context.Background(), "/a")
	s2, _ := h.Collect(context.Background(), "/b")
	if !s1.DiskOK || s1.DiskUsedBytes != 1 {
		t.Fatalf("a = %+v", s1)
	}
	if !s2.DiskOK || s2.DiskUsedBytes != 3 {
		t.Fatalf("b = %+v", s2)
	}
	if len(roots) != 2 || roots[0] != "/a" || roots[1] != "/b" {
		t.Fatalf("roots = %v", roots)
	}
}

func TestHostCollectCanceledStillImmediate(t *testing.T) {
	h := NewHost()
	h.readDiskFn = func(string) (uint64, uint64, uint64, bool) {
		t.Error("disk should not run when ctx already canceled")
		return 0, 0, 0, false
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := h.Collect(ctx, "/"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestHostCollectStaleWhileRevalidate(t *testing.T) {
	h := NewHost()
	h.diskTTL = 10 * time.Millisecond
	var calls atomic.Int32
	blockSecond := make(chan struct{})
	h.readDiskFn = func(string) (uint64, uint64, uint64, bool) {
		n := calls.Add(1)
		if n >= 2 {
			<-blockSecond
		}
		return uint64(n * 10), 100, 100 - uint64(n*10), true
	}
	base := time.Unix(0, 0)
	var mu sync.Mutex
	now := base
	h.nowFn = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}

	s1, _ := h.Collect(context.Background(), "/vol")
	if s1.DiskUsedBytes != 10 {
		t.Fatalf("first = %+v", s1)
	}

	mu.Lock()
	now = base.Add(50 * time.Millisecond) // past TTL
	mu.Unlock()

	// Short ctx: should return stale 10 while refresh is in flight.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	s2, err := h.Collect(ctx, "/vol")
	if err != nil {
		t.Fatal(err)
	}
	if !s2.DiskOK || s2.DiskUsedBytes != 10 {
		t.Fatalf("stale-while-revalidate = %+v", s2)
	}
	close(blockSecond)

	deadline := time.Now().Add(time.Second)
	for calls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	// Advance clock within new cache window after refresh stores used=20.
	mu.Lock()
	now = base.Add(55 * time.Millisecond)
	mu.Unlock()
	deadline = time.Now().Add(time.Second)
	for {
		s3, err := h.Collect(context.Background(), "/vol")
		if err != nil {
			t.Fatal(err)
		}
		if s3.DiskUsedBytes == 20 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("never saw refreshed disk, last=%+v calls=%d", s3, calls.Load())
		}
		time.Sleep(time.Millisecond)
	}
}
