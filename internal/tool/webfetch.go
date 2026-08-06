package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jonathanung/strike-cli/internal/sandbox"
)

const (
	webfetchDefaultTimeout = 30 * time.Second
	webfetchMaxTimeout     = 120 * time.Second
	webfetchMaxBody        = 2 << 20 // 2 MiB download bound before convert
	// webfetchMaxOutputRunes caps model-facing output (history token cost).
	webfetchMaxOutputRunes = 30_000
	webfetchUserAgent      = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/143.0.0.0 Safari/537.36"
)

type webFetchTool struct{}

func NewWebFetch() Tool { return webFetchTool{} }

func (webFetchTool) Name() string { return "webfetch" }

func (webFetchTool) Description() string {
	return `Fetches content from a specified URL.

- Takes a URL and optional format as input
- Fetches the URL content, converts to requested format (markdown by default)
- Returns the content in the specified format
- Use this tool when you need to retrieve and analyze web content

Usage notes:
  - IMPORTANT: if another tool is present that offers better web fetching capabilities, is more targeted to the task, or has fewer restrictions, prefer using that tool instead of this one.
  - The URL must be a fully-formed valid URL
  - HTTP URLs will be automatically upgraded to HTTPS
  - Format options: "markdown" (default), "text", or "html"
  - Optional timeout in seconds (default 30, max 120)
  - This tool is read-only and does not modify any files
  - Results may be truncated (~30k characters) if the content is very large
  - Prefer this tool over curl/wget in bash for ordinary page fetches`
}

