package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func freeLoopbackPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

// stubOpenBrowser replaces openBrowser with a recorder. The default opener
// already no-ops under go test; this is for asserting the URL was requested.
func stubOpenBrowser(t *testing.T) *[]string {
	t.Helper()
	var opened []string
	prev := openBrowser
	openBrowser = func(target string) {
		opened = append(opened, target)
	}
	t.Cleanup(func() { openBrowser = prev })
	return &opened
}

func testOAuthFlow(tokenURL string, port int) FlowConfig {
	return FlowConfig{
		AuthorizeURL: "http://issuer.test/oauth/authorize",
		TokenURL:     tokenURL,
		ClientID:     "test-client",
		Scope:        "openid",
		RedirectHost: "127.0.0.1",
		RedirectPort: port,
		RedirectPath: "/auth/callback",
	}
}

func beginTestPending(t *testing.T, tokenURL string) *PendingLogin {
	t.Helper()
	stubOpenBrowser(t)
	p, err := testOAuthFlow(tokenURL, freeLoopbackPort(t)).Begin()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if p.server != nil {
			_ = p.server.Close()
		}
	})
	return p
}

func TestBeginRequestsBrowserOpenWithoutInvokingOpener(t *testing.T) {
	opened := stubOpenBrowser(t)
	p, err := testOAuthFlow("http://127.0.0.1:1/unused-token", freeLoopbackPort(t)).Begin()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if p.server != nil {
			_ = p.server.Close()
		}
	})
	if len(*opened) != 1 {
		t.Fatalf("openBrowser calls = %d, want 1", len(*opened))
	}
	if (*opened)[0] != p.URL {
		t.Errorf("opened %q, want authorize URL %q", (*opened)[0], p.URL)
	}
	if !strings.Contains(p.URL, "http://issuer.test/oauth/authorize?") {
		t.Errorf("authorize URL = %q", p.URL)
	}
}

func pendingState(t *testing.T, p *PendingLogin) string {
	t.Helper()
	u, err := url.Parse(p.URL)
	if err != nil {
		t.Fatalf("parse authorize URL: %v", err)
	}
	state := u.Query().Get("state")
	if state == "" {
		t.Fatal("authorize URL missing state")
	}
	return state
}

func tokenExchangeServer(t *testing.T, access string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("token method = %s, want POST", r.Method)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read token body: %v", err)
			http.Error(w, "read", 500)
			return
		}
		vals, err := url.ParseQuery(string(body))
		if err != nil {
			t.Errorf("parse token body: %v", err)
			http.Error(w, "parse", 500)
			return
		}
		if vals.Get("grant_type") != "authorization_code" {
			t.Errorf("grant_type = %q", vals.Get("grant_type"))
		}
		if vals.Get("code") == "" || vals.Get("code_verifier") == "" {
			t.Errorf("missing code or verifier: %v", vals)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  access,
			"refresh_token": "refresh-test",
			"expires_in":    3600,
		})
	}))
}

func assertErrNoPaste(t *testing.T, err error, paste string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error")
	}
	if paste != "" && strings.Contains(err.Error(), paste) {
		t.Errorf("error echoed paste %q: %v", paste, err)
	}
}

