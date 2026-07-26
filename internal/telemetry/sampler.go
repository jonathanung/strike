package telemetry

import (
	"context"
	"sync"
	"time"
)

// Clock abstracts time for deterministic sampler tests.
type Clock interface {
	Now() time.Time
	// After returns a channel that delivers once after d (like time.After).
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Result is one sampler emission.
type Result struct {
	Sample Sample
	Err    error
}

// Sampler runs Collect on an interval without overlapping collections.
// Cancel the Start context (or call Stop) to end the loop; the results
// channel is closed on exit.
type Sampler struct {
	collector Collector
	interval  time.Duration
	clock     Clock

	mu     sync.Mutex
	root   string
	cancel context.CancelFunc
	done   chan struct{}
}

// NewSampler builds a sampler. interval ≤ 0 uses DefaultInterval. A nil clock
// uses the real wall clock. collector must be non-nil.
func NewSampler(collector Collector, interval time.Duration, clock Clock) *Sampler {
	if interval <= 0 {
		interval = DefaultInterval
	}
	if clock == nil {
		clock = realClock{}
	}
	return &Sampler{
		collector: collector,
		interval:  interval,
		clock:     clock,
	}
}

// SetRoot updates the filesystem root used for disk sampling. Safe concurrent
// with a running loop; the next Collect picks up the new root.
func (s *Sampler) SetRoot(root string) {
	s.mu.Lock()
	s.root = root
	s.mu.Unlock()
}

// Root returns the current disk root.
func (s *Sampler) Root() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.root
}

// Start begins sampling in a background goroutine. The returned channel yields
// results until ctx is cancelled or Stop is called. Calling Start while already
// running replaces the previous loop (prior channel is closed).
func (s *Sampler) Start(ctx context.Context, root string) <-chan Result {
	s.Stop()

	s.mu.Lock()
	s.root = root
	runCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	done := make(chan struct{})
	s.done = done
	s.mu.Unlock()

	out := make(chan Result, 1)
	go func() {
		defer close(out)
		defer close(done)
		defer cancel()

		// Immediate first sample, then wait interval after each completed collect
		// so slow Collect never overlaps the next.
		for {
			if err := runCtx.Err(); err != nil {
				return
			}
			s.mu.Lock()
			r := s.root
			col := s.collector
			interval := s.interval
			clock := s.clock
			s.mu.Unlock()

			sample, err := col.Collect(runCtx, r)
			if sample.At.IsZero() {
				sample.At = clock.Now()
			}
			select {
			case <-runCtx.Done():
				return
			case out <- Result{Sample: sample, Err: err}:
			}

			select {
			case <-runCtx.Done():
				return
			case <-clock.After(interval):
			}
		}
	}()
	return out
}

// Stop cancels a running sampler and waits for the goroutine to exit.
// Idempotent.
func (s *Sampler) Stop() {
	s.mu.Lock()
	cancel := s.cancel
	done := s.done
	s.cancel = nil
	s.done = nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}
