package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// FlowConfig describes an OAuth 2.0 authorization-code + PKCE flow with a
// fixed loopback redirect (public CLI clients register an exact host:port,
// so the port is not negotiable).
type FlowConfig struct {
	AuthorizeURL string
	TokenURL     string
	ClientID     string
	Scope        string
	RedirectHost string // e.g. "localhost" or "127.0.0.1"
	RedirectPort int
	RedirectPath string // e.g. "/auth/callback"
	// ExtraParams are provider-specific authorize-URL additions.
	ExtraParams map[string]string
	// IncludeNonce adds an OIDC nonce to the authorize URL (xAI requires it).
	IncludeNonce bool
}

type Tokens struct {
	Access    string
	Refresh   string
	IDToken   string
	ExpiresAt time.Time
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	ExpiresIn    int    `json:"expires_in"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

func (r tokenResponse) toTokens(fallbackRefresh string) *Tokens {
	refresh := r.RefreshToken
	if refresh == "" {
		refresh = fallbackRefresh
	}
	expiresIn := r.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	return &Tokens{
		Access:    r.AccessToken,
		Refresh:   refresh,
		IDToken:   r.IDToken,
		ExpiresAt: time.Now().Add(time.Duration(expiresIn) * time.Second),
	}
}

func (f FlowConfig) redirectURI() string {
	return fmt.Sprintf("http://%s:%d%s", f.RedirectHost, f.RedirectPort, f.RedirectPath)
}

type callbackResult struct {
	code string
	err  error
}

// PendingLogin is an in-flight browser OAuth flow: the loopback server is
// listening, the browser has been opened, and Wait blocks until the
// callback arrives. The split lets a TUI start the flow without blocking
// its event loop (and without stdout printing).
type PendingLogin struct {
	URL string

	flow    FlowConfig
	pkce    pkceCodes
	results chan callbackResult
	server  *http.Server
}

// Begin binds the loopback server, opens the browser, and returns
// immediately. Call Wait to complete the flow; Wait always releases the
// server and port.
func (f FlowConfig) Begin() (*PendingLogin, error) {
	pkce := newPKCE()
	state := randomURLSafe(32)

	params := url.Values{
		"response_type":         {"code"},
		"client_id":             {f.ClientID},
		"redirect_uri":          {f.redirectURI()},
		"scope":                 {f.Scope},
		"code_challenge":        {pkce.challenge},
		"code_challenge_method": {"S256"},
		"state":                 {state},
	}
	if f.IncludeNonce {
		params.Set("nonce", randomURLSafe(32))
	}
	for k, v := range f.ExtraParams {
		params.Set(k, v)
	}
	authorizeURL := f.AuthorizeURL + "?" + params.Encode()

	// The redirect port is part of the client registration; a bind failure
	// usually means another login (or the vendor's own CLI) holds it.
	ln, err := net.Listen("tcp", fmt.Sprintf("%s:%d", f.RedirectHost, f.RedirectPort))
	if err != nil {
		return nil, fmt.Errorf("cannot bind %s (is another login flow or CLI using it?): %w", f.redirectURI(), err)
	}

	results := make(chan callbackResult, 1)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != f.RedirectPath {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		switch {
		case q.Get("error") != "":
			msg := q.Get("error_description")
			if msg == "" {
				msg = q.Get("error")
			}
			writeCallbackPage(w, "Login failed: "+msg)
			results <- callbackResult{err: fmt.Errorf("authorization failed: %s", msg)}
		case q.Get("state") != state:
			writeCallbackPage(w, "Login failed: state mismatch.")
			results <- callbackResult{err: fmt.Errorf("state mismatch in OAuth callback (possible CSRF)")}
		case q.Get("code") == "":
			writeCallbackPage(w, "Login failed: missing authorization code.")
			results <- callbackResult{err: fmt.Errorf("callback missing authorization code")}
		default:
			writeCallbackPage(w, "Login complete. You can close this window and return to strike.")
			results <- callbackResult{code: q.Get("code")}
		}
	})}
	go server.Serve(ln)
	openBrowser(authorizeURL)

	return &PendingLogin{
		URL:     authorizeURL,
		flow:    f,
		pkce:    pkce,
		results: results,
		server:  server,
	}, nil
}

// Wait blocks until the browser callback arrives (or 5 minutes pass),
// then exchanges the code for tokens.
func (p *PendingLogin) Wait(ctx context.Context) (*Tokens, error) {
	defer p.server.Close()
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	select {
	case <-waitCtx.Done():
		return nil, fmt.Errorf("login timed out waiting for the browser callback")
	case res := <-p.results:
		if res.err != nil {
			return nil, res.err
		}
		return p.flow.exchangeCode(ctx, res.code, p.pkce)
	}
}

// Login runs the full browser flow for CLI use: Begin, print the fallback
// URL, Wait.
func (f FlowConfig) Login(ctx context.Context) (*Tokens, error) {
	pending, err := f.Begin()
	if err != nil {
		return nil, err
	}
	fmt.Println("Opening browser for login…")
	fmt.Println("If it does not open, visit:\n\n  " + pending.URL + "\n")
	return pending.Wait(ctx)
}

func (f FlowConfig) exchangeCode(ctx context.Context, code string, pkce pkceCodes) (*Tokens, error) {
	resp, err := postForm(ctx, f.TokenURL, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {f.redirectURI()},
		"client_id":     {f.ClientID},
		"code_verifier": {pkce.verifier},
	})
	if err != nil {
		return nil, fmt.Errorf("token exchange: %w", err)
	}
	return resp.toTokens(""), nil
}

// Refresh exchanges a refresh token for a new token pair. If the server
// rotates the refresh token, the new one is returned; otherwise the old one
// is carried forward.
func (f FlowConfig) Refresh(ctx context.Context, refreshToken string) (*Tokens, error) {
	resp, err := postForm(ctx, f.TokenURL, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {f.ClientID},
	})
	if err != nil {
		return nil, fmt.Errorf("token refresh: %w", err)
	}
	return resp.toTokens(refreshToken), nil
}

func postForm(ctx context.Context, endpoint string, form url.Values) (*tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	httpResp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer httpResp.Body.Close()
	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, err
	}
	var resp tokenResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("bad token endpoint response (%s): %.200s", httpResp.Status, body)
	}
	if resp.Error != "" {
		detail := resp.ErrorDesc
		if detail == "" {
			detail = resp.Error
		}
		return &resp, fmt.Errorf("%s", detail)
	}
	if httpResp.StatusCode != http.StatusOK {
		return &resp, fmt.Errorf("unexpected status %s: %.200s", httpResp.Status, body)
	}
	return &resp, nil
}

func writeCallbackPage(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, "<html><body style=\"font-family: sans-serif; padding: 2rem\"><h2>strike</h2><p>%s</p></body></html>", message)
}

func openBrowser(target string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", target)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		cmd = exec.Command("xdg-open", target)
	}
	_ = cmd.Start()
}