func (webFetchTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"url": {"type": "string", "description": "The URL to fetch content from"},
			"format": {"type": "string", "enum": ["text", "markdown", "html"], "description": "The format to return the content in (text, markdown, or html). Defaults to markdown."},
			"timeout": {"type": "number", "description": "Optional timeout in seconds (max 120)"}
		},
		"required": ["url"]
	}`)
}

type webFetchArgs struct {
	URL     string   `json:"url"`
	Format  string   `json:"format"`
	Timeout *float64 `json:"timeout"`
}

func (webFetchTool) Execute(ctx context.Context, args json.RawMessage, tc *Context) (Result, error) {
	var a webFetchArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{}, fmt.Errorf("invalid arguments: %w", err)
	}
	if strings.TrimSpace(a.URL) == "" {
		return Result{}, fmt.Errorf("url is required")
	}
	format := strings.ToLower(strings.TrimSpace(a.Format))
	if format == "" {
		format = "markdown"
	}
	switch format {
	case "text", "markdown", "html":
	default:
		return Result{}, fmt.Errorf("format must be text, markdown, or html")
	}

	rawURL := a.URL
	if strings.HasPrefix(strings.ToLower(rawURL), "http://") {
		rawURL = "https://" + rawURL[len("http://"):]
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return Result{}, fmt.Errorf("invalid url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return Result{}, fmt.Errorf("url must use http or https")
	}
	if u.Host == "" {
		return Result{}, fmt.Errorf("url must include a host")
	}
	host := u.Hostname()
	if err := assertPublicHTTPHost(host); err != nil {
		return Result{}, err
	}
	allow := networkAllowFrom(tc)
	if err := sandbox.CheckNetworkAllow(host, allow); err != nil {
		return Result{}, err
	}

	finalURL := u.String()
	if err := tc.Ask(ctx, AskRequest{
		Permission: "webfetch",
		Patterns:   []string{finalURL},
		Always:     []string{"*"},
	}); err != nil {
		return Result{}, err
	}

	timeout := webfetchDefaultTimeout
	if a.Timeout != nil && *a.Timeout > 0 {
		timeout = time.Duration(*a.Timeout * float64(time.Second))
		if timeout > webfetchMaxTimeout {
			timeout = webfetchMaxTimeout
		}
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	client := &http.Client{
		Timeout:   timeout,
		Transport: webfetchHTTPTransport(allow),
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("redirect to non-http(s) scheme %q blocked", req.URL.Scheme)
			}
			rh := req.URL.Hostname()
			if err := assertPublicHTTPHost(rh); err != nil {
				return err
			}
			if err := sandbox.CheckNetworkAllow(rh, allow); err != nil {
				return err
			}
			return nil
		},
	}

	req, err := http.NewRequestWithContext(runCtx, http.MethodGet, finalURL, nil)
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("User-Agent", webfetchUserAgent)
	req.Header.Set("Accept", webfetchAccept(format))
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := client.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Result{}, fmt.Errorf("request failed with status %s", resp.Status)
	}
	if cl := resp.ContentLength; cl > webfetchMaxBody {
		return Result{}, fmt.Errorf("response too large (exceeds 5MiB limit)")
	}

	limited := io.LimitReader(resp.Body, webfetchMaxBody+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return Result{}, err
	}
	if len(body) > webfetchMaxBody {
		return Result{}, fmt.Errorf("response too large (exceeds 5MiB limit)")
	}

	contentType := resp.Header.Get("Content-Type")
	content := string(body)
	output := content
	if strings.Contains(strings.ToLower(contentType), "text/html") {
		switch format {
		case "text":
			output = htmlToText(content)
		case "markdown":
			output = htmlToMarkdown(content)
		}
	}
	output = truncateRunes(output, webfetchMaxOutputRunes)

	meta, _ := json.Marshal(map[string]any{
		"url":    resp.Request.URL.String(),
		"status": resp.StatusCode,
		"bytes":  len(body),
		"format": format,
	})
	return Result{
		Title:    fmt.Sprintf("%s (%s)", resp.Request.URL.String(), contentType),
		Output:   output,
		Metadata: meta,
	}, nil
}

func webfetchAccept(format string) string {
	switch format {
	case "markdown":
		return "text/markdown;q=1.0, text/x-markdown;q=0.9, text/plain;q=0.8, text/html;q=0.7, */*;q=0.1"
	case "text":
		return "text/plain;q=1.0, text/markdown;q=0.9, text/html;q=0.8, */*;q=0.1"
	case "html":
		return "text/html;q=1.0, application/xhtml+xml;q=0.9, text/plain;q=0.8, text/markdown;q=0.7, */*;q=0.1"
	default:
		return "*/*"
	}
}

// Test seams (tests only; production leaves these nil/empty):
//
//	webfetchTestAllowHost — accepted by assertPublicHTTPHost without SSRF checks
//	webfetchTestTransport — used as http.Client.Transport when non-nil (TLS httptest)
var (
	webfetchTestAllowHost string
	webfetchTestTransport http.RoundTripper
)

// networkAllowFrom returns the config allowlist from tc (nil-safe).
func networkAllowFrom(tc *Context) []string {
	if tc == nil {
		return nil
	}
	return tc.NetworkAllow
}

// webfetchHTTPTransport returns the RoundTripper used for fetches.
// Tests may inject webfetchTestTransport (e.g. TLS InsecureSkipVerify for httptest).
// Production uses a transport whose DialContext resolves and filters blocked IPs
// at connect time, closing the DNS-rebinding TOCTOU window of a separate LookupIP.
// allow is the optional network.allow list (empty = unrestricted public).
func webfetchHTTPTransport(allow []string) http.RoundTripper {
	if webfetchTestTransport != nil {
		// Test transports skip custom dial; allowlist is enforced pre-request
		// and on redirects. Production dial re-checks for DNS rebinding.
		return webfetchTestTransport
	}
	return newWebfetchSafeTransport(allow)
}

func newWebfetchSafeTransport(allow []string) *http.Transport {
	base, ok := http.DefaultTransport.(*http.Transport)
	var t *http.Transport
	if ok {
		t = base.Clone()
	} else {
		t = &http.Transport{}
	}
	// Defensive copy so later mutation of the caller's slice cannot widen dial.
	allow = sandbox.CloneNetworkAllow(allow)
	dialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		ipAddr, err := resolvePublicDialAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		if err := sandbox.CheckNetworkDialAllow(host, ipAddr, allow); err != nil {
			return nil, err
		}
		// Dial the filtered IP; http.Transport keeps TLS ServerName / Host as the
		// original request hostname, so certificate verification still works.
		return dialer.DialContext(ctx, network, net.JoinHostPort(ipAddr, port))
	}
	return t
}

// resolvePublicDialAddr resolves host (or parses a literal IP) and returns the
// first non-blocked address string suitable for dialing.
func resolvePublicDialAddr(ctx context.Context, host string) (string, error) {
	if ip := net.ParseIP(host); ip != nil {
		if isBlockedIP(ip) {
			return "", fmt.Errorf("refusing to fetch private or local address %s", host)
		}
		return ip.String(), nil
	}
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return "", fmt.Errorf("resolving host %q: %w", host, err)
	}
	for _, a := range addrs {
		if !isBlockedIP(a.IP) {
			return a.IP.String(), nil
		}
	}
	if len(addrs) == 0 {
		return "", fmt.Errorf("host %q resolved to no addresses", host)
	}
	return "", fmt.Errorf("refusing to fetch %s: resolves to private or local address %s", host, addrs[0].IP)
}

func assertPublicHTTPHost(host string) error {
	if host == "" {
		return fmt.Errorf("url host is empty")
	}
	if webfetchTestAllowHost != "" && host == webfetchTestAllowHost {
		return nil
	}
	// Hostname() already strips brackets from IPv6 literals.
	// Pre-check for early rejection; DialContext re-validates at connect time.
	if ip := net.ParseIP(host); ip != nil {
		if isBlockedIP(ip) {
			return fmt.Errorf("refusing to fetch private or local address %s", host)
		}
		return nil
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("resolving host %q: %w", host, err)
	}
	if len(ips) == 0 {
		return fmt.Errorf("host %q resolved to no addresses", host)
	}
	for _, ip := range ips {
		if isBlockedIP(ip) {
			return fmt.Errorf("refusing to fetch %s: resolves to private or local address %s", host, ip)
		}
	}
	return nil
}

func isBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	// CGNAT / shared address space (RFC 6598) and benchmark range (RFC 2544).
	if ip4 := ip.To4(); ip4 != nil {
		if ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127 {
			return true
		}
		if ip4[0] == 198 && (ip4[1] == 18 || ip4[1] == 19) {
			return true
		}
	}
	return false
}

func truncateRunes(s string, max int) string {
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	var b strings.Builder
	n := 0
	for _, r := range s {
		if n >= max {
			break
		}
		b.WriteRune(r)
		n++
	}
	return b.String() + "\n\n… (output truncated)"
}

var (
	htmlScriptRe   = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script>`)
	htmlStyleRe    = regexp.MustCompile(`(?is)<style\b[^>]*>.*?</style>`)
	htmlNoscriptRe = regexp.MustCompile(`(?is)<noscript\b[^>]*>.*?</noscript>`)
	htmlTagRe      = regexp.MustCompile(`(?s)<[^>]+>`)
	htmlWSRe       = regexp.MustCompile(`\s+`)
	htmlEntityRe   = regexp.MustCompile(`&(#x?[0-9a-fA-F]+|\w+);`)
)

