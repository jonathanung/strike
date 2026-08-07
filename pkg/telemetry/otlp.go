package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// OTLP is an optional OpenTelemetry Protocol HTTP/JSON exporter for redacted
// telemetry envelopes. Disabled unless Endpoint is non-empty. No collector is
// required at runtime; this is opt-in only.
//
// Wire format is a minimal OTLP/HTTP JSON subset (resourceSpans) so operators
// can point at any OTLP HTTP receiver without pulling the full OTel SDK.
type OTLP struct {
	// Endpoint is the full URL (e.g. http://localhost:4318/v1/traces).
	Endpoint string
	// Headers are optional extra request headers (never logged).
	Headers map[string]string
	// ServiceName defaults to "strike".
	ServiceName string
	// Timeout defaults to 10s.
	Timeout time.Duration
	// HTTPClient overrides the default client when non-nil.
	HTTPClient *http.Client

	mu     sync.Mutex
	closed bool
}

// OTLPFromEnv builds an exporter from STRIKE_OTLP_ENDPOINT (and optional
// STRIKE_OTLP_HEADERS as comma-separated k=v). Returns nil when unset.
func OTLPFromEnv() *OTLP {
	ep := strings.TrimSpace(os.Getenv("STRIKE_OTLP_ENDPOINT"))
	if ep == "" {
		return nil
	}
	o := &OTLP{Endpoint: ep, ServiceName: "strike"}
	if h := strings.TrimSpace(os.Getenv("STRIKE_OTLP_HEADERS")); h != "" {
		o.Headers = map[string]string{}
		for _, part := range strings.Split(h, ",") {
			k, v, ok := strings.Cut(part, "=")
			k, v = strings.TrimSpace(k), strings.TrimSpace(v)
			if ok && k != "" {
				o.Headers[k] = v
			}
		}
	}
	return o
}

// ExportEnvelopes posts already-redacted envelopes as OTLP spans/events.
// No-op when o is nil or Endpoint empty. Never panics on receiver errors.
func (o *OTLP) ExportEnvelopes(ctx context.Context, envs []Envelope) error {
	if o == nil || strings.TrimSpace(o.Endpoint) == "" || len(envs) == 0 {
		return nil
	}
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		return fmt.Errorf("telemetry: otlp closed")
	}
	o.mu.Unlock()

	body, err := buildOTLPTracesJSON(o.serviceName(), envs)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range o.Headers {
		if strings.TrimSpace(k) != "" {
			req.Header.Set(k, v)
		}
	}
	client := o.HTTPClient
	if client == nil {
		to := o.Timeout
		if to <= 0 {
			to = 10 * time.Second
		}
		client = &http.Client{Timeout: to}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("telemetry: otlp status %d", resp.StatusCode)
	}
	return nil
}

// Close marks the exporter closed (idempotent).
func (o *OTLP) Close() error {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	o.closed = true
	o.mu.Unlock()
	return nil
}

func (o *OTLP) serviceName() string {
	if o != nil && strings.TrimSpace(o.ServiceName) != "" {
		return o.ServiceName
	}
	return "strike"
}

// buildOTLPTracesJSON maps envelopes → minimal OTLP resourceSpans JSON.
func buildOTLPTracesJSON(service string, envs []Envelope) ([]byte, error) {
	spans := make([]map[string]any, 0, len(envs))
	for i, env := range envs {
		// Ensure payload is redacted (callers should pass NewEnvelope output).
		name := env.Family
		if name == "" {
			name = "event"
		}
		attrs := []map[string]any{
			{"key": "strike.family", "value": map[string]any{"stringValue": env.Family}},
			{"key": "strike.schema", "value": map[string]any{"stringValue": env.SchemaVersion}},
		}
		// Attach compact payload as string attribute (already redacted JSON).
		if len(env.Payload) > 0 {
			payload := string(env.Payload)
			if len(payload) > 8192 {
				payload = payload[:8192] + "…[truncated]"
			}
			attrs = append(attrs, map[string]any{
				"key":   "strike.payload",
				"value": map[string]any{"stringValue": payload},
			})
		}
		ts := time.Now().UTC()
		if env.Time != "" {
			if t, err := time.Parse(time.RFC3339Nano, env.Time); err == nil {
				ts = t
			}
		}
		nano := ts.UnixNano()
		spans = append(spans, map[string]any{
			"traceId":           fmt.Sprintf("%032x", i+1), // synthetic; not distributed trace
			"spanId":            fmt.Sprintf("%016x", i+1),
			"name":              name,
			"kind":              1, // INTERNAL
			"startTimeUnixNano": fmt.Sprintf("%d", nano),
			"endTimeUnixNano":   fmt.Sprintf("%d", nano+1),
			"attributes":        attrs,
		})
	}
	doc := map[string]any{
		"resourceSpans": []map[string]any{
			{
				"resource": map[string]any{
					"attributes": []map[string]any{
						{"key": "service.name", "value": map[string]any{"stringValue": service}},
					},
				},
				"scopeSpans": []map[string]any{
					{"spans": spans},
				},
			},
		},
	}
	return json.Marshal(doc)
}
