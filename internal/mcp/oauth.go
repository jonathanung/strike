package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// oauthSession holds tokens and AS endpoints for one HTTP MCP server.
type oauthSession struct {
	mu sync.Mutex

	clientID     string
	clientSecret string
	scopes       string
	authorizeURL string
	tokenURL     string
	revokeURL    string
	tokenFile    string

	access  string
	refresh string
	expiry  time.Time

	hc *http.Client
}

type oauthTokenFile struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	Expiry       time.Time `json:"expiry,omitempty"`
	TokenURL     string    `json:"token_url,omitempty"`
	RevokeURL    string    `json:"revoke_url,omitempty"`
	AuthorizeURL string    `json:"authorize_url,omitempty"`
}

type asMetadata struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	RevocationEndpoint    string `json:"revocation_endpoint"`
}

func newOAuthSession(ctx context.Context, cfg ServerConfig) (*oauthSession, error) {
	if cfg.OAuth == nil {
		return nil, nil
	}
	o := cfg.OAuth
	s := &oauthSession{
		clientID:     strings.TrimSpace(o.ClientID),
		clientSecret: o.ClientSecret,
		scopes:       strings.TrimSpace(o.Scopes),
		authorizeURL: strings.TrimSpace(o.AuthorizeURL),
		tokenURL:     strings.TrimSpace(o.TokenURL),
		revokeURL:    strings.TrimSpace(o.RevokeURL),
		tokenFile:    strings.TrimSpace(o.TokenFile),
		hc:           &http.Client{Timeout: 30 * time.Second},
	}
	// Discovery when endpoints incomplete.
	if s.tokenURL == "" || s.authorizeURL == "" {
		disc := strings.TrimSpace(o.DiscoveryURL)
		if disc == "" {
			disc = defaultDiscoveryURL(cfg.URL)
		}
		if disc != "" {
			meta, err := discoverAS(ctx, s.hc, disc)
			if err != nil {
				// Soft-fail discovery when static token file may still work.
				if s.tokenFile == "" {
					return nil, fmt.Errorf("mcp %s: oauth discovery: %w", cfg.Name, err)
				}
			} else {
				if s.authorizeURL == "" {
					s.authorizeURL = meta.AuthorizationEndpoint
				}
				if s.tokenURL == "" {
					s.tokenURL = meta.TokenEndpoint
				}
				if s.revokeURL == "" {
					s.revokeURL = meta.RevocationEndpoint
				}
			}
		}
	}
	if s.tokenFile != "" {
		_ = s.loadTokenFile()
	}
	// Ensure access token is fresh enough for initialize.
	if s.access == "" || s.expired() {
		if s.refresh != "" && s.tokenURL != "" {
			if err := s.refreshTokens(ctx); err != nil && s.access == "" {
				return nil, fmt.Errorf("mcp %s: oauth token: %w", cfg.Name, err)
			}
		}
	}
	return s, nil
}

func defaultDiscoveryURL(mcpURL string) string {
	u, err := url.Parse(strings.TrimSpace(mcpURL))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	// RFC 8414 well-known at origin.
	return u.Scheme + "://" + u.Host + "/.well-known/oauth-authorization-server"
}

func discoverAS(ctx context.Context, hc *http.Client, discoveryURL string) (asMetadata, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, discoveryURL, nil)
	if err != nil {
		return asMetadata{}, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return asMetadata{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return asMetadata{}, fmt.Errorf("status %d", resp.StatusCode)
	}
	var meta asMetadata
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&meta); err != nil {
		return asMetadata{}, err
	}
	if meta.TokenEndpoint == "" {
		return asMetadata{}, fmt.Errorf("missing token_endpoint")
	}
	return meta, nil
}

func (s *oauthSession) accessToken() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.access
}

func (s *oauthSession) expired() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.expiry.IsZero() {
		return s.access == ""
	}
	return time.Now().After(s.expiry.Add(-30 * time.Second))
}

func (s *oauthSession) refreshTokens(ctx context.Context) error {
	if s == nil {
		return fmt.Errorf("oauth not configured")
	}
	s.mu.Lock()
	refresh := s.refresh
	tokenURL := s.tokenURL
	clientID := s.clientID
	secret := s.clientSecret
	s.mu.Unlock()
	if refresh == "" {
		return fmt.Errorf("no refresh token")
	}
	if tokenURL == "" {
		return fmt.Errorf("no token url")
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refresh},
	}
	if clientID != "" {
		form.Set("client_id", clientID)
	}
	if secret != "" {
		form.Set("client_secret", secret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("token refresh status %d", resp.StatusCode)
	}
	var tr struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		Error        string `json:"error"`
	}
	if err := json.Unmarshal(body, &tr); err != nil {
		return err
	}
	if tr.Error != "" || tr.AccessToken == "" {
		return fmt.Errorf("token refresh failed")
	}
	s.mu.Lock()
	s.access = tr.AccessToken
	if tr.RefreshToken != "" {
		s.refresh = tr.RefreshToken
	}
	if tr.ExpiresIn > 0 {
		s.expiry = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	} else {
		s.expiry = time.Now().Add(time.Hour)
	}
	s.mu.Unlock()
	_ = s.saveTokenFile()
	return nil
}

