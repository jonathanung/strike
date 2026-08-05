package engine

import (
	"context"

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
func (e *Engine) admitModelStream(ctx context.Context, req provider.Request) (<-chan provider.StreamEvent, error) {
	lease, err := e.acquireModelLease(ctx)
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

func (e *Engine) acquireModelLease(ctx context.Context) (*scheduler.Lease, error) {
	if e == nil || e.opts.Scheduler == nil {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return e.opts.Scheduler.Acquire(ctx, scheduler.PoolModel)
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
