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

	"github.com/jonathanung/strike-cli/internal/provider"
)

// AuthFunc applies credentials to an outgoing request. Resolved per request
// so OAuth refresh happens transparently.
type AuthFunc func(ctx context.Context, req *http.Request) error

// BearerAuth adapts a bearer-token source (API key or OAuth access token)
// into an AuthFunc.
func BearerAuth(source func(ctx context.Context) (string, error)) AuthFunc {
	return func(ctx context.Context, req *http.Request) error {
		token, err := source(ctx)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
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
		var apiErr apiError
		if json.Unmarshal(data, &apiErr) == nil && apiErr.Error != nil {
			return fmt.Errorf("%s: %s: %s", c.ProviderName, apiErr.Error.Type, apiErr.Error.Message)
		}
		return fmt.Errorf("%s: unexpected status %s: %.200s", c.ProviderName, resp.Status, data)
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
		resp.Body.Close()
		var apiErr apiError
		if json.Unmarshal(data, &apiErr) == nil && apiErr.Error != nil {
			return nil, fmt.Errorf("%s: %s: %s", c.ProviderName, apiErr.Error.Type, apiErr.Error.Message)
		}
		return nil, fmt.Errorf("%s: unexpected status %s: %.300s", c.ProviderName, resp.Status, data)
	}
	return resp.Body, nil
}

// Stream runs fn in a goroutine against a fresh event channel, closing it
// when fn returns — the shared emit pattern for all providers.
func Stream(fn func(ch chan<- provider.StreamEvent)) <-chan provider.StreamEvent {
	ch := make(chan provider.StreamEvent)
	go func() {
		defer close(ch)
		fn(ch)
	}()
	return ch
}

// Fail is a convenience for terminating a stream with an error.
func Fail(ch chan<- provider.StreamEvent, err error) {
	ch <- provider.StreamEvent{Type: provider.EventError, Err: err}
}

// OpenAIEffort spells the normalized reasoning dial the way the OpenAI family
// accepts it — one string, shared by chat-completions (reasoning_effort) and
// the Responses API (reasoning.effort). Its ladder tops out at "high", so the
// two levels above that clamp down rather than erroring. An empty result
// means "omit the field".
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