// LoginAuthorizationCode exchanges an auth code for tokens (host-driven login).
func (s *oauthSession) LoginAuthorizationCode(ctx context.Context, code, redirectURI string) error {
	if s == nil {
		return fmt.Errorf("oauth not configured")
	}
	s.mu.Lock()
	tokenURL := s.tokenURL
	clientID := s.clientID
	secret := s.clientSecret
	s.mu.Unlock()
	if tokenURL == "" || clientID == "" {
		return fmt.Errorf("oauth login requires token url and client id")
	}
	form := url.Values{
		"grant_type":   {"authorization_code"},
		"code":         {code},
		"redirect_uri": {redirectURI},
		"client_id":    {clientID},
	}
	if secret != "" {
		form.Set("client_secret", secret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("token exchange status %d", resp.StatusCode)
	}
	var tr struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tr); err != nil {
		return err
	}
	if tr.AccessToken == "" {
		return fmt.Errorf("empty access token")
	}
	s.mu.Lock()
	s.access = tr.AccessToken
	s.refresh = tr.RefreshToken
	if tr.ExpiresIn > 0 {
		s.expiry = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	}
	s.mu.Unlock()
	return s.saveTokenFile()
}

// Revoke best-effort revokes refresh+access tokens.
func (s *oauthSession) Revoke(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	revokeURL := s.revokeURL
	access := s.access
	refresh := s.refresh
	clientID := s.clientID
	secret := s.clientSecret
	s.access, s.refresh = "", ""
	s.expiry = time.Time{}
	tokenFile := s.tokenFile
	s.mu.Unlock()
	if tokenFile != "" {
		_ = os.Remove(tokenFile)
	}
	if revokeURL == "" {
		return nil
	}
	for _, tok := range []string{access, refresh} {
		if tok == "" {
			continue
		}
		form := url.Values{"token": {tok}}
		if clientID != "" {
			form.Set("client_id", clientID)
		}
		if secret != "" {
			form.Set("client_secret", secret)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, revokeURL, strings.NewReader(form.Encode()))
		if err != nil {
			continue
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := s.hc.Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
	}
	return nil
}

// AuthorizeURL builds the browser authorize URL (PKCE left to host when needed).
func (s *oauthSession) AuthorizeURL(redirectURI, state, codeChallenge string) (string, error) {
	if s == nil || s.authorizeURL == "" {
		return "", fmt.Errorf("authorize url not configured")
	}
	u, err := url.Parse(s.authorizeURL)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", s.clientID)
	if redirectURI != "" {
		q.Set("redirect_uri", redirectURI)
	}
	if state != "" {
		q.Set("state", state)
	}
	if s.scopes != "" {
		q.Set("scope", s.scopes)
	}
	if codeChallenge != "" {
		q.Set("code_challenge", codeChallenge)
		q.Set("code_challenge_method", "S256")
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func (s *oauthSession) loadTokenFile() error {
	if s.tokenFile == "" {
		return nil
	}
	data, err := os.ReadFile(s.tokenFile)
	if err != nil {
		return err
	}
	var tf oauthTokenFile
	if err := json.Unmarshal(data, &tf); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.access = tf.AccessToken
	s.refresh = tf.RefreshToken
	s.expiry = tf.Expiry
	if s.tokenURL == "" {
		s.tokenURL = tf.TokenURL
	}
	if s.revokeURL == "" {
		s.revokeURL = tf.RevokeURL
	}
	if s.authorizeURL == "" {
		s.authorizeURL = tf.AuthorizeURL
	}
	return nil
}

func (s *oauthSession) saveTokenFile() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tokenFile == "" {
		return nil
	}
	tf := oauthTokenFile{
		AccessToken:  s.access,
		RefreshToken: s.refresh,
		Expiry:       s.expiry,
		TokenURL:     s.tokenURL,
		RevokeURL:    s.revokeURL,
		AuthorizeURL: s.authorizeURL,
	}
	data, err := json.MarshalIndent(tf, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.tokenFile), 0o700); err != nil {
		return err
	}
	return os.WriteFile(s.tokenFile, append(data, '\n'), 0o600)
}

// OAuthLoginURL is a host helper: build authorize URL for a configured HTTP server.
func OAuthLoginURL(cfg ServerConfig, redirectURI, state, codeChallenge string) (string, error) {
	if cfg.OAuth == nil {
		return "", fmt.Errorf("mcp %s: oauth not configured", cfg.Name)
	}
	s := &oauthSession{
		clientID:     cfg.OAuth.ClientID,
		scopes:       cfg.OAuth.Scopes,
		authorizeURL: cfg.OAuth.AuthorizeURL,
		tokenURL:     cfg.OAuth.TokenURL,
		hc:           &http.Client{Timeout: 15 * time.Second},
	}
	if s.authorizeURL == "" {
		disc := cfg.OAuth.DiscoveryURL
		if disc == "" {
			disc = defaultDiscoveryURL(cfg.URL)
		}
		meta, err := discoverAS(context.Background(), s.hc, disc)
		if err != nil {
			return "", err
		}
		s.authorizeURL = meta.AuthorizationEndpoint
	}
	return s.AuthorizeURL(redirectURI, state, codeChallenge)
}
