package plugin

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// defaultHTTPTimeout bounds a single catalog/artifact fetch.
	defaultHTTPTimeout = 2 * time.Minute
	// maxCatalogBytes caps remote catalog JSON (8 MiB).
	maxCatalogBytes = 8 << 20
	// maxArtifactBytes caps a single plugin archive download (64 MiB).
	maxArtifactBytes = 64 << 20
	// maxRedirects limits HTTP redirects for catalog/artifact fetches.
	maxRedirects = 5
	// catalogUserAgent identifies Strike plugin catalog clients.
	catalogUserAgent = "strike-plugin-catalog/1"
)

// httpDoer is satisfied by *http.Client (tests inject httptest clients).
type httpDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

func defaultHTTPClient() *http.Client {
	return &http.Client{
		Timeout: defaultHTTPTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return fmt.Errorf("stopped after %d redirects", maxRedirects)
			}
			if err := validateHTTPURL(req.URL.String()); err != nil {
				return err
			}
			return nil
		},
	}
}

func clientOrDefault(c httpDoer) httpDoer {
	if c != nil {
		return c
	}
	return defaultHTTPClient()
}

// validateHTTPURL requires http(s) with a host; rejects empty, file, and other schemes.
func validateHTTPURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("url is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid url: %w", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return fmt.Errorf("url scheme must be http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("url host is required")
	}
	return nil
}

// downloadBytes GETs url with an upper size bound. Does not follow to non-http(s).
func downloadBytes(ctx context.Context, client httpDoer, rawURL string, maxBytes int64) ([]byte, error) {
	if err := validateHTTPURL(rawURL); err != nil {
		return nil, err
	}
	if maxBytes <= 0 {
		maxBytes = maxArtifactBytes
	}
	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), defaultHTTPTimeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", catalogUserAgent)
	req.Header.Set("Accept", "*/*")

	resp, err := clientOrDefault(client).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("download %s: %s: %s", rawURL, resp.Status, strings.TrimSpace(string(body)))
	}
	// Prefer Content-Length when present and over limit.
	if resp.ContentLength > maxBytes {
		return nil, fmt.Errorf("download %s: content-length %d exceeds limit %d", rawURL, resp.ContentLength, maxBytes)
	}
	// Read at most maxBytes+1 to detect overflow.
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("download %s: body exceeds limit %d bytes", rawURL, maxBytes)
	}
	return data, nil
}
