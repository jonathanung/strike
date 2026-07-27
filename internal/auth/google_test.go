package auth

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestGoogleFlowDefaults(t *testing.T) {
	t.Setenv("GOOGLE_CLIENT_ID", "")
	flow := GoogleFlow()
	if flow.AuthorizeURL != googleAuthBase {
		t.Errorf("AuthorizeURL = %q, want %q", flow.AuthorizeURL, googleAuthBase)
	}
	if flow.TokenURL != googleTokenURL {
		t.Errorf("TokenURL = %q, want %q", flow.TokenURL, googleTokenURL)
	}
	if flow.Scope != googleScope {
		t.Errorf("Scope = %q, want %q", flow.Scope, googleScope)
	}
	if flow.ClientID != "" {
		t.Errorf("ClientID = %q, want empty when env unset", flow.ClientID)
	}
	if flow.RedirectPort != googleRedirectPort {
		t.Errorf("RedirectPort = %d, want %d", flow.RedirectPort, googleRedirectPort)
	}
	if flow.RedirectHost != "localhost" {
		t.Errorf("RedirectHost = %q", flow.RedirectHost)
	}
	if flow.RedirectPath != "/oauth/callback" {
		t.Errorf("RedirectPath = %q", flow.RedirectPath)
	}
	if flow.ExtraParams["access_type"] != "offline" {
		t.Errorf("access_type = %q", flow.ExtraParams["access_type"])
	}
	if flow.ExtraParams["prompt"] != "consent" {
		t.Errorf("prompt = %q", flow.ExtraParams["prompt"])
	}
}

func TestGoogleFlowWithClientID(t *testing.T) {
	t.Setenv("GOOGLE_CLIENT_ID", "test-client-id-123.apps.googleusercontent.com")
	flow := GoogleFlow()
	if flow.ClientID != "test-client-id-123.apps.googleusercontent.com" {
		t.Errorf("ClientID = %q", flow.ClientID)
	}
}

func TestGoogleFlowAuthorizeURLContainsRequiredParams(t *testing.T) {
	t.Setenv("GOOGLE_CLIENT_ID", "test-client.apps.googleusercontent.com")
	flow := GoogleFlow()
	pending, err := flow.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if pending.server != nil {
		defer pending.server.Close()
	}
	u := pending.URL
	// Values in authorize URLs are URL-encoded by url.Values.Encode().
	decoded, err := url.QueryUnescape(u)
	if err != nil {
		t.Fatalf("QueryUnescape: %v", err)
	}
	for _, want := range []string{
		"response_type=code",
		"client_id=test-client.apps.googleusercontent.com",
		"redirect_uri=http://localhost:8765/oauth/callback",
		"scope=" + googleScope,
		"code_challenge_method=S256",
		"access_type=offline",
		"prompt=consent",
	} {
		if !strings.Contains(decoded, want) {
			t.Errorf("authorize URL missing %q: %s", want, u)
		}
	}
	if !strings.Contains(u, "code_challenge=") {
		t.Errorf("authorize URL missing code_challenge")
	}
	if !strings.Contains(u, "state=") {
		t.Errorf("authorize URL missing state")
	}
}

func TestGoogleRefreshFlow(t *testing.T) {
	flow, ok := refreshFlows["gemini"]
	if !ok {
		t.Fatal("gemini not in refreshFlows")
	}
	if flow.AuthorizeURL != googleAuthBase {
		t.Errorf("refresh flow AuthorizeURL = %q, want %q", flow.AuthorizeURL, googleAuthBase)
	}
}

func TestGoogleFlowFullExchange(t *testing.T) {
	t.Setenv("GOOGLE_CLIENT_ID", "test-client.apps.googleusercontent.com")
	os.Unsetenv("GOOGLE_CLIENT_SECRET")

	// Use the existing tokenExchangeServer helper.
	srv := tokenExchangeServer(t, "google-oauth-access-token")
	defer srv.Close()

	// Override the flow's TokenURL for testing.
	flow := GoogleFlow()
	flow.TokenURL = srv.URL
	port := freeLoopbackPort(t)
	flow.RedirectPort = port

	stubOpenBrowser(t)
	pending, err := flow.Begin()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if pending.server != nil {
			_ = pending.server.Close()
		}
	})
	if pending.server == nil {
		t.Fatal("Begin should bind the loopback server")
	}

	state := pendingState(t, pending)
	code := "google-auth-code"

	tokCh := make(chan *Tokens, 1)
	errCh := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		tok, err := pending.Wait(ctx)
		if err != nil {
			errCh <- err
			return
		}
		tokCh <- tok
	}()

	// Hit the loopback server with the callback.
	time.Sleep(20 * time.Millisecond)
	callbackURL := fmt.Sprintf("http://127.0.0.1:%d/oauth/callback?code=%s&state=%s", port, code, state)
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
		if tok.Access != "google-oauth-access-token" {
			t.Errorf("access = %q", tok.Access)
		}
		if tok.Refresh != "refresh-test" {
			t.Errorf("refresh = %q", tok.Refresh)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for Wait")
	}
}
