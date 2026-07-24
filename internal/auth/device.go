package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DeviceConfig describes an RFC 8628 device-authorization flow — the
// headless path (VPS, SSH, containers): no loopback server; the user opens
// a short URL on any device and enters a code while the CLI polls.
type DeviceConfig struct {
	DeviceURL string
	TokenURL  string
	ClientID  string
	Scope     string
}

type DeviceCode struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

func (d DeviceConfig) RequestCode(ctx context.Context) (*DeviceCode, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.DeviceURL,
		strings.NewReader(url.Values{"client_id": {d.ClientID}, "scope": {d.Scope}}.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("device code request failed (%s): %.200s", resp.Status, body)
	}
	var code DeviceCode
	if err := json.Unmarshal(body, &code); err != nil {
		return nil, err
	}
	if code.DeviceCode == "" || code.UserCode == "" || code.VerificationURI == "" {
		return nil, fmt.Errorf("device code response missing required fields")
	}
	return &code, nil
}

// Poll long-polls the token endpoint per RFC 8628 §3.5: keep waiting on
// authorization_pending, back off on slow_down, stop on anything else.
func (d DeviceConfig) Poll(ctx context.Context, code *DeviceCode) (*Tokens, error) {
	interval := time.Duration(max(code.Interval, 5)) * time.Second
	expiresIn := code.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 300
	}
	deadline := time.Now().Add(time.Duration(expiresIn) * time.Second)

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
		resp, err := postForm(ctx, d.TokenURL, url.Values{
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
			"client_id":   {d.ClientID},
			"device_code": {code.DeviceCode},
		})
		if err == nil {
			return resp.toTokens(""), nil
		}
		if resp != nil {
			switch resp.Error {
			case "authorization_pending":
				continue
			case "slow_down":
				interval += 5 * time.Second
				continue
			case "access_denied", "authorization_denied":
				return nil, fmt.Errorf("device authorization was denied")
			case "expired_token":
				return nil, fmt.Errorf("device code expired — run login again")
			}
		}
		return nil, err
	}
	return nil, fmt.Errorf("device authorization timed out")
}
