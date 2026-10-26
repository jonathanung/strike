package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike/internal/trust/usageaudit"
	"github.com/jonathanung/strike/pkg/protocol"
)

func TestUsageAndAuditRoutes(t *testing.T) {
	dir := t.TempDir()
	auditDir := t.TempDir()
	writeFixtureSession(t, dir, "s1",
		protocol.ModelSelected{Correlation: protocol.Correlation{SessionID: "s1"}, Provider: "openai", Model: "priced"},
		protocol.UsageReported{
			Input:     protocol.KnownTokens(1_000_000),
			Output:    protocol.KnownTokens(1_000_000),
			CacheRead: protocol.KnownTokens(1_000_000),
			Source:    protocol.UsageSourceActual,
		},
	)
	// Fixture append uses the current clock, so the event lands on today.
	srv, err := New(Options{
		Auth:          true,
		Token:         "secret",
		SessionDir:    dir,
		UsageAuditDir: auditDir,
		UsagePricer: func(provider, model string) (usageaudit.Rates, bool) {
			if provider == "openai" && model == "priced" {
				return usageaudit.Rates{Input: 3, Output: 6, CacheRead: 1, CacheReadKnown: true}, true
			}
			return usageaudit.Rates{}, false
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/usage", nil)
	srv.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("no auth status = %d", res.Code)
	}

	res = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v1/usage", nil)
	req.Header.Set("Authorization", "Bearer secret")
	srv.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("usage status = %d body=%s", res.Code, res.Body.String())
	}
	var usage usageResponse
	if err := json.Unmarshal(res.Body.Bytes(), &usage); err != nil {
		t.Fatal(err)
	}
	if len(usage.Sessions) != 1 || !usage.Sessions[0].CostKnown {
		t.Fatalf("sessions = %+v", usage.Sessions)
	}
	if usage.Sessions[0].CacheRead != 1_000_000 || usage.Sessions[0].CostUSD != 3+6+1 {
		t.Fatalf("cost = %+v, want input 3 output 6 cache 1", usage.Sessions[0])
	}

	res = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/v1/usage/budget", strings.NewReader(`{"dailyUsd":-1}`))
	req.Header.Set("Authorization", "Bearer secret")
	srv.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("negative budget status = %d body=%s", res.Code, res.Body.String())
	}

	res = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/v1/usage/budget", strings.NewReader(`{"dailyUsd":1}`))
	req.Header.Set("Authorization", "Bearer secret")
	srv.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("budget status = %d body=%s", res.Code, res.Body.String())
	}

	secret := "sk-abcdefghijklmnopqrstuvwxyz"
	res = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/audit", bytes.NewReader([]byte(`{"action":"terminal","sessionId":"s1","summary":"open `+secret+`","detail":{"cmd":"echo `+secret+`"}}`)))
	req.Header.Set("Authorization", "Bearer secret")
	srv.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("append status = %d body=%s", res.Code, res.Body.String())
	}
	if strings.Contains(res.Body.String(), secret) {
		t.Fatalf("response leaked secret: %s", res.Body.String())
	}

	raw, err := os.ReadFile(filepath.Join(auditDir, "chain.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatal("chain file contains the raw secret")
	}

	res = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v1/audit/verify", nil)
	req.Header.Set("Authorization", "Bearer secret")
	srv.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("verify status = %d body=%s", res.Code, res.Body.String())
	}

	tampered := bytes.Replace(raw, []byte("daily usage budget set"), []byte("daily usage budget xyz"), 1)
	if bytes.Equal(tampered, raw) {
		t.Fatal("budget summary missing from chain")
	}
	if err := os.WriteFile(filepath.Join(auditDir, "chain.jsonl"), tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	res = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v1/audit/verify", nil)
	req.Header.Set("Authorization", "Bearer secret")
	srv.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusConflict {
		t.Fatalf("tampered verify status = %d body=%s", res.Code, res.Body.String())
	}

	res = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v1/audit/export", nil)
	req.Header.Set("Authorization", "Bearer secret")
	srv.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"redacted":true`) || !strings.Contains(res.Body.String(), `"verified":false`) {
		t.Fatalf("export = %d %s", res.Code, res.Body.String())
	}
	_ = time.Now()
}

func TestTokenFileRotationIsAuditedWithoutTheToken(t *testing.T) {
	dir := t.TempDir()
	auditDir := t.TempDir()
	tokenFile := filepath.Join(dir, "daemon.token")
	if err := os.WriteFile(tokenFile, []byte("first-token-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv, err := New(Options{
		Auth:          true,
		Token:         "bootstrap",
		TokenFile:     tokenFile,
		SessionDir:    dir,
		UsageAuditDir: auditDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	call := func() int {
		res := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/v1/usage", nil)
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(mustRead(t, tokenFile)))
		srv.Handler().ServeHTTP(res, req)
		return res.Code
	}
	if call() != http.StatusOK {
		t.Fatal("first request failed")
	}
	if err := os.WriteFile(tokenFile, []byte("second-token-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if call() != http.StatusOK {
		t.Fatal("rotated request failed")
	}
	raw, err := os.ReadFile(filepath.Join(auditDir, "chain.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Contains(text, "first-token-value") || strings.Contains(text, "second-token-value") {
		t.Fatalf("token leaked into audit: %s", text)
	}
	if !strings.Contains(text, "daemon token file") {
		t.Fatalf("rotation missing: %s", text)
	}
	if err := srv.usageLog().Verify(); err != nil {
		t.Fatal(err)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
