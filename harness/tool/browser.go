package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jonathanung/strike-cli/harness/sandbox"
	"github.com/jonathanung/strike-cli/pkg/redact"
)

const (
	browserDefaultTimeout    = 30 * time.Second
	browserMaxTimeout        = 120 * time.Second
	browserMaxBody           = 2 << 20
	browserMaxOutputRunes    = 30_000
	browserMaxNetworkRecords = 32
	browserMaxConsoleRecords = 32
	browserMaxActions        = 64
	browserMaxA11yNodes      = 80
	browserUserAgent         = webfetchUserAgent
)

// Computer-use / mutating actions are reserved. This slice is inspect-only.
var browserDeniedActions = map[string]string{
	"click":      "click requires confirmation and is not enabled in the read-only browser slice",
	"type":       "type requires confirmation and is not enabled in the read-only browser slice",
	"fill":       "form fill requires confirmation and is not enabled in the read-only browser slice",
	"press":      "key press requires confirmation and is not enabled in the read-only browser slice",
	"hover":      "hover is not enabled in the read-only browser slice",
	"drag":       "drag is not enabled in the read-only browser slice",
	"select":     "select requires confirmation and is not enabled in the read-only browser slice",
	"upload":     "upload requires confirmation and is not enabled in the read-only browser slice",
	"download":   "download requires confirmation and is not enabled in the read-only browser slice",
	"evaluate":   "script evaluation is not enabled in the read-only browser slice",
	"js":         "script evaluation is not enabled in the read-only browser slice",
	"clipboard":  "clipboard access is not enabled in the read-only browser slice",
	"credential": "credential store access is not enabled in the read-only browser slice",
	"login":      "account changes require confirmation and are not enabled in the read-only browser slice",
}

type browserTool struct{}

// NewBrowser returns the isolated read-only browser inspection tool.
func NewBrowser() Tool { return browserTool{} }

func (browserTool) Name() string { return "browser" }

func (browserTool) Contract() Contract {
	return staticContract(SideEffectNetwork, IdempotencySafeRetry)
}

func (browserTool) Description() string {
	return `Inspect a page in an isolated, read-only browser profile.

- Isolated per session or task (cookies, captures, and action log do not leak across profiles)
- Actions: navigate, snapshot (DOM + accessibility), console, network, screenshot, close
- Domain and network.allow policies are enforced below this tool (same SSRF + allowlist as webfetch)
- Captures are bounded and redacted; downloads, uploads, clicks, typing, and script eval are denied
- Screenshot is reserved until a paint engine ships (http-inspector runtime)
- Prefer this over webfetch when you need session-isolated DOM/a11y/network inspection

This first slice is useful without unrestricted computer control.`
}