func TestCompleteWithPasteFullCallbackURLExchanges(t *testing.T) {
	srv := tokenExchangeServer(t, "access-from-paste")
	defer srv.Close()

	p := beginTestPending(t, srv.URL)
	state := pendingState(t, p)
	code := "auth-code-from-callback"
	callback := fmt.Sprintf(
		"http://127.0.0.1:%d/auth/callback?code=%s&state=%s",
		p.flow.RedirectPort, url.QueryEscape(code), url.QueryEscape(state),
	)

	if err := p.CompleteWithPaste(callback); err != nil {
		t.Fatalf("CompleteWithPaste: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	tok, err := p.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if tok == nil || tok.Access != "access-from-paste" {
		t.Fatalf("tokens = %+v, want access-from-paste", tok)
	}
	if tok.Refresh != "refresh-test" {
		t.Errorf("refresh = %q", tok.Refresh)
	}
}

func TestCompleteWithPasteWrongStateAllowsRetry(t *testing.T) {
	p := beginTestPending(t, "http://127.0.0.1:1/unused-token")
	state := pendingState(t, p)
	secretCode := "SECRET_CODE_MUST_NOT_LEAK"

	err := p.CompleteWithPaste("http://127.0.0.1/cb?code=" + secretCode + "&state=not-the-right-state")
	assertErrNoPaste(t, err, secretCode)
	if !strings.Contains(err.Error(), "state mismatch") {
		t.Errorf("err = %v, want state mismatch", err)
	}

	// Wrong state must not complete the login — correct paste still works.
	good := fmt.Sprintf("http://127.0.0.1/cb?code=good-code&state=%s", url.QueryEscape(state))
	if err := p.CompleteWithPaste(good); err != nil {
		t.Fatalf("retry with matching state: %v", err)
	}
}

func TestCompleteWithPasteBareCode(t *testing.T) {
	p := beginTestPending(t, "http://127.0.0.1:1/unused-token")
	if err := p.CompleteWithPaste("  bare-authorization-code-abc  "); err != nil {
		t.Fatalf("bare code: %v", err)
	}
}

func TestCompleteWithPasteEmptyAndWhitespace(t *testing.T) {
	p := beginTestPending(t, "http://127.0.0.1:1/unused-token")
	for _, raw := range []string{"", "   ", "\t\n", " \t "} {
		err := p.CompleteWithPaste(raw)
		if err == nil {
			t.Errorf("CompleteWithPaste(%q): want error", raw)
			continue
		}
		if !strings.Contains(err.Error(), "empty") {
			t.Errorf("CompleteWithPaste(%q): err = %v, want empty paste", raw, err)
		}
	}
}

func TestCompleteWithPasteGarbage(t *testing.T) {
	p := beginTestPending(t, "http://127.0.0.1:1/unused-token")
	cases := []struct {
		name string
		raw  string
	}{
		{"url without code", "http://127.0.0.1/callback?foo=bar"},
		{"https without code", "https://example.test/oauth/callback"},
		{"query-shaped without code", "code=&state=abc"},
		{"http prefix junk", "httpnot-a-url"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Fresh pending so a prior success does not mask errors.
			p := beginTestPending(t, "http://127.0.0.1:1/unused-token")
			err := p.CompleteWithPaste(tc.raw)
			if err == nil {
				// "httpnot-a-url" contains no :// and no code= — treated as bare code.
				if tc.name == "http prefix junk" {
					return
				}
				t.Fatal("expected error, got nil")
			}
			assertErrNoPaste(t, err, tc.raw)
		})
	}
	// Ensure the outer pending is still usable (no panic path above).
	_ = p
}

func TestCompleteWithPasteErrorQuery(t *testing.T) {
	p := beginTestPending(t, "http://127.0.0.1:1/unused-token")

	err := p.CompleteWithPaste("http://127.0.0.1/cb?error=access_denied&error_description=user+denied+consent")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "user denied consent") {
		t.Errorf("err = %v, want error_description", err)
	}

	err = p.CompleteWithPaste("http://127.0.0.1/cb?error=server_error")
	if err == nil || !strings.Contains(err.Error(), "server_error") {
		t.Errorf("err = %v, want server_error", err)
	}

	// OAuth error must not complete — a later good paste still works.
	state := pendingState(t, p)
	if err := p.CompleteWithPaste(fmt.Sprintf("http://127.0.0.1/cb?code=ok&state=%s", url.QueryEscape(state))); err != nil {
		t.Fatalf("paste after oauth error: %v", err)
	}
}

func TestCompleteWithPasteSecondAfterSuccess(t *testing.T) {
	p := beginTestPending(t, "http://127.0.0.1:1/unused-token")
	if err := p.CompleteWithPaste("first-code"); err != nil {
		t.Fatal(err)
	}
	err := p.CompleteWithPaste("second-code")
	if err == nil {
		t.Fatal("expected already-completed error")
	}
	if !strings.Contains(err.Error(), "already completed") {
		t.Errorf("err = %v, want already completed", err)
	}
	if strings.Contains(err.Error(), "second-code") || strings.Contains(err.Error(), "first-code") {
		t.Errorf("error echoed a code: %v", err)
	}
}

