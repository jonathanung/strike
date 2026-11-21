package server

// Live preview (#1341): the cockpit's Preview tab shows a dev server that is
// running on the gateway host, through the gateway, never through a raw
// LAN URL.
//
// Boundaries:
//   - The destination is always 127.0.0.1:<port> (or [::1]:<port>). The
//     browser supplies only a port number, never a host or URL.
//   - Nothing is proxied until the user selects a port (PUT /v1/preview).
//     The proxy serves only the selected port. Every other port, and every
//     request after DELETE, gets a 404.
//   - The gateway's own port cannot be selected. Previewing the cockpit
//     would put the gateway's API behind an unauthenticated path.
//   - /preview/<key>/ is not under /v1, so the gateway cookie is not
//     required. The cookie is SameSite=Strict, so a sandboxed frame could
//     not send it anyway. The unguessable per-port key is the capability,
//     and only an authenticated PUT reveals it.
//   - Gateway credentials (Authorization, Cookie) are never forwarded.
//     Set-Cookie from the app is dropped. Every proxied response carries
//     a CSP sandbox without allow-same-origin, so the app runs in an opaque
//     origin and cannot read the cockpit's storage or call /v1 as the user.

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// previewPathPrefix is where the proxy is mounted.
const previewPathPrefix = "/preview/"

// previewCSP is added to every proxied response. It sits next to any CSP
// the app sends, and the browser enforces both policies.
const previewCSP = "sandbox allow-scripts allow-forms allow-popups allow-modals allow-downloads; frame-ancestors 'self'"

// previewProbeTimeout bounds the reachability check on selection.
const previewProbeTimeout = 3 * time.Second

// previewState is the one selected preview target for this gateway.
type previewState struct {
	mu sync.Mutex
	// keys holds a stable random key per port for the life of the process,
	// so reselecting a port keeps the same path and the app's base path
	// setting keeps working.
	keys     map[int]string
	port     int
	selected time.Time
}

func (p *previewState) keyFor(port int) (string, error) {
	if p.keys == nil {
		p.keys = map[int]string{}
	}
	if k, ok := p.keys[port]; ok {
		return k, nil
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	k := hex.EncodeToString(b[:])
	p.keys[port] = k
	return k, nil
}

// previewTarget is what the pane chrome shows. Origin is the real
// destination: the label is built from the same port the proxy dials.
type previewTarget struct {
	Selected bool   `json:"selected"`
	Port     int    `json:"port,omitempty"`
	Origin   string `json:"origin,omitempty"`
	Path     string `json:"path,omitempty"`
	// Probe is filled on selection (and on GET ?probe=1).
	Probe *previewProbe `json:"probe,omitempty"`
}

type previewProbe struct {
	Reachable bool   `json:"reachable"`
	Status    int    `json:"status,omitempty"`
	Error     string `json:"error,omitempty"`
	// Framing is "ok" or "refused". Refused means the app forbids being
	// framed (X-Frame-Options: DENY or CSP frame-ancestors 'none'). The
	// pane says so and offers open-in-browser instead of a blank frame.
	Framing       string `json:"framing,omitempty"`
	FramingHeader string `json:"framingHeader,omitempty"`
}

type previewPortsResponse struct {
	// Detection is "proc" when ports come from /proc/net/tcp{,6}, or
	// "unsupported" on hosts without it. Then the user types a port.
	Detection string        `json:"detection"`
	Ports     []previewPort `json:"ports"`
	// Self is the gateway's own port, which is never selectable.
	Self int `json:"self,omitempty"`
}

type previewPort struct {
	Port int `json:"port"`
}

func previewOrigin(port int) string {
	return "http://127.0.0.1:" + strconv.Itoa(port)
}

// selfPort is the port this gateway listens on, or 0 when unknown.
func (s *Server) selfPort() int {
	addr := s.opts.Addr
	if s.http != nil && s.http.Addr != "" {
		addr = s.http.Addr
	}
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(p)
	return n
}

func (s *Server) handlePreviewPorts(w http.ResponseWriter, r *http.Request) {
	ports, ok := listeningLoopbackPorts()
	resp := previewPortsResponse{Detection: "proc", Ports: []previewPort{}, Self: s.selfPort()}
	if !ok {
		resp.Detection = "unsupported"
	}
	for _, p := range ports {
		if p == resp.Self {
			continue
		}
		resp.Ports = append(resp.Ports, previewPort{Port: p})
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) previewCurrent() (previewTarget, error) {
	s.preview.mu.Lock()
	defer s.preview.mu.Unlock()
	if s.preview.port == 0 {
		return previewTarget{}, nil
	}
	key, err := s.preview.keyFor(s.preview.port)
	if err != nil {
		return previewTarget{}, err
	}
	return previewTarget{
		Selected: true,
		Port:     s.preview.port,
		Origin:   previewOrigin(s.preview.port),
		Path:     previewPathPrefix + key + "/",
	}, nil
}

func (s *Server) handlePreviewGet(w http.ResponseWriter, r *http.Request) {
	cur, err := s.previewCurrent()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: err.Error()})
		return
	}
	if cur.Selected && truthyParam(r.URL.Query().Get("probe")) {
		cur.Probe = probePreview(r.Context(), cur.Port)
	}
	writeJSON(w, http.StatusOK, cur)
}

