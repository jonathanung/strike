// Package base is the shared foundation providers embed (Go's analogue of
// a provider base class): one place for HTTP transport, auth application,
// JSON/SSE posting, and error shaping. Concrete providers embed Client and
// implement only their wire-format mapping.
package base

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jonathanung/strike-cli/internal/provider"
)

// AuthFunc applies credentials to an outgoing request. Resolved per request
// so OAuth refresh happens transparently.
type AuthFunc func(ctx context.Context, req *http.Request) error

// BearerAuth adapts a bearer-token source (API key or OAuth access token)
// into an AuthFunc. An empty token leaves Authorization unset (local gateways).
func BearerAuth(source func(ctx context.Context) (string, error)) AuthFunc {
	return func(ctx context.Context, req *http.Request) error {
		token, err := source(ctx)
		if err != nil {
			return err
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		return nil
	}
}

// Client is the embeddable provider core.
type Client struct {
	ProviderName string
	HTTP         *http.Client
	// Auth applies per-request credentials; nil when Headers carry them.
	Auth AuthFunc
	// Headers are static headers added to every request (API version
	// pins, key headers, backend gates).
	Headers map[string]string
}

func (c *Client) Name() string { return c.ProviderName }

// apiError is the {"error": {...}} envelope OpenAI-style and Anthropic
// APIs share for failures.
type apiError struct {
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

func (c *Client) newRequest(ctx context.Context, url string, body []byte) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range c.Headers {
		req.Header.Set(k, v)
	}
	if c.Auth != nil {
		if err := c.Auth(ctx, req); err != nil {
			return nil, err
		}
	}
	return req, nil
}

// PostJSON marshals in, POSTs it, and unmarshals a 200 response into out.
// Failures come back as one uniformly shaped error: provider name, API
// error type/message when parseable, status + body snippet otherwise.
func (c *Client) PostJSON(ctx context.Context, url string, in any, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := c.newRequest(ctx, url, body)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return statusError(c.ProviderName, resp.StatusCode, data, resp.Header)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("%s: bad response (%s): %.200s", c.ProviderName, resp.Status, data)
	}
	return nil
}

// PostSSE POSTs in and returns the response body for SSE consumption.
// The caller owns closing the returned reader.
func (c *Client) PostSSE(ctx context.Context, url string, in any) (io.ReadCloser, error) {
	body, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	req, err := c.newRequest(ctx, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		hdr := resp.Header.Clone()
		resp.Body.Close()
		return nil, statusError(c.ProviderName, resp.StatusCode, data, hdr)
	}
	return resp.Body, nil
}

// Stream runs fn in a goroutine against a fresh event channel and normalizes
// the result to the provider terminal contract (exactly one Done/Error).
func Stream(fn func(ch chan<- provider.StreamEvent)) <-chan provider.StreamEvent {
	raw := make(chan provider.StreamEvent)
	go func() {
		defer close(raw)
		fn(raw)
	}()
	return provider.NormalizeStream(raw)
}

// Fail is a convenience for terminating a stream with an error.
func Fail(ch chan<- provider.StreamEvent, err error) {
	ch <- provider.StreamEvent{Type: provider.EventError, Err: err}
}

// MaxRetryAfter is the upper bound for honoring a provider Retry-After value.
// Larger or invalid guidance falls back to the engine's local backoff policy.
const MaxRetryAfter = 60 * time.Second

// StatusError is a non-OK HTTP response from a provider API.
type StatusError struct {
	Provider string
	Status   int
	Type     string // API error type when the body parsed
	Message  string
	Body     string // body snippet when unparsed
	// RetryAfter is a parsed Retry-After delay when the response carried a
	// valid delay-seconds or HTTP-date value within MaxRetryAfter. Zero means
	// missing, invalid, or excessive — callers should use local backoff.
	RetryAfter time.Duration
}

func (e *StatusError) Error() string {
	if e.Type != "" {
		return fmt.Sprintf("%s: %s: %s", e.Provider, e.Type, e.Message)
	}
	if e.Body != "" {
		return fmt.Sprintf("%s: unexpected status %d: %s", e.Provider, e.Status, e.Body)
	}
	return fmt.Sprintf("%s: unexpected status %d", e.Provider, e.Status)
}

// Retryable reports transient overload/upstream failures safe to re-attempt.
func (e *StatusError) Retryable() bool {
	return e.Status == 408 || e.Status == 429 || e.Status >= 500
}

// RetryAfterDelay implements provider.retryAfterCarrier so the engine can
// prefer server retry guidance over local backoff.
func (e *StatusError) RetryAfterDelay() (time.Duration, bool) {
	if e == nil || e.RetryAfter <= 0 {
		return 0, false
	}
	return e.RetryAfter, true
}

func statusError(provider string, status int, data []byte, hdr http.Header) error {
	retryAfter := parseRetryAfterHeader(hdr)
	var apiErr apiError
	if json.Unmarshal(data, &apiErr) == nil && apiErr.Error != nil {
		return &StatusError{
			Provider:   provider,
			Status:     status,
			Type:       apiErr.Error.Type,
			Message:    apiErr.Error.Message,
			RetryAfter: retryAfter,
		}
	}
	body := string(data)
	if len(body) > 200 {
		body = body[:200]
	}
	return &StatusError{Provider: provider, Status: status, Body: body, RetryAfter: retryAfter}
}

// ParseRetryAfter parses a Retry-After header value (delay-seconds or HTTP-date).
// Returns 0 when the value is missing, unparsable, non-positive, or exceeds
// MaxRetryAfter (callers then use local backoff).
func ParseRetryAfter(raw string) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	// delay-seconds (RFC 9110): non-negative integer
	if sec, err := strconv.ParseInt(raw, 10, 64); err == nil {
		if sec <= 0 {
			return 0
		}
		d := time.Duration(sec) * time.Second
		if d > MaxRetryAfter {
			return 0
		}
		return d
	}
	// HTTP-date forms
	for _, layout := range []string{
		http.TimeFormat, // RFC1123
		time.RFC1123Z,   // RFC1123 with numeric zone
		time.RFC850,     // obsolete
		time.ANSIC,      // obsolete
	} {
		if t, err := time.Parse(layout, raw); err == nil {
			d := time.Until(t)
			if d <= 0 || d > MaxRetryAfter {
				return 0
			}
			return d
		}
	}
	return 0
}

func parseRetryAfterHeader(hdr http.Header) time.Duration {
	if hdr == nil {
		return 0
	}
	return ParseRetryAfter(hdr.Get("Retry-After"))
}

// OpenAIEffort spells the normalized reasoning dial the way the OpenAI family
// accepts it — one string, shared by chat-completions (reasoning_effort) and
// the Responses API (reasoning.effort). The vendor ladder is shorter at both
// ends, so this mapping is deliberately lossy: it tops out at "high", so
// EffortXHigh and EffortMax clamp down, and it has no zero setting, so
// EffortOff floors at "minimal" rather than truly disabling reasoning the way
// the Anthropic adapter can. An empty result means "omit the field".
func OpenAIEffort(effort provider.Effort) string {
	switch effort {
	case provider.EffortOff:
		return "minimal"
	case provider.EffortLow:
		return "low"
	case provider.EffortMedium:
		return "medium"
	case provider.EffortHigh, provider.EffortXHigh, provider.EffortMax:
		return "high"
	default:
		return ""
	}
}
