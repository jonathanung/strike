package telemetry_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/pkg/telemetry"
)

func TestOTLPExportRedacted(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)

	env, err := telemetry.NewEnvelope("tool", time.Now().UTC(), telemetry.ToolEvent{
		CallID:      "c1",
		Name:        "bash",
		ArgsPreview: "export KEY=sk-ant-api03-" + strings.Repeat("x", 40),
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(env.Payload), "sk-ant-") {
		t.Fatalf("envelope not redacted: %s", env.Payload)
	}

	o := &telemetry.OTLP{Endpoint: srv.URL, ServiceName: "strike-test"}
	if err := o.ExportEnvelopes(context.Background(), []telemetry.Envelope{env}); err != nil {
		t.Fatal(err)
	}
	if len(gotBody) == 0 {
		t.Fatal("empty body")
	}
	var doc map[string]any
	if err := json.Unmarshal(gotBody, &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc["resourceSpans"]; !ok {
		t.Fatalf("missing resourceSpans: %s", gotBody)
	}
	if strings.Contains(string(gotBody), "sk-ant-") {
		t.Fatal("secret in OTLP body")
	}
}

func TestOTLPNilNoop(t *testing.T) {
	var o *telemetry.OTLP
	if err := o.ExportEnvelopes(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	o = &telemetry.OTLP{}
	if err := o.ExportEnvelopes(context.Background(), []telemetry.Envelope{{Family: "x"}}); err != nil {
		t.Fatal(err)
	}
}

func TestOTLPFromEnv(t *testing.T) {
	t.Setenv("STRIKE_OTLP_ENDPOINT", "")
	if telemetry.OTLPFromEnv() != nil {
		t.Fatal("want nil")
	}
	t.Setenv("STRIKE_OTLP_ENDPOINT", "http://localhost:4318/v1/traces")
	t.Setenv("STRIKE_OTLP_HEADERS", "Authorization=Bearer x,X-Foo=bar")
	o := telemetry.OTLPFromEnv()
	if o == nil || o.Endpoint == "" || o.Headers["X-Foo"] != "bar" {
		t.Fatalf("%+v", o)
	}
}
