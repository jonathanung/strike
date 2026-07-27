package harness

import "context"

// DefaultHarness implements the standard agent loop: stream → execute tools →
// repeat until no more tool calls. This is identical to today's built-in turn
// loop.
type DefaultHarness struct{}

func (d *DefaultHarness) Name() string { return "default" }

func (d *DefaultHarness) Run(ctx context.Context, req Request) (Result, error) {
	for {
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}

		// Deliver child completed notices + threshold compact (engine-side:
		// injected before each Stream by runTurn's callbacks).
		outcome, err := req.Stream(ctx)
		if err != nil {
			return Result{}, err
		}

		// Engine already appended assistant message in req.Stream.
		if len(outcome.Calls) == 0 {
			return Result{StopReason: outcome.StopReason}, nil
		}

		// Execute all calls — even when ctx is canceled — so every call gets
		// a matching tool-result in history (the Execute callback produces
		// unstarted/canceled results for canceled contexts).
		for _, call := range outcome.Calls {
			_ = req.Execute(ctx, call)
		}

		// After any tool execution, check cancellation.
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
	}
}
