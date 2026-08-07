package base

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/provider"
)

func TestBearerAuth(t *testing.T) {
	auth := BearerAuth(func(context.Context) (string, error) { return "tok", nil })
	req, _ := http.NewRequest(http.MethodGet, "http://example", nil)
	if err := auth(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer tok" {
		t.Errorf("Authorization = %q", got)
	}
}

func TestPostJSONSuccessAndErrors(t *testing.T) {
	var sawAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		if r.Header.Get("X-Custom") != "1" {
			t.Errorf("missing custom header")
		}
		switch r.URL.Path {
		case "/ok":
			_ = json.NewEncoder(w).Encode(map[string]string{"hello": "world"})
		case "/apierr":
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]string{"type": "invalid_request", "message": "bad"},
			})
		default:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("oops"))
		}
	}))
	defer srv.Close()

	c := &Client{
		ProviderName: "test",
		HTTP:         srv.Client(),
		Auth:         BearerAuth(func(context.Context) (string, error) { return "abc", nil }),
		Headers:      map[string]string{"X-Custom": "1"},
	}

	var out struct {
		Hello string `json:"hello"`
	}
	if err := c.PostJSON(context.Background(), srv.URL+"/ok", map[string]string{"a": "b"}, &out); err != nil {
		t.Fatal(err)
	}
	if out.Hello != "world" || sawAuth != "Bearer abc" {
		t.Errorf("out=%#v auth=%q", out, sawAuth)
	}

	err := c.PostJSON(context.Background(), srv.URL+"/apierr", map[string]string{}, &out)
	if err == nil || !strings.Contains(err.Error(), "invalid_request") || !strings.Contains(err.Error(), "bad") {
		t.Fatalf("api err = %v", err)
	}
	var apiStatus *StatusError
	if !errors.As(err, &apiStatus) || apiStatus.Status != http.StatusBadRequest || apiStatus.Retryable() {
		t.Fatalf("api StatusError = %#v", err)
	}

	err = c.PostJSON(context.Background(), srv.URL+"/plain", map[string]string{}, &out)
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("status err = %v", err)
	}
	var plainStatus *StatusError
	if !errors.As(err, &plainStatus) || plainStatus.Status != http.StatusInternalServerError || !plainStatus.Retryable() {
		t.Fatalf("plain StatusError = %#v", err)
	}
}

func TestPostSSE(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("Accept = %q", r.Header.Get("Accept"))
		}
		if r.URL.Path == "/err" {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]string{"type": "auth", "message": "nope"},
			})
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: hi\n\n")
	}))
	defer srv.Close()

	c := &Client{ProviderName: "sse", HTTP: srv.Client()}
	body, err := c.PostSSE(context.Background(), srv.URL+"/ok", map[string]string{"x": "1"})
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	data, _ := io.ReadAll(body)
	if !strings.Contains(string(data), "hi") {
		t.Errorf("body = %q", data)
	}

	_, err = c.PostSSE(context.Background(), srv.URL+"/err", map[string]string{})
	if err == nil || !strings.Contains(err.Error(), "auth") {
		t.Fatalf("err = %v", err)
	}
}

func TestStreamAndFail(t *testing.T) {
	ch := Stream(func(ch chan<- provider.StreamEvent) {
		Fail(ch, io.EOF)
	})
	ev, ok := <-ch
	if !ok || ev.Type != provider.EventError || ev.Err != io.EOF {
		t.Fatalf("event = %#v ok=%v", ev, ok)
	}
	if _, ok := <-ch; ok {
		t.Fatal("channel should be closed")
	}
}

func TestStreamNormalizesMissingTerminal(t *testing.T) {
	ch := Stream(func(ch chan<- provider.StreamEvent) {
		ch <- provider.StreamEvent{Type: provider.EventTextDelta, Text: "x"}
	})
	var got []provider.StreamEvent
	for ev := range ch {
		got = append(got, ev)
	}
	if len(got) != 2 || got[1].Type != provider.EventError || !errors.Is(got[1].Err, provider.ErrIncompleteStream) {
		t.Fatalf("events = %#v", got)
	}
}