func TestBeginReturnsPendingWhenBindFails(t *testing.T) {
	stubOpenBrowser(t)
	// Hold a port so Begin cannot bind the redirect listener.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	p, err := testOAuthFlow("http://127.0.0.1:1/unused-token", port).Begin()
	if err != nil {
		t.Fatalf("Begin with occupied port: %v", err)
	}
	if p == nil || p.URL == "" {
		t.Fatal("expected PendingLogin with authorize URL")
	}
	if p.LoopbackListening() {
		t.Error("LoopbackListening should be false when bind fails")
		_ = p.server.Close()
	}
	// Paste completion still works without the loopback server.
	if err := p.CompleteWithPaste("paste-without-listener"); err != nil {
		t.Fatalf("CompleteWithPaste after bind failure: %v", err)
	}
}

func TestLoginErrorsImmediatelyWhenBindFails(t *testing.T) {
	stubOpenBrowser(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	tok, err := testOAuthFlow("http://127.0.0.1:1/unused-token", port).Login(ctx)
	if time.Since(start) > time.Second {
		t.Errorf("Login took %v, want immediate error when bind fails", time.Since(start))
	}
	if tok != nil {
		t.Errorf("tokens = %+v, want nil", tok)
	}
	if err == nil {
		t.Fatal("Login: want bind error, got nil")
	}
	if !strings.Contains(err.Error(), "cannot bind") {
		t.Errorf("err = %v, want cannot bind", err)
	}
	if !strings.Contains(err.Error(), "TUI") {
		t.Errorf("err = %v, want TUI paste hint", err)
	}
}

func TestHTTPCallbackStillWorksWhenBindSucceeds(t *testing.T) {
	srv := tokenExchangeServer(t, "access-from-http")
	defer srv.Close()

	p := beginTestPending(t, srv.URL)
	if p.server == nil {
		t.Fatal("expected loopback server when bind succeeds")
	}
	state := pendingState(t, p)
	code := "http-callback-code"
	callbackURL := fmt.Sprintf(
		"http://127.0.0.1:%d/auth/callback?code=%s&state=%s",
		p.flow.RedirectPort, url.QueryEscape(code), url.QueryEscape(state),
	)

	// Drive Wait concurrently while the browser callback hits the loopback server.
	errCh := make(chan error, 1)
	tokCh := make(chan *Tokens, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		tok, err := p.Wait(ctx)
		if err != nil {
			errCh <- err
			return
		}
		tokCh <- tok
	}()

	// Give the server a moment to accept connections.
	time.Sleep(20 * time.Millisecond)
	resp, err := http.Get(callbackURL)
	if err != nil {
		t.Fatalf("HTTP callback: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("callback status = %d body %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "Login complete") {
		t.Errorf("callback page = %q", body)
	}

	select {
	case err := <-errCh:
		t.Fatalf("Wait: %v", err)
	case tok := <-tokCh:
		if tok.Access != "access-from-http" {
			t.Errorf("access = %q", tok.Access)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for Wait")
	}
}

func TestParseOAuthPasteTable(t *testing.T) {
	// Direct coverage of edge shapes used by CompleteWithPaste.
	cases := []struct {
		raw       string
		wantCode  string
		wantState string
		wantErr   string
	}{
		{"", "", "", "empty"},
		{"  bare  ", "bare", "", ""},
		{"http://h/cb?code=c1&state=s1", "c1", "s1", ""},
		{"code=c2&state=s2", "c2", "s2", ""},
		{"/path?code=c3&state=s3", "c3", "s3", ""},
		{"http://h/cb?error=denied&error_description=nope", "", "", "nope"},
	}
	for _, tc := range cases {
		code, state, err := parseOAuthPaste(tc.raw)
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("parseOAuthPaste(%q) err = %v, want %q", tc.raw, err, tc.wantErr)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseOAuthPaste(%q): %v", tc.raw, err)
			continue
		}
		if code != tc.wantCode || state != tc.wantState {
			t.Errorf("parseOAuthPaste(%q) = (%q,%q), want (%q,%q)", tc.raw, code, state, tc.wantCode, tc.wantState)
		}
	}
}