func truthyParam(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// validatePreviewPort is the whole destination policy: a TCP port number
// that is not the gateway's own. The host is never client-supplied.
func validatePreviewPort(port, self int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("port must be 1-65535")
	}
	if self != 0 && port == self {
		return fmt.Errorf("port %d is this gateway; it cannot preview itself", port)
	}
	return nil
}

func (s *Server) handlePreviewPut(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Port int `json:"port"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	if err := validatePreviewPort(body.Port, s.selfPort()); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	s.preview.mu.Lock()
	if _, err := s.preview.keyFor(body.Port); err != nil {
		s.preview.mu.Unlock()
		writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: err.Error()})
		return
	}
	s.preview.port = body.Port
	s.preview.selected = time.Now()
	s.preview.mu.Unlock()

	cur, err := s.previewCurrent()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: err.Error()})
		return
	}
	cur.Probe = probePreview(r.Context(), cur.Port)
	writeJSON(w, http.StatusOK, cur)
}

func (s *Server) handlePreviewDelete(w http.ResponseWriter, r *http.Request) {
	s.preview.mu.Lock()
	s.preview.port = 0
	s.preview.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// previewDial connects to the loopback port, IPv4 first, then IPv6. It
// ignores the address the HTTP client asks for, so no URL or Host header
// can redirect the proxy off the gateway host.
func previewDial(port int) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err == nil {
			return conn, nil
		}
		if conn6, err6 := d.DialContext(ctx, "tcp", net.JoinHostPort("::1", strconv.Itoa(port))); err6 == nil {
			return conn6, nil
		}
		return nil, err
	}
}

func previewTransport(port int) *http.Transport {
	return &http.Transport{
		Proxy:                 nil,
		DialContext:           previewDial(port),
		ResponseHeaderTimeout: 30 * time.Second,
		IdleConnTimeout:       30 * time.Second,
		MaxIdleConnsPerHost:   4,
	}
}

func probePreview(ctx context.Context, port int) *previewProbe {
	ctx, cancel := context.WithTimeout(ctx, previewProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, previewOrigin(port)+"/", nil)
	if err != nil {
		return &previewProbe{Error: err.Error()}
	}
	tr := previewTransport(port)
	defer tr.CloseIdleConnections()
	client := &http.Client{
		Transport: tr,
		// Report the first answer; following redirects could leave loopback.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		return &previewProbe{Error: fmt.Sprintf("nothing answered on 127.0.0.1:%d (%s)", port, previewDialReason(err))}
	}
	_ = resp.Body.Close()
	framing, header := previewFraming(resp.Header)
	return &previewProbe{Reachable: true, Status: resp.StatusCode, Framing: framing, FramingHeader: header}
}

// previewDialReason shortens a client error for the pane.
func previewDialReason(err error) string {
	var op *net.OpError
	if errors.As(err, &op) && op.Err != nil {
		return op.Err.Error()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timed out"
	}
	return err.Error()
}

// previewFraming classifies the app's own framing policy. The proxied page
// is same-origin with the cockpit, so X-Frame-Options SAMEORIGIN and
// frame-ancestors 'self' still allow it. Only an outright refusal counts.
func previewFraming(h http.Header) (string, string) {
	for _, v := range h.Values("X-Frame-Options") {
		if strings.EqualFold(strings.TrimSpace(v), "deny") {
			return "refused", "X-Frame-Options: " + v
		}
	}
	for _, v := range h.Values("Content-Security-Policy") {
		for _, dir := range strings.Split(v, ";") {
			f := strings.Fields(strings.ToLower(dir))
			if len(f) >= 1 && f[0] == "frame-ancestors" && (len(f) == 1 || (len(f) == 2 && f[1] == "'none'")) {
				return "refused", "Content-Security-Policy: " + strings.TrimSpace(dir)
			}
		}
	}
	return "ok", ""
}

// handlePreviewProxy serves /preview/<key>/<path> for the selected port.
func (s *Server) handlePreviewProxy(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, previewPathPrefix)
	key, sub, hasSlash := strings.Cut(rest, "/")
	s.preview.mu.Lock()
	port := s.preview.port
	want := ""
	if port != 0 {
		want = s.preview.keys[port]
	}
	s.preview.mu.Unlock()
	if port == 0 || want == "" || subtle.ConstantTimeCompare([]byte(key), []byte(want)) != 1 {
		http.NotFound(w, r)
		return
	}
	prefix := previewPathPrefix + key
	if !hasSlash {
		// /preview/<key> → /preview/<key>/ so relative URLs resolve inside.
		http.Redirect(w, r, prefix+"/", http.StatusFound)
		return
	}
	// The gateway's own framing and CSP headers would block the frame and
	// mislabel the app's policy. The proxied response sets its own.
	w.Header().Del("Content-Security-Policy")
	w.Header().Del("X-Frame-Options")
	w.Header().Del("Permissions-Policy")

	target := &url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(port))}
	rp := &httputil.ReverseProxy{
		Transport: previewTransport(port),
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.URL.Path = "/" + sub
			pr.Out.URL.RawPath = ""
			pr.Out.Host = target.Host
			for _, h := range []string{"Authorization", "Cookie", "Proxy-Authorization"} {
				pr.Out.Header.Del(h)
			}
		},
		ModifyResponse: func(resp *http.Response) error {
			resp.Header.Del("Set-Cookie")
			resp.Header.Add("Content-Security-Policy", previewCSP)
			if loc := resp.Header.Get("Location"); loc != "" {
				resp.Header.Set("Location", rewritePreviewLocation(loc, prefix, port))
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("Content-Security-Policy", previewCSP)
			w.WriteHeader(http.StatusBadGateway)
			_, _ = fmt.Fprintf(w, "Preview: nothing answered on 127.0.0.1:%d (%s).\n", port, previewDialReason(err))
		},
	}
	defer rp.Transport.(*http.Transport).CloseIdleConnections()
	rp.ServeHTTP(w, r)
}

// rewritePreviewLocation keeps redirects inside the preview path. An
// absolute-path redirect ("/login") or one back to the app's own loopback
// origin is mapped under prefix. Anything else (another host) is left as
// is: the sandboxed frame navigates there directly, not through the proxy.
func rewritePreviewLocation(loc, prefix string, port int) string {
	if strings.HasPrefix(loc, "/") && !strings.HasPrefix(loc, "//") {
		return prefix + loc
	}
	u, err := url.Parse(loc)
	if err != nil || u.Scheme != "http" {
		return loc
	}
	host, p, err := net.SplitHostPort(u.Host)
	if err != nil || p != strconv.Itoa(port) {
		return loc
	}
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		return loc
	}
	out := prefix + u.EscapedPath()
	if !strings.HasPrefix(u.EscapedPath(), "/") {
		out = prefix + "/" + u.EscapedPath()
	}
	if u.RawQuery != "" {
		out += "?" + u.RawQuery
	}
	return out
}
