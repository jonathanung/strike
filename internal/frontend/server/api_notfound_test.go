package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// #1452: unmatched /v1 paths never reach a handler, so the mux used to
// answer with Go's plain-text "404 page not found" instead of the daemon's
// standard JSON error envelope (opErrorResponse) that every in-handler
// error uses. The fallback must cover unknown paths and bare /v1, for any
// method, without changing the shell or known routes.
func TestUnknownV1RouteReturnsJSON404(t *testing.T) {
	srv := testServer(t, t.TempDir(), "")
	cases := []struct {
		method, path string
	}{
		{http.MethodGet, "/v1/nope"},
		{http.MethodGet, "/v1/nope/deeper/still"},
		{http.MethodPost, "/v1/what/ever"},
		{http.MethodDelete, "/v1/unknown-thing"},
		{http.MethodGet, "/v1"},
		{http.MethodPost, "/v1"},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			res := httptest.NewRecorder()
			req := httptest.NewRequest(tc.method, tc.path, nil)
			srv.Handler().ServeHTTP(res, req)
			if res.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404 (body %q)", res.Code, res.Body.String())
			}
			if ct := res.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
				t.Fatalf("Content-Type = %q, want JSON (body %q)", ct, res.Body.String())
			}
			var body opErrorResponse
			if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode %q: %v", res.Body.String(), err)
			}
			want := "route not found: " + tc.method + " " + tc.path
			if body.Error != want {
				t.Fatalf("error = %q, want %q", body.Error, want)
			}
		})
	}
}

// The fallback sits inside withMiddleware, so on an auth gateway an
// unauthenticated probe of an unknown /v1 path gets the same 401 as any
// known route — the 404 must not leak which routes exist to callers
// without a token (#1452 delivery notes).
func TestUnknownV1RouteRespectsAuthMiddleware(t *testing.T) {
	srv := testServer(t, t.TempDir(), "secret")

	res := httptest.NewRecorder()
	srv.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/v1/nope", nil))
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want 401", res.Code)
	}

	res = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/nope", nil)
	req.Header.Set("Authorization", "Bearer secret")
	srv.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusNotFound {
		t.Fatalf("authenticated status = %d, want 404", res.Code)
	}
	var body opErrorResponse
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", res.Body.String(), err)
	}
	if body.Error != "route not found: GET /v1/nope" {
		t.Fatalf("error = %q", body.Error)
	}
}

// Known routes, in-handler JSON errors, and the attach shell must be
// unchanged by the /v1 fallback, and unknown non-API paths must keep the
// shell world's plain 404 (Q3: JSON covers /v1 only).
func TestAPIFallbackLeavesKnownRoutesAndShellAlone(t *testing.T) {
	srv := testServer(t, t.TempDir(), "")

	// A registered route still answers.
	res := httptest.NewRecorder()
	srv.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/v1/bootstrap", nil))
	if res.Code != http.StatusOK {
		t.Fatalf("GET /v1/bootstrap = %d, want 200", res.Code)
	}

	// An in-handler JSON error is unchanged: no catalog service is wired
	// on this server, so /v1/models reports the JSON envelope itself.
	res = httptest.NewRecorder()
	srv.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if res.Code != http.StatusNotImplemented {
		t.Fatalf("GET /v1/models = %d, want 501", res.Code)
	}
	var body opErrorResponse
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("in-handler error not JSON: %q", res.Body.String())
	}
	if body.Error != "catalog capability unavailable on this host" {
		t.Fatalf("in-handler error = %q", body.Error)
	}

	// The shell still serves HTML at / and /attach.
	for _, path := range []string{"/", "/attach"} {
		res = httptest.NewRecorder()
		srv.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, path, nil))
		if res.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", path, res.Code)
		}
		if !strings.Contains(res.Body.String(), "<title>Strike</title>") {
			t.Fatalf("GET %s did not serve the shell", path)
		}
	}

	// Unknown non-API paths keep Go's plain-text 404, not the envelope.
	res = httptest.NewRecorder()
	srv.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/not-an-api-route", nil))
	if res.Code != http.StatusNotFound {
		t.Fatalf("GET /not-an-api-route = %d, want 404", res.Code)
	}
	if strings.Contains(res.Body.String(), "route not found") {
		t.Fatalf("non-API 404 leaked the API envelope: %q", res.Body.String())
	}
}

// Method mismatch on a known path: without special handling the
// method-less fallback would swallow the mux's 405 into a misleading
// "route not found" 404. The fallback probes the mux for the path under
// other methods and answers a JSON 405 with an Allow header instead
// (issue #1452 Q2, achieved with the stock ServeMux).
func TestKnownRouteWrongMethodReturnsJSON405(t *testing.T) {
	srv := testServer(t, t.TempDir(), "")
	cases := []struct {
		method, path, allow string
	}{
		// /v1/bootstrap is registered for GET only.
		{http.MethodPost, "/v1/bootstrap", http.MethodGet},
		// A wildcard route: /v1/sessions/{id}/events is GET only.
		{http.MethodDelete, "/v1/sessions/abc/events", http.MethodGet},
		// /v1/ops is registered for POST only.
		{http.MethodGet, "/v1/ops", http.MethodPost},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			res := httptest.NewRecorder()
			srv.Handler().ServeHTTP(res, httptest.NewRequest(tc.method, tc.path, nil))
			if res.Code != http.StatusMethodNotAllowed {
				t.Fatalf("status = %d, want 405 (body %q)", res.Code, res.Body.String())
			}
			if ct := res.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
				t.Fatalf("Content-Type = %q, want JSON", ct)
			}
			var body opErrorResponse
			if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode %q: %v", res.Body.String(), err)
			}
			want := "method not allowed: " + tc.method + " " + tc.path
			if body.Error != want {
				t.Fatalf("error = %q, want %q", body.Error, want)
			}
			if allow := res.Header().Get("Allow"); !strings.Contains(allow, tc.allow) {
				t.Fatalf("Allow = %q, want it to name %s", allow, tc.allow)
			}
		})
	}
}