func stripHTMLNoise(html string) string {
	s := htmlScriptRe.ReplaceAllString(html, " ")
	s = htmlStyleRe.ReplaceAllString(s, " ")
	s = htmlNoscriptRe.ReplaceAllString(s, " ")
	return s
}

func htmlToText(html string) string {
	s := stripHTMLNoise(html)
	s = htmlTagRe.ReplaceAllString(s, " ")
	s = decodeBasicEntities(s)
	s = htmlWSRe.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// htmlToMarkdown is a best-effort converter for common structural tags.
func htmlToMarkdown(html string) string {
	s := stripHTMLNoise(html)
	// Headings
	for i := 6; i >= 1; i-- {
		re := regexp.MustCompile(fmt.Sprintf(`(?is)<h%d\b[^>]*>(.*?)</h%d>`, i, i))
		prefix := strings.Repeat("#", i) + " "
		s = re.ReplaceAllStringFunc(s, func(m string) string {
			inner := re.FindStringSubmatch(m)
			if len(inner) < 2 {
				return m
			}
			return "\n\n" + prefix + strings.TrimSpace(htmlToText(inner[1])) + "\n\n"
		})
	}
	// Links
	aRe := regexp.MustCompile(`(?is)<a\b[^>]*href\s*=\s*["']([^"']+)["'][^>]*>(.*?)</a>`)
	s = aRe.ReplaceAllStringFunc(s, func(m string) string {
		parts := aRe.FindStringSubmatch(m)
		if len(parts) < 3 {
			return m
		}
		text := strings.TrimSpace(htmlToText(parts[2]))
		if text == "" {
			text = parts[1]
		}
		return fmt.Sprintf("[%s](%s)", text, parts[1])
	})
	// Code blocks and inline code
	preRe := regexp.MustCompile(`(?is)<pre\b[^>]*>(.*?)</pre>`)
	s = preRe.ReplaceAllStringFunc(s, func(m string) string {
		parts := preRe.FindStringSubmatch(m)
		if len(parts) < 2 {
			return m
		}
		code := htmlTagRe.ReplaceAllString(parts[1], "")
		code = decodeBasicEntities(code)
		return "\n\n```\n" + strings.TrimRight(code, "\n") + "\n```\n\n"
	})
	codeRe := regexp.MustCompile(`(?is)<code\b[^>]*>(.*?)</code>`)
	s = codeRe.ReplaceAllStringFunc(s, func(m string) string {
		parts := codeRe.FindStringSubmatch(m)
		if len(parts) < 2 {
			return m
		}
		return "`" + strings.TrimSpace(htmlToText(parts[1])) + "`"
	})
	// Lists
	liRe := regexp.MustCompile(`(?is)<li\b[^>]*>(.*?)</li>`)
	s = liRe.ReplaceAllStringFunc(s, func(m string) string {
		parts := liRe.FindStringSubmatch(m)
		if len(parts) < 2 {
			return m
		}
		return "\n- " + strings.TrimSpace(htmlToText(parts[1]))
	})
	// Paragraphs and breaks
	s = regexp.MustCompile(`(?is)</p\s*>`).ReplaceAllString(s, "\n\n")
	s = regexp.MustCompile(`(?is)<br\s*/?>`).ReplaceAllString(s, "\n")
	s = regexp.MustCompile(`(?is)<p\b[^>]*>`).ReplaceAllString(s, "")
	// Remaining tags
	s = htmlTagRe.ReplaceAllString(s, "")
	s = decodeBasicEntities(s)
	// Collapse excess blank lines
	s = regexp.MustCompile(`[ \t]+\n`).ReplaceAllString(s, "\n")
	s = regexp.MustCompile(`\n{3,}`).ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

func decodeBasicEntities(s string) string {
	return htmlEntityRe.ReplaceAllStringFunc(s, func(e string) string {
		switch e {
		case "&amp;":
			return "&"
		case "&lt;":
			return "<"
		case "&gt;":
			return ">"
		case "&quot;":
			return "\""
		case "&apos;", "&#39;":
			return "'"
		case "&nbsp;":
			return " "
		}
		// Numeric entities are left as-is for simplicity.
		return e
	})
}