func TestClientName(t *testing.T) {
	c := &Client{ProviderName: "xai"}
	if c.Name() != "xai" {
		t.Errorf("Name = %q", c.Name())
	}
}

func TestParseRetryAfter(t *testing.T) {
	t.Parallel()
	future := time.Now().UTC().Add(12 * time.Second).Format(http.TimeFormat)
	past := time.Now().UTC().Add(-5 * time.Second).Format(http.TimeFormat)
	far := time.Now().UTC().Add(2 * time.Hour).Format(http.TimeFormat)

	cases := []struct {
		name string
		raw  string
		want time.Duration // 0 = invalid/missing/excessive
		min  time.Duration // for HTTP-date, allow slack
		max  time.Duration
	}{
		{name: "empty", raw: "", want: 0},
		{name: "spaces", raw: "  ", want: 0},
		{name: "delay seconds", raw: "7", want: 7 * time.Second},
		{name: "delay zero", raw: "0", want: 0},
		{name: "delay negative", raw: "-3", want: 0},
		{name: "delay excessive", raw: "120", want: 0}, // > MaxRetryAfter
		{name: "delay at cap", raw: "60", want: 60 * time.Second},
		{name: "garbage", raw: "soon", want: 0},
		{name: "http-date future", raw: future, min: 5 * time.Second, max: 12 * time.Second},
		{name: "http-date past", raw: past, want: 0},
		{name: "http-date excessive", raw: far, want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseRetryAfter(tc.raw)
			if tc.min > 0 || tc.max > 0 {
				if got < tc.min || got > tc.max {
					t.Fatalf("ParseRetryAfter(%q) = %v, want in [%v, %v]", tc.raw, got, tc.min, tc.max)
				}
				return
			}
			if got != tc.want {
				t.Fatalf("ParseRetryAfter(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestPostJSONPreservesRetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "5")
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]string{"type": "rate_limit_error", "message": "slow down"},
		})
	}))
	defer srv.Close()

	c := &Client{ProviderName: "test", HTTP: srv.Client()}
	err := c.PostJSON(context.Background(), srv.URL, map[string]string{}, &struct{}{})
	var se *StatusError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v, want StatusError", err)
	}
	if se.Status != http.StatusTooManyRequests || !se.Retryable() {
		t.Fatalf("StatusError = %#v", se)
	}
	if se.RetryAfter != 5*time.Second {
		t.Fatalf("RetryAfter = %v, want 5s", se.RetryAfter)
	}
	d, ok := se.RetryAfterDelay()
	if !ok || d != 5*time.Second {
		t.Fatalf("RetryAfterDelay = %v, %v", d, ok)
	}
}

func TestPostSSEPreservesRetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "not-a-delay")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("nope"))
	}))
	defer srv.Close()

	c := &Client{ProviderName: "sse", HTTP: srv.Client()}
	_, err := c.PostSSE(context.Background(), srv.URL, map[string]string{})
	var se *StatusError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v", err)
	}
	if se.RetryAfter != 0 {
		t.Fatalf("invalid Retry-After should be zero, got %v", se.RetryAfter)
	}
	if _, ok := se.RetryAfterDelay(); ok {
		t.Fatal("RetryAfterDelay ok=true for invalid header")
	}
}

func TestPostJSONMissingRetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("busy"))
	}))
	defer srv.Close()

	c := &Client{ProviderName: "test", HTTP: srv.Client()}
	err := c.PostJSON(context.Background(), srv.URL, map[string]string{}, &struct{}{})
	var se *StatusError
	if !errors.As(err, &se) || se.RetryAfter != 0 {
		t.Fatalf("StatusError = %#v", err)
	}
}

func TestPostJSONExcessiveRetryAfterDropped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("wait forever"))
	}))
	defer srv.Close()

	c := &Client{ProviderName: "test", HTTP: srv.Client()}
	err := c.PostJSON(context.Background(), srv.URL, map[string]string{}, &struct{}{})
	var se *StatusError
	if !errors.As(err, &se) || se.RetryAfter != 0 {
		t.Fatalf("excessive Retry-After must be dropped: %#v", se)
	}
}
