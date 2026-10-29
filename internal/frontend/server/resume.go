package server

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// eventCursor is the byte offset to resume a session log from.
// Browsers send Last-Event-ID when EventSource reconnects (phone sleep,
// radio change). ?after= is the same cursor for WebSocket clients, which
// have no Last-Event-ID header. An empty or unparseable value starts at
// the beginning. A cursor past the end of a truncated log is handled by
// the caller.
func eventCursor(r *http.Request) int64 {
	raw := strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	if raw == "" {
		raw = strings.TrimSpace(r.URL.Query().Get("after"))
	}
	if raw == "" {
		return 0
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// requestOriginAllowed reports whether this request's Origin may read
// or mutate the daemon. Loopback and private/CGNAT origins stay on the
// static allowlist. A same-host Origin is also allowed so a phone on
// https://host.tailnet.ts.net (MagicDNS, TLS ended by tailscale serve)
// can POST to the page it loaded. A different host is still rejected,
// which is the CSRF check for the attach cookie.
func requestOriginAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if originAllowed(origin) {
		return true
	}
	return originMatchesRequest(r)
}

// originMatchesRequest reports whether Origin's host:port equals the
// request Host. Scheme must be http or https.
func originMatchesRequest(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" || strings.TrimSpace(r.Host) == "" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

// cookieSecure reports whether the attach cookie should set Secure.
// strike serve speaks cleartext. tailscale serve (and any other TLS
// proxy on loopback) forwards HTTPS with X-Forwarded-Proto. The cookie
// is Secure only in that case, so a phone on http://100.x still stores
// it. The header is ignored from anywhere but loopback so a client on
// the tailnet cannot flip the flag.
func cookieSecure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if !strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https") {
		return false
	}
	ip := ClientIP(r.RemoteAddr)
	return ip != nil && ip.IsLoopback()
}

// clampCursor returns start, or 0 when start is past size (the log was
// truncated or replaced).
func clampCursor(start, size int64) int64 {
	if start < 0 || (size >= 0 && start > size) {
		return 0
	}
	return start
}
