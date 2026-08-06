package engine

import (
	"context"
	"crypto/rand"
	"errors"
	"sync"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/provider"
	"github.com/jonathanung/strike-cli/internal/scheduler"
)

// admitModelStream is the sole production admission path for provider model
// streams. When Options.Scheduler is set, it acquires the model pool before
// calling Provider.Stream and holds the lease until the returned channel is
// fully drained (including NormalizeStream's incomplete-stream injection).
//
// Properties:
//   - Canceled waiters never invoke the provider (Acquire honors ctx).
//   - Stream start errors release the lease immediately.
//   - Each attempt acquires independently so retry backoff runs without a lease.
//   - A nil Scheduler is a no-op (unlimited; same as pre-scheduler behavior).
//   - Queue lifecycle emits scheduler.queued/admitted/canceled with corr.
func (e *Engine) admitModelStream(ctx context.Context, corr protocol.Correlation, req provider.Request) (<-chan provider.StreamEvent, error) {
	lease, err := e.acquireScheduler(ctx, corr, "model", scheduler.PoolModel)
	if err != nil {
		return nil, err
	}
	stream, err := e.prov.Stream(ctx, req)
	if err != nil {
		releaseModelLease(lease)
		return nil, err
	}
	// Normalize under the lease so incomplete streams still occupy capacity
	// until the consumer finishes draining.
	normalized := provider.NormalizeStream(stream)
	if lease == nil {
		return normalized, nil
	}
	return holdModelLease(normalized, lease), nil
}

func releaseModelLease(lease *scheduler.Lease) {
	if lease != nil {
		lease.Release()
	}
}

// holdModelLease forwards events and releases lease when in is fully drained.
// Release is deferred so panic paths and early consumer exits that still drain
// (via drainStream) free capacity.
func holdModelLease(in <-chan provider.StreamEvent, lease *scheduler.Lease) <-chan provider.StreamEvent {
	out := make(chan provider.StreamEvent)
	go func() {
		defer close(out)
		defer releaseModelLease(lease)
		for ev := range in {
			out <- ev
		}
	}()
	return out
}

// acquireScheduler acquires named pools and emits durable queue lifecycle
// events correlated to the caller's session/turn. Nil scheduler is a no-op.
//
// Guarantees for one RequestID:
//   - scheduler.queued only when the caller blocks on capacity
//   - exactly one of scheduler.admitted or scheduler.canceled after any queued
//   - admitted is never emitted after canceled for the same RequestID
func (e *Engine) acquireScheduler(ctx context.Context, corr protocol.Correlation, label string, pools ...string) (*scheduler.Lease, error) {
	if e == nil || e.opts.Scheduler == nil {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	reqID := rand.Text()
	var mu sync.Mutex
	// terminal is set once admitted or canceled is emitted so a racy second
	// notify cannot produce admitted after canceled (or double terminal).
	terminal := false
	lease, err := e.opts.Scheduler.AcquireNotify(ctx, func(ev scheduler.AcquireEvent) {
		mu.Lock()
		defer mu.Unlock()
		switch ev.Phase {
		case scheduler.PhaseQueued:
			if terminal {
				return
			}
			e.emit(protocol.SchedulerQueued{
				Correlation: corr,
				RequestID:   reqID,
				Pools:       copyStrings(ev.Pools),
				Label:       label,
			})
		case scheduler.PhaseAdmitted:
			if terminal {
				return
			}
			terminal = true
			e.emit(protocol.SchedulerAdmitted{
				Correlation: corr,
				RequestID:   reqID,
				Pools:       copyStrings(ev.Pools),
				Label:       label,
				WaitMs:      ev.Wait.Milliseconds(),
			})
		case scheduler.PhaseCanceled:
			if terminal {
				return
			}
			terminal = true
			reason := protocol.SchedulerReasonCanceled
			if errors.Is(ev.Err, scheduler.ErrClosed) {
				reason = protocol.SchedulerReasonClosed
			}
			e.emit(protocol.SchedulerCanceled{
				Correlation: corr,
				RequestID:   reqID,
				Pools:       copyStrings(ev.Pools),
				Label:       label,
				WaitMs:      ev.Wait.Milliseconds(),
				Reason:      reason,
			})
		}
	}, pools...)
	return lease, err
}

func copyStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}
