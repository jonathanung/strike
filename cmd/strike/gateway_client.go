package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jonathanung/strike/pkg/redact"
)

// daemonAuthedJSON calls a loopback daemon route with the host token.
// Callers must not log hostToken, the request, or a success body that
// carries a grant. Error text returned to the user is passed through
// redactSecrets first.
func daemonAuthedJSON(method, rawURL, hostToken string, body any) (int, []byte, error) {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, rawURL, rdr)
	if err != nil {
		return 0, nil, err
	}
	if hostToken != "" {
		req.Header.Set("Authorization", "Bearer "+hostToken)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{Timeout: 10 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	slurp, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return res.StatusCode, nil, err
	}
	return res.StatusCode, slurp, nil
}

// redactSecrets removes known secret values and credential-shaped patterns.
func redactSecrets(s string, secrets ...string) string {
	out := redact.String(s)
	for _, sec := range secrets {
		sec = strings.TrimSpace(sec)
		if len(sec) < 8 {
			continue
		}
		out = strings.ReplaceAll(out, sec, redact.Placeholder)
	}
	return out
}

func daemonBaseURL(base string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return "http://127.0.0.1:8787"
	}
	return base
}

func parseGrantTTL(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = "15m"
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid --ttl %q", raw)
	}
	if d < time.Minute || d > 24*time.Hour {
		return 0, fmt.Errorf("ttl %s is outside 1m..24h", d)
	}
	return d, nil
}

func validDaemonToken(s string) bool {
	if len(s) != 32 {
		return false
	}
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		default:
			return false
		}
	}
	return true
}
