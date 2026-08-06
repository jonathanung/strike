package sdk

import (
	"context"
	"errors"

	"github.com/jonathanung/strike-cli/pkg/protocol"
)

// ErrClosed is returned when the event stream ends before the requested work
// finished (for example RunTurn without a TurnCompleted).
var ErrClosed = errors.New("sdk: session closed")

// Client drives one Strike session through the Op/Event protocol seam.
//
// Construct with [New] when you already hold engine channels (in-process
// embedding via a custom composition root), or with [ConnectJSONL] when
// speaking envelope JSONL over a pipe/socket.
//
// Client methods are safe for concurrent use by multiple goroutines, subject
// to the usual rules for the underlying channels or writers.
type Client struct {
	ops    chan<- protocol.Op
	events <-chan protocol.Event

	// sendOp is used by JSONL-backed clients; when non-nil, Send uses it
	// instead of the ops channel.
	sendOp func(context.Context, protocol.Op) error

	// closer stops background work (JSONL event pump). Optional.
	closer func() error
}

// New returns a Client bound to in-process ops/events channels.
// Both arguments are required.
func New(ops chan<- protocol.Op, events <-chan protocol.Event) *Client {
	if ops == nil {
		panic("sdk: nil ops channel")
	}
	if events == nil {
		panic("sdk: nil events channel")
	}
	return &Client{ops: ops, events: events}
}

// Events is the engine→client event stream. Receive-only; closes when the
// session ends.
func (c *Client) Events() <-chan protocol.Event {
	if c == nil {
		return nil
	}
	return c.events
}

// Send submits one op. It respects ctx cancellation while waiting for channel
// capacity (in-process) or while writing (JSONL).
func (c *Client) Send(ctx context.Context, op protocol.Op) error {
	if c == nil {
		return errors.New("sdk: nil client")
	}
	if op == nil {
		return errors.New("sdk: nil op")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.sendOp != nil {
		return c.sendOp(ctx, op)
	}
	select {
	case c.ops <- op:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close signals background pumps started by [ConnectJSONL] to stop delivering
// into a full Events buffer. It is a no-op for channel-backed clients from
// [New]. Close does not close the caller's ops channel or the JSONL reader;
// close the reader to unblock a pump stuck in Decode, then Close again (or
// drain Events) to observe a terminal decode error if one occurred.
func (c *Client) Close() error {
	if c == nil || c.closer == nil {
		return nil
	}
	return c.closer()
}

// Interrupt is a convenience for protocol.Interrupt{}.
func (c *Client) Interrupt(ctx context.Context) error {
	return c.Send(ctx, protocol.Interrupt{})
}

// Prompt submits protocol.UserInput and is a shorthand for Send.
func (c *Client) Prompt(ctx context.Context, text string, images ...protocol.ImageAttachment) error {
	return c.Send(ctx, protocol.UserInput{Text: text, Images: images})
}
