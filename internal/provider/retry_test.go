package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
	"time"
)

type statusErr struct {
	status    int
	retryable bool
}

func (e statusErr) Error() string   { return fmt.Sprintf("status %d", e.status) }
func (e statusErr) Retryable() bool { return e.retryable }

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

var _ net.Error = timeoutErr{}

func TestIsRetryable(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "canceled", err: context.Canceled, want: false},
		{name: "wrapped canceled", err: fmt.Errorf("wrap: %w", context.Canceled), want: false},
		{name: "incomplete stream", err: ErrIncompleteStream, want: true},
		{name: "eof", err: io.EOF, want: true},
		{name: "unexpected eof", err: io.ErrUnexpectedEOF, want: true},
		{name: "net timeout", err: timeoutErr{}, want: true},
		{name: "retryable status", err: statusErr{status: 429, retryable: true}, want: true},
		{name: "non-retryable status", err: statusErr{status: 400, retryable: false}, want: false},
		{name: "rate limit message", err: errors.New("openai: rate_limit_error: rate limited"), want: true},
		{name: "429 message", err: errors.New("unexpected status 429: slow down"), want: true},
		{name: "500 message", err: errors.New("anthropic: unexpected status 500: oops"), want: true},
		{name: "auth failure", err: errors.New("anthropic: authentication_error: invalid x-api-key"), want: false},
		{name: "invalid request", err: errors.New("openai: invalid_request_error: bad schema"), want: false},
		{name: "plain failure", err: errors.New("sync stream failed"), want: false},
		{name: "context overflow not retryable", err: errors.New("openai: context_length_exceeded: too many tokens"), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsRetryable(tt.err); got != tt.want {
				t.Fatalf("IsRetryable(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestIsContextOverflow(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "context_length_exceeded", err: errors.New("Error code: 400 - context_length_exceeded"), want: true},
		{name: "maximum context length", err: errors.New("this model's maximum context length is 128000 tokens"), want: true},
		{name: "prompt too long", err: errors.New("prompt is too long: 200000 tokens > 128000"), want: true},
		{name: "auth", err: errors.New("authentication_error: invalid key"), want: false},
		{name: "rate limit", err: errors.New("rate limit exceeded"), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsContextOverflow(tt.err); got != tt.want {
				t.Fatalf("IsContextOverflow(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestNormalizeStreamDropsPostTerminalAndInjectsIncomplete(t *testing.T) {
	t.Run("single done", func(t *testing.T) {
		in := make(chan StreamEvent, 3)
		in <- StreamEvent{Type: EventTextDelta, Text: "hi"}
		in <- StreamEvent{Type: EventDone, StopReason: "end_turn"}
		in <- StreamEvent{Type: EventTextDelta, Text: "late"}
		close(in)
		got := collectStream(t, NormalizeStream(in))
		if len(got) != 2 || got[0].Type != EventTextDelta || got[1].Type != EventDone {
			t.Fatalf("events = %#v", got)
		}
	})
	t.Run("error wins once", func(t *testing.T) {
		in := make(chan StreamEvent, 2)
		in <- StreamEvent{Type: EventError, Err: errors.New("boom")}
		in <- StreamEvent{Type: EventDone, StopReason: "end_turn"}
		close(in)
		got := collectStream(t, NormalizeStream(in))
		if len(got) != 1 || got[0].Type != EventError {
			t.Fatalf("events = %#v", got)
		}
	})
	t.Run("incomplete", func(t *testing.T) {
		in := make(chan StreamEvent, 1)
		in <- StreamEvent{Type: EventTextDelta, Text: "partial"}
		close(in)
		got := collectStream(t, NormalizeStream(in))
		if len(got) != 2 || got[1].Type != EventError || !errors.Is(got[1].Err, ErrIncompleteStream) {
			t.Fatalf("events = %#v", got)
		}
	})
}

func collectStream(t *testing.T, ch <-chan StreamEvent) []StreamEvent {
	t.Helper()
	deadline := time.After(2 * time.Second)
	var out []StreamEvent
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, ev)
		case <-deadline:
			t.Fatal("timed out draining stream")
		}
	}
}