func (browserTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"action": {
				"type": "string",
				"enum": ["navigate", "snapshot", "console", "network", "screenshot", "close"],
				"description": "Read-only inspect action. navigate loads a URL; snapshot/console/network read captured state; screenshot is reserved; close destroys the isolated profile."
			},
			"url": {"type": "string", "description": "URL to open (navigate). HTTP is upgraded to HTTPS. Credentials in the URL are rejected."},
			"timeout": {"type": "number", "description": "Optional timeout in seconds for navigate (max 120)"}
		},
		"required": ["action"]
	}`)
}

type browserArgs struct {
	Action  string   `json:"action"`
	URL     string   `json:"url"`
	Timeout *float64 `json:"timeout"`
}

type browserNetworkRec struct {
	Method          string `json:"method"`
	URL             string `json:"url"`
	Status          int    `json:"status"`
	ContentType     string `json:"contentType,omitempty"`
	Bytes           int    `json:"bytes"`
	DownloadBlocked bool   `json:"downloadBlocked,omitempty"`
}

type browserConsoleRec struct {
	Level   string `json:"level"`
	Message string `json:"message"`
}

type browserActionRec struct {
	ID     string `json:"id"`
	Action string `json:"action"`
	URL    string `json:"url,omitempty"`
	Status int    `json:"status,omitempty"`
	At     string `json:"at"`
}

type browserA11yNode struct {
	Role    string `json:"role"`
	Name    string `json:"name,omitempty"`
	Href    string `json:"href,omitempty"`
	Missing string `json:"missing,omitempty"`
}

type browserProfile struct {
	mu          sync.Mutex
	id          string
	dir         string
	currentURL  string
	title       string
	html        string
	status      int
	contentType string
	console     []browserConsoleRec
	network     []browserNetworkRec
	actions     []browserActionRec
	seq         int
	jar         http.CookieJar
}

var (
	browserProfiles   = map[string]*browserProfile{}
	browserProfilesMu sync.Mutex
)

func resetBrowserProfilesForTest() {
	browserProfilesMu.Lock()
	defer browserProfilesMu.Unlock()
	for _, p := range browserProfiles {
		if p != nil && p.dir != "" {
			_ = os.RemoveAll(p.dir)
		}
	}
	browserProfiles = map[string]*browserProfile{}
}

func (browserTool) Execute(ctx context.Context, args json.RawMessage, tc *Context) (Result, error) {
	var a browserArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{}, ErrInvalidArgs("invalid arguments: " + err.Error())
	}
	action := strings.ToLower(strings.TrimSpace(a.Action))
	if action == "" {
		return Result{}, ErrInvalidArgs("action is required")
	}
	if reason, denied := browserDeniedActions[action]; denied {
		return Result{}, ErrBlocked(reason)
	}

	switch action {
	case "navigate":
		return browserNavigate(ctx, a, tc)
	case "snapshot":
		return browserSnapshot(tc)
	case "console":
		return browserConsole(tc)
	case "network":
		return browserNetwork(tc)
	case "screenshot":
		return browserScreenshot(tc)
	case "close":
		return browserClose(tc)
	default:
		return Result{}, ErrInvalidArgs("unknown action " + action)
	}
}

func browserNavigate(ctx context.Context, a browserArgs, tc *Context) (Result, error) {
	if tc != nil && !tc.Sandbox.NetworkEnabled() {
		return Result{}, ErrNetworkDenied("browser blocked: OS network isolation is on (air-gap)")
	}
	rawURL := strings.TrimSpace(a.URL)
	if rawURL == "" {
		return Result{}, ErrInvalidArgs("url is required for navigate")
	}
	finalURL, host, err := browserNormalizeURL(rawURL)
	if err != nil {
		return Result{}, err
	}
	if err := assertPublicHTTPHost(host); err != nil {
		return Result{}, err
	}
	allow := networkAllowFrom(tc)
	if err := sandbox.CheckNetworkAllow(host, allow); err != nil {
		return Result{}, ErrNetworkDenied(err.Error())
	}
	if err := tc.Ask(ctx, AskRequest{
		Permission: "browser",
		Patterns:   []string{finalURL},
		Always:     []string{"*"},
	}); err != nil {
		return Result{}, err
	}

	timeout := browserDefaultTimeout
	if a.Timeout != nil && *a.Timeout > 0 {
		timeout = time.Duration(*a.Timeout * float64(time.Second))
		if timeout > browserMaxTimeout {
			timeout = browserMaxTimeout
		}
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	prof := browserGetOrCreate(tc)
	client := &http.Client{
		Timeout:   timeout,
		Transport: webfetchHTTPTransport(allow),
		Jar:       prof.jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			if req.URL.User != nil {
				return ErrBlocked("redirect with URL credentials blocked")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("redirect to non-http(s) scheme %q blocked", req.URL.Scheme)
			}
			rh := req.URL.Hostname()
			if err := assertPublicHTTPHost(rh); err != nil {
				return err
			}
			if err := sandbox.CheckNetworkAllow(rh, allow); err != nil {
				return ErrNetworkDenied(err.Error())
			}
			return nil
		},
	}

	req, err := http.NewRequestWithContext(runCtx, http.MethodGet, finalURL, nil)
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("User-Agent", browserUserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain;q=0.9,*/*;q=0.1")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := client.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()

	resolved := resp.Request.URL.String()
	if cl := resp.ContentLength; cl > browserMaxBody {
		return Result{}, fmt.Errorf("response too large (exceeds 2MiB limit)")
	}
	limited := io.LimitReader(resp.Body, browserMaxBody+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return Result{}, err
	}
	if len(body) > browserMaxBody {
		return Result{}, fmt.Errorf("response too large (exceeds 2MiB limit)")
	}

	ct := resp.Header.Get("Content-Type")
	downloadBlocked := browserIsDownload(resp.Header.Get("Content-Disposition"), ct)
	html := ""
	title := ""
	if !downloadBlocked {
		html = string(body)
		title = browserExtractTitle(html)
	}

	netRec := browserNetworkRec{
		Method:          http.MethodGet,
		URL:             resolved,
		Status:          resp.StatusCode,
		ContentType:     ct,
		Bytes:           len(body),
		DownloadBlocked: downloadBlocked,
	}
	console := browserConsoleRec{}
	if downloadBlocked {
		console = browserConsoleRec{Level: "warning", Message: "download blocked: " + resolved}
	} else if resp.StatusCode >= 400 {
		console = browserConsoleRec{Level: "error", Message: fmt.Sprintf("HTTP %d %s", resp.StatusCode, resolved)}
	}

	act := prof.record(func(p *browserProfile) browserActionRec {
		if !downloadBlocked {
			p.currentURL = resolved
			p.title = title
			p.html = html
			p.status = resp.StatusCode
			p.contentType = ct
		}
		p.pushNetwork(netRec)
		if console.Message != "" {
			p.pushConsole(console)
		}
		return browserActionRec{Action: "navigate", URL: resolved, Status: resp.StatusCode}
	})
	browserPersistAction(prof, act)

	a11y := browserA11y(html)
	out := browserFormatNavigate(resolved, title, resp.StatusCode, ct, downloadBlocked, html, a11y)
	meta := redact.JSON(mustRaw(map[string]any{
		"action":          "navigate",
		"actionId":        act.ID,
		"profileId":       prof.id,
		"url":             resolved,
		"status":          resp.StatusCode,
		"bytes":           len(body),
		"downloadBlocked": downloadBlocked,
		"runtime":         "http-inspector",
	}))
	return Result{
		Title:    fmt.Sprintf("browser navigate %s", resolved),
		Output:   redact.ScrubToolOutput(out),
		Metadata: meta,
	}, nil
}

func browserSnapshot(tc *Context) (Result, error) {
	prof, ok := browserLookup(tc)
	if !ok || strings.TrimSpace(prof.currentURL) == "" {
		return Result{}, ErrPrecondition("no page loaded; navigate first")
	}
	prof.mu.Lock()
	pageURL := prof.currentURL
	title := prof.title
	html := prof.html
	status := prof.status
	ct := prof.contentType
	prof.mu.Unlock()

	act := prof.record(func(*browserProfile) browserActionRec {
		return browserActionRec{Action: "snapshot", URL: pageURL, Status: status}
	})
	browserPersistAction(prof, act)

	a11y := browserA11y(html)
	out := browserFormatSnapshot(pageURL, title, status, ct, html, a11y)
	meta := redact.JSON(mustRaw(map[string]any{
		"action":    "snapshot",
		"actionId":  act.ID,
		"profileId": prof.id,
		"url":       pageURL,
		"status":    status,
		"runtime":   "http-inspector",
	}))
	return Result{
		Title:    fmt.Sprintf("browser snapshot %s", pageURL),
		Output:   redact.ScrubToolOutput(out),
		Metadata: meta,
	}, nil
}

func browserConsole(tc *Context) (Result, error) {
	prof, ok := browserLookup(tc)
	if !ok {
		return Result{}, ErrPrecondition("no browser profile; navigate first")
	}
	prof.mu.Lock()
	recs := append([]browserConsoleRec(nil), prof.console...)
	pageURL := prof.currentURL
	prof.mu.Unlock()

	act := prof.record(func(*browserProfile) browserActionRec {
		return browserActionRec{Action: "console", URL: pageURL}
	})
	browserPersistAction(prof, act)

	var b strings.Builder
	fmt.Fprintf(&b, "console (%d)\n", len(recs))
	if len(recs) == 0 {
		b.WriteString("(empty — http-inspector has no JavaScript console)\n")
	}
	for _, rec := range recs {
		fmt.Fprintf(&b, "[%s] %s\n", rec.Level, rec.Message)
	}
	meta := redact.JSON(mustRaw(map[string]any{
		"action":    "console",
		"actionId":  act.ID,
		"profileId": prof.id,
		"count":     len(recs),
		"runtime":   "http-inspector",
	}))
	return Result{
		Title:    "browser console",
		Output:   redact.ScrubToolOutput(truncateRunes(b.String(), browserMaxOutputRunes)),
		Metadata: meta,
	}, nil
}

func browserNetwork(tc *Context) (Result, error) {
	prof, ok := browserLookup(tc)
	if !ok {
		return Result{}, ErrPrecondition("no browser profile; navigate first")
	}
	prof.mu.Lock()
	recs := append([]browserNetworkRec(nil), prof.network...)
	pageURL := prof.currentURL
	prof.mu.Unlock()

	act := prof.record(func(*browserProfile) browserActionRec {
		return browserActionRec{Action: "network", URL: pageURL}
	})
	browserPersistAction(prof, act)

	var b strings.Builder
	fmt.Fprintf(&b, "network (%d)\n", len(recs))
	for _, rec := range recs {
		fmt.Fprintf(&b, "%s %s -> %d %s (%d bytes)", rec.Method, rec.URL, rec.Status, rec.ContentType, rec.Bytes)
		if rec.DownloadBlocked {
			b.WriteString(" [download blocked]")
		}
		b.WriteByte('\n')
	}
	meta := redact.JSON(mustRaw(map[string]any{
		"action":    "network",
		"actionId":  act.ID,
		"profileId": prof.id,
		"count":     len(recs),
		"runtime":   "http-inspector",
	}))
	return Result{
		Title:    "browser network",
		Output:   redact.ScrubToolOutput(truncateRunes(b.String(), browserMaxOutputRunes)),
		Metadata: meta,
	}, nil
}

func browserScreenshot(tc *Context) (Result, error) {
	prof := browserGetOrCreate(tc)
	pageURL := ""
	if p, ok := browserLookup(tc); ok {
		p.mu.Lock()
		pageURL = p.currentURL
		p.mu.Unlock()
	}
	act := prof.record(func(*browserProfile) browserActionRec {
		return browserActionRec{Action: "screenshot", URL: pageURL}
	})
	browserPersistAction(prof, act)
	meta := redact.JSON(mustRaw(map[string]any{
		"action":    "screenshot",
		"actionId":  act.ID,
		"profileId": prof.id,
		"url":       pageURL,
		"available": false,
		"runtime":   "http-inspector",
		"reason":    "http-inspector has no paint engine",
	}))
	return Result{
		Title:    "browser screenshot",
		Output:   "screenshot: unavailable\nreason: http-inspector has no paint engine (reserved for a later renderer slice)",
		Metadata: meta,
	}, nil
}

func browserClose(tc *Context) (Result, error) {
	id := browserProfileID(tc)
	browserProfilesMu.Lock()
	prof, ok := browserProfiles[id]
	delete(browserProfiles, id)
	browserProfilesMu.Unlock()
	dir := ""
	n := 0
	if ok && prof != nil {
		prof.mu.Lock()
		dir = prof.dir
		n = len(prof.actions)
		prof.mu.Unlock()
		if dir != "" {
			_ = os.RemoveAll(dir)
		}
	}
	meta, _ := json.Marshal(map[string]any{
		"action":      "close",
		"profileId":   id,
		"removed":     ok,
		"actionCount": n,
		"runtime":     "http-inspector",
	})
	return Result{
		Title:    "browser close",
		Output:   fmt.Sprintf("closed profile %s (removed=%v, actions=%d)", id, ok, n),
		Metadata: meta,
	}, nil
}

func browserNormalizeURL(raw string) (finalURL, host string, err error) {
	if strings.HasPrefix(strings.ToLower(raw), "http://") {
		raw = "https://" + raw[len("http://"):]
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", ErrInvalidArgs("invalid url: " + err.Error())
	}
	if u.User != nil {
		return "", "", ErrBlocked("URL credentials are not allowed")
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return "", "", ErrInvalidArgs("url must use http or https")
	}
	if u.Host == "" {
		return "", "", ErrInvalidArgs("url must include a host")
	}
	return u.String(), u.Hostname(), nil
}

func browserIsDownload(disposition, contentType string) bool {
	d := strings.ToLower(disposition)
	if strings.Contains(d, "attachment") {
		return true
	}
	ct := strings.ToLower(contentType)
	if i := strings.Index(ct, ";"); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	switch ct {
	case "", "text/html", "application/xhtml+xml", "text/plain", "text/markdown",
		"application/json", "application/xml", "text/xml", "text/css":
		return false
	}
	if strings.HasPrefix(ct, "text/") {
		return false
	}
	return true
}

func browserProfileID(tc *Context) string {
	if tc == nil {
		return "anon"
	}
	if s := strings.TrimSpace(tc.SessionID); s != "" {
		return sanitizeBrowserID(s)
	}
	if s := strings.TrimSpace(tc.RootSessionID); s != "" {
		return sanitizeBrowserID(s)
	}
	return "anon"
}

func sanitizeBrowserID(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" {
		return "anon"
	}
	if utf8.RuneCountInString(out) > 80 {
		out = string([]rune(out)[:80])
	}
	return out
}

func browserProfileDir(tc *Context, id string) string {
	if tc != nil && strings.TrimSpace(tc.SessionTempDir) != "" {
		return filepath.Join(tc.SessionTempDir, "browser", id)
	}
	return filepath.Join(os.TempDir(), "strike-browser", id)
}

func browserLookup(tc *Context) (*browserProfile, bool) {
	id := browserProfileID(tc)
	browserProfilesMu.Lock()
	defer browserProfilesMu.Unlock()
	p, ok := browserProfiles[id]
	return p, ok
}

func browserGetOrCreate(tc *Context) *browserProfile {
	id := browserProfileID(tc)
	browserProfilesMu.Lock()
	defer browserProfilesMu.Unlock()
	if p, ok := browserProfiles[id]; ok {
		return p
	}
	jar, _ := cookiejar.New(nil)
	dir := browserProfileDir(tc, id)
	_ = os.MkdirAll(dir, 0o700)
	p := &browserProfile{id: id, dir: dir, jar: jar}
	browserProfiles[id] = p
	return p
}

func (p *browserProfile) record(mut func(*browserProfile) browserActionRec) browserActionRec {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seq++
	act := mut(p)
	act.ID = fmt.Sprintf("ba-%d", p.seq)
	act.At = time.Now().UTC().Format(time.RFC3339)
	p.actions = append(p.actions, act)
	if len(p.actions) > browserMaxActions {
		p.actions = p.actions[len(p.actions)-browserMaxActions:]
	}
	return act
}

func (p *browserProfile) pushNetwork(rec browserNetworkRec) {
	p.network = append(p.network, rec)
	if len(p.network) > browserMaxNetworkRecords {
		p.network = p.network[len(p.network)-browserMaxNetworkRecords:]
	}
}

func (p *browserProfile) pushConsole(rec browserConsoleRec) {
	p.console = append(p.console, rec)
	if len(p.console) > browserMaxConsoleRecords {
		p.console = p.console[len(p.console)-browserMaxConsoleRecords:]
	}
}

func browserPersistAction(p *browserProfile, act browserActionRec) {
	if p == nil || p.dir == "" {
		return
	}
	act.URL = redact.String(act.URL)
	act.Action = redact.String(act.Action)
	line, err := json.Marshal(act)
	if err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(p.dir, "actions.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = f.Write(append(line, '\n'))
	_ = f.Close()
}

func browserExtractTitle(html string) string {
	re := regexp.MustCompile(`(?is)<title\b[^>]*>(.*?)</title>`)
	m := re.FindStringSubmatch(html)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(htmlToText(m[1]))
}

func browserA11y(html string) []browserA11yNode {
	if strings.TrimSpace(html) == "" {
		return nil
	}
	var nodes []browserA11yNode
	add := func(n browserA11yNode) {
		if len(nodes) >= browserMaxA11yNodes {
			return
		}
		n.Name = strings.TrimSpace(n.Name)
		nodes = append(nodes, n)
	}
	hRe := regexp.MustCompile(`(?is)<h([1-6])\b[^>]*>(.*?)</h[1-6]>`)
	for _, m := range hRe.FindAllStringSubmatch(html, -1) {
		add(browserA11yNode{Role: "heading" + m[1], Name: htmlToText(m[2])})
	}
	for _, tag := range []string{"header", "nav", "main", "footer", "aside"} {
		re := regexp.MustCompile(`(?is)<` + tag + `\b`)
		if re.MatchString(html) {
			add(browserA11yNode{Role: tag})
		}
	}
	aRe := regexp.MustCompile(`(?is)<a\b[^>]*href\s*=\s*["']([^"']+)["'][^>]*>(.*?)</a>`)
	for _, m := range aRe.FindAllStringSubmatch(html, -1) {
		add(browserA11yNode{Role: "link", Name: htmlToText(m[2]), Href: m[1]})
	}
	btnRe := regexp.MustCompile(`(?is)<button\b[^>]*>(.*?)</button>`)
	for _, m := range btnRe.FindAllStringSubmatch(html, -1) {
		add(browserA11yNode{Role: "button", Name: htmlToText(m[1])})
	}
	imgRe := regexp.MustCompile(`(?is)<img\b([^>]*)>`)
	altRe := regexp.MustCompile(`(?i)alt\s*=\s*["']([^"']*)["']`)
	for _, m := range imgRe.FindAllStringSubmatch(html, -1) {
		alt := ""
		missing := ""
		if am := altRe.FindStringSubmatch(m[1]); len(am) >= 2 {
			alt = am[1]
		} else {
			missing = "alt"
		}
		add(browserA11yNode{Role: "img", Name: alt, Missing: missing})
	}
	inRe := regexp.MustCompile(`(?is)<input\b([^>]*)>`)
	nameRe := regexp.MustCompile(`(?i)name\s*=\s*["']([^"']*)["']`)
	typeRe := regexp.MustCompile(`(?i)type\s*=\s*["']([^"']*)["']`)
	ariaRe := regexp.MustCompile(`(?i)aria-label\s*=\s*["']([^"']*)["']`)
	for _, m := range inRe.FindAllStringSubmatch(html, -1) {
		name := ""
		typ := "text"
		if nm := nameRe.FindStringSubmatch(m[1]); len(nm) >= 2 {
			name = nm[1]
		}
		if tm := typeRe.FindStringSubmatch(m[1]); len(tm) >= 2 {
			typ = tm[1]
		}
		if am := ariaRe.FindStringSubmatch(m[1]); len(am) >= 2 && name == "" {
			name = am[1]
		}
		missing := ""
		if name == "" {
			missing = "name"
		}
		add(browserA11yNode{Role: "textbox", Name: typ + " " + name, Missing: missing})
	}
	return nodes
}

func browserFormatNavigate(pageURL, title string, status int, ct string, downloadBlocked bool, html string, a11y []browserA11yNode) string {
	var b strings.Builder
	fmt.Fprintf(&b, "url: %s\nstatus: %d\ncontentType: %s\n", pageURL, status, ct)
	if title != "" {
		fmt.Fprintf(&b, "title: %s\n", title)
	}
	if downloadBlocked {
		b.WriteString("download: blocked (read-only inspector does not save files)\n")
		return truncateRunes(b.String(), browserMaxOutputRunes)
	}
	b.WriteString("\n# accessibility\n")
	if len(a11y) == 0 {
		b.WriteString("(none)\n")
	}
	for _, n := range a11y {
		fmt.Fprintf(&b, "- %s", n.Role)
		if n.Name != "" {
			fmt.Fprintf(&b, " %q", n.Name)
		}
		if n.Href != "" {
			fmt.Fprintf(&b, " -> %s", n.Href)
		}
		if n.Missing != "" {
			fmt.Fprintf(&b, " missing=%s", n.Missing)
		}
		b.WriteByte('\n')
	}
	b.WriteString("\n# dom\n")
	b.WriteString(htmlToText(html))
	return truncateRunes(b.String(), browserMaxOutputRunes)
}

func browserFormatSnapshot(pageURL, title string, status int, ct, html string, a11y []browserA11yNode) string {
	return browserFormatNavigate(pageURL, title, status, ct, false, html, a11y)
}

func mustRaw(v map[string]any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}
