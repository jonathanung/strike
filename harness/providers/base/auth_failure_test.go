package base

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jonathanung/strike/harness/provider"
)

// TestStatusErrorAuthFailureVendorShapes drives real HTTP responses through
// PostSSE so classification is checked on the error adapters actually
// return (#1546). Conservative: only 401, or 403 that explicitly says
// authentication, is a credential failure.
func TestStatusErrorAuthFailureVendorShapes(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"anthropic 401 authentication_error", 401, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`, true},
		{"openai 401 invalid api key", 401, `{"error":{"type":"invalid_request_error","code":"invalid_api_key","message":"Incorrect API key provided"}}`, true},
		{"google 401 unauthenticated", 401, `{"error":{"code":401,"status":"UNAUTHENTICATED","message":"Request had invalid authentication credentials."}}`, true},
		{"unparsed 401 body", 401, `unauthorized`, true},
		{"403 authentication_error", 403, `{"error":{"type":"authentication_error","message":"OAuth token has been revoked"}}`, true},
		{"anthropic 403 permission_error is not auth", 403, `{"error":{"type":"permission_error","message":"Your API key does not have permission to use the specified resource."}}`, false},
		{"openai 403 unsupported region is not auth", 403, `{"error":{"type":"invalid_request_error","code":"unsupported_country_region_territory","message":"Country not supported"}}`, false},
		{"bare 403 is not auth", 403, `forbidden`, false},
		{"429 quota is not auth", 429, `{"error":{"type":"insufficient_quota","message":"You exceeded your current quota"}}`, false},
		{"500 is not auth", 500, `{"error":{"type":"api_error","message":"Internal server error"}}`, false},
		{"400 invalid request is not auth", 400, `{"error":{"type":"invalid_request_error","message":"bad schema"}}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			}))
			defer srv.Close()
			c := &Client{ProviderName: "vendor", HTTP: srv.Client()}
			_, err := c.PostSSE(context.Background(), srv.URL, map[string]string{})
			if err == nil {
				t.Fatal("want error")
			}
			name, ok := provider.AuthFailure(err)
			if ok != tt.want {
				t.Fatalf("AuthFailure(%v) ok = %v, want %v", err, ok, tt.want)
			}
			if ok && name != "vendor" {
				t.Fatalf("provider name = %q, want vendor", name)
			}
		})
	}
}

func TestStatusErrorAuthFailureNil(t *testing.T) {
	var e *StatusError
	if _, ok := e.AuthFailure(); ok {
		t.Fatal("nil StatusError must not classify")
	}
}
