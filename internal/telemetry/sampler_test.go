package telemetry

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeCollector struct {
	mu          sync.Mutex
	samples     []Sample
	errs        []error
	delay       time.Duration
	roots       []string
	calls       atomic.Int32
	inFlight    atomic.Int32
	maxInFlight atomic.Int32
}

func (f *fakeCollector) Collect(ctx context.Context, root string) (Sample, error) {
	cur := f.inFlight.Add(1)
	for {
		prev := f.maxInFlight.Load()
		if cur <= prev || f.maxInFlight.CompareAndSwap(prev, cur) {
			break
		}
	}
	defer f.inFlight.Add(-1)

	f.mu.Lock()
	f.roots = append(f.roots, root)
	delay := f.delay
	idx := int(f.calls.Load())
	f.mu.Unlock()

	if delay > 0 {
		select {
		case <-ctx.Done():
			return Sample{}, ctx.Err()
		case <-time.After(delay):
		}
	}
	if err := ctx.Err(); err != nil {
		return Sample{}, err
	}

	n := int(f.calls.Add(1)) - 1
	f.mu.Lock()
	defer f.mu.Unlock()
	var s Sample
	if n < len(f.samples) {
		s = f.samples[n]
	} else if len(f.samples) > 0 {
		s = f.samples[len(f.samples)-1]
	}
	s.DiskRoot = root
	var err error
	if n < len(f.errs) {
		err = f.errs[n]
	} else if len(f.errs) > 0 {
		err = f.errs[len(f.errs)-1]
	}
	_ = idx
	return s, err
}

// manualClock lets tests advance After channels explicitly.
type manualClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []chan time.Time
}

func newManualClock(t time.Time) *manualClock {
	return &manualClock{now: t}
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *manualClock) After(d time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	c.mu.Lock()
	c.waiters = append(c.waiters, ch)
	c.mu.Unlock()
	return ch
}

func (c *manualClock) advance() {
	c.mu.Lock()
	c.now = c.now.Add(time.Second)
	ws := c.waiters
	c.waiters = nil
	c.mu.Unlock()
	for _, ch := range ws {
		ch <- c.now
	}
}

func TestSamplerTicksAndCancel(t *testing.T) {
	col := &fakeCollector{
		samples: []Sample{
			{CPUHostOK: true, CPUHostPct: 10},
			{CPUHostOK: true, CPUHostPct: 20},
			{CPUHostOK: true, CPUHostPct: 30},
		},
	}
	clock := newManualClock(time.Unix(0, 0))
	s := NewSampler(col, time.Second, clock)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch := s.Start(ctx, "/proj")
	r1 := <-ch
	if r1.Err != nil || !r1.Sample.CPUHostOK || r1.Sample.CPUHostPct != 10 {
		t.Fatalf("first = %+v", r1)
	}
	if r1.Sample.DiskRoot != "/proj" {
		t.Errorf("root = %q", r1.Sample.DiskRoot)
	}

	// Advance interval to allow second collect.
	clock.advance()
	r2 := <-ch
	if r2.Sample.CPUHostPct != 20 {
		t.Fatalf("second = %+v", r2)
	}

	s.SetRoot("/other")
	clock.advance()
	r3 := <-ch
	if r3.Sample.DiskRoot != "/other" {
		t.Errorf("root switch = %q", r3.Sample.DiskRoot)
	}

	s.Stop()
	// Channel must close without leak.
	for range ch {
	}
	if col.maxInFlight.Load() > 1 {
		t.Errorf("overlapping collections: max in-flight %d", col.maxInFlight.Load())
	}
}

func TestSamplerSlowCollectNoOverlap(t *testing.T) {
	col := &fakeCollector{
		delay:   30 * time.Millisecond,
		samples: []Sample{{MemOK: true, MemUsedBytes: 1, MemTotalBytes: 2}},
	}
	// Real clock with short interval still must not overlap because wait is after collect.
	s := NewSampler(col, 5*time.Millisecond, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	ch := s.Start(ctx, "/tmp")
	n := 0
	for range ch {
		n++
	}
	if n < 1 {
		t.Fatal("expected at least one sample")
	}
	if col.maxInFlight.Load() > 1 {
		t.Errorf("overlapping collections: %d", col.maxInFlight.Load())
	}
	s.Stop()
}

func TestSamplerCollectError(t *testing.T) {
	sentinel := errors.New("boom")
	col := &fakeCollector{
		samples: []Sample{{}},
		errs:    []error{sentinel},
	}
	clock := newManualClock(time.Unix(0, 0))
	s := NewSampler(col, time.Second, clock)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := s.Start(ctx, "")
	r := <-ch
	if !errors.Is(r.Err, sentinel) {
		t.Fatalf("err = %v", r.Err)
	}
	s.Stop()
}

func TestSamplerReplaceOnRestart(t *testing.T) {
	col := &fakeCollector{samples: []Sample{{CPUHostOK: true, CPUHostPct: 1}}}
	clock := newManualClock(time.Unix(0, 0))
	s := NewSampler(col, time.Second, clock)
	ctx := context.Background()
	ch1 := s.Start(ctx, "/a")
	<-ch1
	ch2 := s.Start(ctx, "/b")
	// ch1 should close
	for range ch1 {
	}
	r := <-ch2
	if r.Sample.DiskRoot != "/b" {
		t.Errorf("root = %q", r.Sample.DiskRoot)
	}
	s.Stop()
	for range ch2 {
	}
}

func TestHostCollectSmoke(t *testing.T) {
	h := NewHost()
	ctx := context.Background()
	// First collect primes CPU counters; second yields CPU percent when available.
	if _, err := h.Collect(ctx, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	s, err := h.Collect(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// On linux/darwin we expect memory and disk at least.
	if !s.MemOK && !s.DiskOK {
		t.Logf("platform returned no mem/disk (may be unsupported): %+v", s)
	}
	if s.MemOK && s.MemTotalBytes == 0 {
		t.Error("MemOK with zero total")
	}
	if s.DiskOK && s.DiskTotalBytes == 0 {
		t.Error("DiskOK with zero total")
	}
}

func TestHostCollectCanceled(t *testing.T) {
	h := NewHost()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := h.Collect(ctx, "/"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}
