package provider

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
)

// ErrIncompleteStream is returned when a provider channel closes without a
// terminal EventDone or EventError. It is retryable at the model boundary.
var ErrIncompleteStream = errors.New("provider stream closed without terminal event")

// retryMarker is implemented by errors that know whether a fresh Stream call
// is safe (no partial commit). Used by HTTP status errors from base.
type retryMarker interface {
	Retryable() bool
}

// IsRetryable reports whether err is a transient provider/transport failure
// that may be retried with a new attempt identity before any tool side
// effects from the failed attempt. Context cancellation is never retryable.
// Permanent client errors (4xx other than 408/429) are not retryable.
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, ErrIncompleteStream) {
		return true
	}
	var marker retryMarker
	if errors.As(err, &marker) {
		return marker.Retryable()
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, needle := range retrySubstrings {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

// retrySubstrings match common transport / overload wording when adapters
// still return plain errors. Keep this conservative: auth and validation
// failures must not match.
var retrySubstrings = []string{
	"429",
	"rate limit",
	"overloaded",
	"timeout",
	"timed out",
	"temporarily unavailable",
	"connection reset",
	"connection refused",
	"broken pipe",
	"i/o timeout",
	"tls handshake",
	"unexpected eof",
	"socket hang up",
	"server error",
	"bad gateway",
	"service unavailable",
	"gateway timeout",
	"status 500",
	"status 502",
	"status 503",
	"status 504",
	"unexpected status 500",
	"unexpected status 502",
	"unexpected status 503",
	"unexpected status 504",
	"unexpected status 429",
}
