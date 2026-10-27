package server

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	defaultPairTTL          = 5 * time.Minute
	defaultPairRedeemLimit  = 8
	defaultPairRedeemGlobal = 64
	maxLivePairCodes        = 32
	pairCodePrefix          = "p1."
	sessionPrefix           = "s1."
)

// LoopbackOptIn is the exact operator instruction when the gateway is not
// reachable from a phone. It never suggests binding 0.0.0.0.
const LoopbackOptIn = "A phone cannot open this gateway. It is listening on loopback only. Strike will not bind 0.0.0.0. To reach it from a phone, pick one and restart:\n" +
	"1. Private or Tailscale address: strike daemon install --addr <tailnet-or-lan-ip>:8787 --allow-private-bind\n" +
	"2. SSH local forward: ssh -L 8787:127.0.0.1:8787 user@host\n" +
	"A tunnel you run yourself can be named with strike serve --advertise-url <url>. Strike does not start that tunnel."

// LANWarning is shown when the process is bound to a private or tailnet address.
const LANWarning = "Listening on a private network address. Anyone on that network who redeems a pairing code can use the API. Public addresses and 0.0.0.0 stay rejected."

// AdvertiseWarning is shown when the QR uses an operator-supplied tunnel URL.
const AdvertiseWarning = "This QR uses --advertise-url. Strike is still bound only to its listen address. The tunnel is yours to run and to trust."

// GatewayURL is one address a client might open.
type GatewayURL struct {
	Kind           string `json:"kind"`
	URL            string `json:"url"`
	PhoneReachable bool   `json:"phoneReachable"`
}

// GatewayInfo is the public reachability snapshot. It never contains a token
// or a pairing code. Native clients and the web UI share this shape.
type GatewayInfo struct {
	Bind             string       `json:"bind"`
	Auth             bool         `json:"auth"`
	Loopback         bool         `json:"loopback"`
	PhoneReachable   bool         `json:"phoneReachable"`
	LAN              bool         `json:"lan"`
	URLs             []GatewayURL `json:"urls"`
	OptIn            string       `json:"optIn,omitempty"`
	LanWarning       string       `json:"lanWarning,omitempty"`
	AdvertiseWarning string       `json:"advertiseWarning,omitempty"`
	Unbound          []string     `json:"unbound,omitempty"`
}

// PairMint is the body of POST /v1/pair. Code is returned once.
type PairMint struct {
	Code       string      `json:"code"`
	ExpiresAt  string      `json:"expiresAt"`
	TTLSeconds int         `json:"ttlSeconds"`
	URL        string      `json:"url"`
	Gateway    GatewayInfo `json:"gateway"`
}

type pairRedeemResponse struct {
	OK           bool   `json:"ok"`
	DeviceID     string `json:"deviceId"`
	Delivery     string `json:"delivery"`
	SessionToken string `json:"sessionToken,omitempty"`
}

type storedCode struct {
	hash    [32]byte
	expires time.Time
	used    bool
	label   string
}

type storedDevice struct {
	id       string
	label    string
	hash     [32]byte
	created  time.Time
	lastSeen time.Time
	revoked  bool
}

// DeviceView is one paired browser. Session secrets are not included.
type DeviceView struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	CreatedAt string `json:"createdAt"`
	LastSeen  string `json:"lastSeen"`
	Revoked   bool   `json:"revoked"`
}

type pairStore struct {
	mu        sync.Mutex
	ttl       time.Duration
	clock     func() time.Time
	ipLimit   *hitWindow
	globalLim *hitWindow
	bind      string
	auth      bool
	advertise string
	codes     []storedCode
	devices   []storedDevice
	seen      bool
	tokenSum  [32]byte
}

type hitWindow struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	clock  func() time.Time
	hits   map[string][]time.Time
}

func newHitWindow(max int, window time.Duration, clock func() time.Time) *hitWindow {
	if max <= 0 {
		max = 1
	}
	if window <= 0 {
		window = time.Minute
	}
	if clock == nil {
		clock = time.Now
	}
	return &hitWindow{max: max, window: window, clock: clock, hits: map[string][]time.Time{}}
}

func (h *hitWindow) allow(key string) bool {
	now := h.clock()
	h.mu.Lock()
	defer h.mu.Unlock()
	cut := now.Add(-h.window)
	prev := h.hits[key]
	kept := make([]time.Time, 0, len(prev)+1)
	for _, ts := range prev {
		if ts.After(cut) {
			kept = append(kept, ts)
		}
	}
	if len(kept) >= h.max {
		h.hits[key] = kept
		return false
	}
	h.hits[key] = append(kept, now)
	return true
}

func newPairStore(opts Options) *pairStore {
	clock := opts.PairClock
	if clock == nil {
		clock = time.Now
	}
	ttl := opts.PairTTL
	if ttl <= 0 {
		ttl = defaultPairTTL
	}
	limit := opts.PairRedeemLimit
	if limit <= 0 {
		limit = defaultPairRedeemLimit
	}
	global := opts.PairRedeemGlobal
	if global <= 0 {
		global = defaultPairRedeemGlobal
	}
	adv, _ := ValidateAdvertiseURL(opts.AdvertiseURL)
	return &pairStore{
		ttl:       ttl,
		clock:     clock,
		ipLimit:   newHitWindow(limit, time.Minute, clock),
		globalLim: newHitWindow(global, time.Minute, clock),
		bind:      opts.Addr,
		auth:      opts.Auth,
		advertise: adv,
	}
}

// ValidateAdvertiseURL accepts an empty string or an http(s) URL with a host
// and no userinfo, query, or fragment. The returned string is canonical.
func ValidateAdvertiseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", errors.New("advertise url must be http(s) with a host")
	}
	if u.User != nil {
		return "", errors.New("advertise url must not contain credentials")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("advertise url must not contain a query or fragment")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return u.String(), nil
}

func (p *pairStore) setBind(addr string) {
	p.mu.Lock()
	p.bind = strings.TrimSpace(addr)
	p.mu.Unlock()
}

func (p *pairStore) allowRedeem(ip string) bool {
	if strings.TrimSpace(ip) == "" {
		ip = "unknown"
	}
	if !p.ipLimit.allow(ip) {
		return false
	}
	return p.globalLim.allow("global")
}

// observe drops outstanding codes and paired sessions when the daemon token
// changes. strike daemon logout rotates the token file; the next check here
// revokes browsers without a restart. The first observation records the
// baseline and does not revoke.
func (p *pairStore) observe(token string) {
	sum := sha256.Sum256([]byte(token))
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.seen {
		p.seen = true
		p.tokenSum = sum
		return
	}
	if subtle.ConstantTimeCompare(p.tokenSum[:], sum[:]) == 1 {
		return
	}
	p.tokenSum = sum
	p.codes = nil
	for i := range p.devices {
		p.devices[i].revoked = true
		p.devices[i].hash = [32]byte{}
	}
}

func (p *pairStore) info() GatewayInfo {
	p.mu.Lock()
	bind := p.bind
	auth := p.auth
	adv := p.advertise
	p.mu.Unlock()
	return buildGatewayInfo(bind, auth, adv, LANIPs())
}

func buildGatewayInfo(bind string, auth bool, advertise string, unbound []string) GatewayInfo {
	if strings.TrimSpace(bind) == "" {
		bind = "127.0.0.1:8787"
	}
	loopback := IsLocalhostBind(bind)
	lan := !loopback && IsPrivateBind(bind)
	base := httpBase(bind)
	info := GatewayInfo{
		Bind:     bind,
		Auth:     auth,
		Loopback: loopback,
		LAN:      lan,
		URLs: []GatewayURL{{
			Kind:           "bind",
			URL:            base + "/",
			PhoneReachable: !loopback && lan,
		}},
	}
	if loopback {
		info.OptIn = LoopbackOptIn
		var extra []string
		for _, ip := range unbound {
			if ip == "" || strings.HasPrefix(base, "http://"+ip) {
				continue
			}
			extra = append(extra, ip)
		}
		info.Unbound = extra
	}
	if lan {
		info.LanWarning = LANWarning
		info.PhoneReachable = true
	} else if !loopback && advertise == "" {
		info.OptIn = "This bind is not a loopback or private address. Strike serve rejects it. Do not use 0.0.0.0."
	}
	if advertise != "" {
		info.URLs = append(info.URLs, GatewayURL{
			Kind:           "tunnel",
			URL:            strings.TrimRight(advertise, "/") + "/",
			PhoneReachable: true,
		})
		info.PhoneReachable = true
		info.AdvertiseWarning = AdvertiseWarning
		if host := advertiseHost(advertise); host != "" && !IsPrivateBind(host) && !IsLocalhostBind(host) {
			info.AdvertiseWarning += " The advertised host is not a private address."
		}
	}
	return info
}

func httpBase(bind string) string {
	host, port, err := net.SplitHostPort(bind)
	if err != nil {
		return "http://" + strings.TrimRight(bind, "/")
	}
	return "http://" + net.JoinHostPort(host, port)
}

func advertiseHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	host := u.Hostname()
	return host
}

func (p *pairStore) mint(label string) (PairMint, error) {
	plain, hash, err := newSecret(pairCodePrefix, 16)
	if err != nil {
		return PairMint{}, err
	}
	now := p.clock()
	p.mu.Lock()
	live := make([]storedCode, 0, len(p.codes)+1)
	for _, c := range p.codes {
		if c.used || !now.Before(c.expires) {
			continue
		}
		live = append(live, c)
	}
	if len(live) >= maxLivePairCodes {
		p.codes = live
		p.mu.Unlock()
		return PairMint{}, errors.New("too_many_codes")
	}
	expires := now.Add(p.ttl)
	p.codes = append(live, storedCode{hash: hash, expires: expires, label: cleanLabel(label)})
	bind, auth, adv := p.bind, p.auth, p.advertise
	p.mu.Unlock()
	mint := PairMint{
		Code:       plain,
		ExpiresAt:  expires.UTC().Format(time.RFC3339),
		TTLSeconds: int(p.ttl / time.Second),
		Gateway:    buildGatewayInfo(bind, auth, adv, LANIPs()),
	}
	mint.URL = pageURLFrom(mint.Gateway, plain)
	return mint, nil
}

func pageURLFrom(info GatewayInfo, code string) string {
	base := ""
	for _, u := range info.URLs {
		if u.Kind == "tunnel" {
			base = strings.TrimRight(u.URL, "/")
			break
		}
	}
	if base == "" {
		for _, u := range info.URLs {
			if u.Kind == "bind" {
				base = strings.TrimRight(u.URL, "/")
				break
			}
		}
	}
	if code == "" {
		return base + "/"
	}
	return base + "/#pair=" + code
}

type redeemResult struct {
	id      string
	session string
}

func (p *pairStore) redeem(code, label string) (redeemResult, string) {
	code = strings.TrimSpace(code)
	now := p.clock()
	if code == "" || len(code) > 64 || !strings.HasPrefix(code, pairCodePrefix) {
		p.mu.Lock()
		hashes := codeHashes(p.codes)
		p.mu.Unlock()
		_, _ = matchHash("invalid", hashes)
		return redeemResult{}, "invalid_code"
	}
	sum := sha256.Sum256([]byte(code))
	p.mu.Lock()
	defer p.mu.Unlock()
	idx, ok := matchHashSum(sum, codeHashes(p.codes))
	if !ok {
		return redeemResult{}, "invalid_code"
	}
	if p.codes[idx].used {
		return redeemResult{}, "code_used"
	}
	if !now.Before(p.codes[idx].expires) {
		p.codes[idx].used = true
		return redeemResult{}, "code_expired"
	}
	p.codes[idx].used = true
	deviceLabel := cleanLabel(label)
	if deviceLabel == "browser" && p.codes[idx].label != "" && p.codes[idx].label != "browser" {
		deviceLabel = p.codes[idx].label
	}
	session, sessionHash, err := newSecret(sessionPrefix, 32)
	if err != nil {
		return redeemResult{}, "invalid_code"
	}
	id, err := newID("d1.")
	if err != nil {
		return redeemResult{}, "invalid_code"
	}
	p.devices = append(p.devices, storedDevice{
		id:       id,
		label:    deviceLabel,
		hash:     sessionHash,
		created:  now,
		lastSeen: now,
	})
	return redeemResult{id: id, session: session}, ""
}

func codeHashes(codes []storedCode) [][32]byte {
	out := make([][32]byte, len(codes))
	for i := range codes {
		out[i] = codes[i].hash
	}
	return out
}

// matchHash is the constant-time scan used for pairing codes and sessions.
// It always walks the whole slice. An empty slice still performs one compare
// against a zero hash and returns no match.
func matchHash(got string, hashes [][32]byte) (int, bool) {
	return matchHashSum(sha256.Sum256([]byte(got)), hashes)
}

func matchHashSum(sum [32]byte, hashes [][32]byte) (int, bool) {
	scan := hashes
	var dummy [32]byte
	if len(scan) == 0 {
		scan = [][32]byte{dummy}
	}
	idx := 0
	found := 0
	for i := range scan {
		eq := subtle.ConstantTimeCompare(sum[:], scan[i][:])
		idx = subtle.ConstantTimeSelect(eq, i, idx)
		found = subtle.ConstantTimeSelect(eq, 1, found)
	}
	if len(hashes) == 0 || found != 1 {
		return -1, false
	}
	return idx, true
}

func (p *pairStore) sessionOK(token string) bool {
	token = strings.TrimSpace(token)
	if token == "" || !strings.HasPrefix(token, sessionPrefix) || len(token) > 80 {
		return false
	}
	sum := sha256.Sum256([]byte(token))
	now := p.clock()
	p.mu.Lock()
	defer p.mu.Unlock()
	hashes := make([][32]byte, len(p.devices))
	for i := range p.devices {
		hashes[i] = p.devices[i].hash
	}
	idx, ok := matchHashSum(sum, hashes)
	if !ok {
		return false
	}
	if p.devices[idx].revoked {
		return false
	}
	p.devices[idx].lastSeen = now
	return true
}

func (p *pairStore) list() []DeviceView {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]DeviceView, 0, len(p.devices))
	for _, d := range p.devices {
		out = append(out, DeviceView{
			ID:        d.id,
			Label:     d.label,
			CreatedAt: d.created.UTC().Format(time.RFC3339),
			LastSeen:  d.lastSeen.UTC().Format(time.RFC3339),
			Revoked:   d.revoked,
		})
	}
	return out
}

func (p *pairStore) revoke(id string) bool {
	id = strings.TrimSpace(id)
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.devices {
		if p.devices[i].id == id && !p.devices[i].revoked {
			p.devices[i].revoked = true
			p.devices[i].hash = [32]byte{}
			return true
		}
	}
	return false
}

func (p *pairStore) revokeAll() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for i := range p.devices {
		if p.devices[i].revoked {
			continue
		}
		p.devices[i].revoked = true
		p.devices[i].hash = [32]byte{}
		n++
	}
	p.codes = nil
	return n
}

func (p *pairStore) revokeSession(token string) bool {
	token = strings.TrimSpace(token)
	if token == "" || !strings.HasPrefix(token, sessionPrefix) {
		return false
	}
	sum := sha256.Sum256([]byte(token))
	p.mu.Lock()
	defer p.mu.Unlock()
	hashes := make([][32]byte, len(p.devices))
	for i := range p.devices {
		hashes[i] = p.devices[i].hash
	}
	idx, ok := matchHashSum(sum, hashes)
	if !ok || p.devices[idx].revoked {
		return false
	}
	p.devices[idx].revoked = true
	p.devices[idx].hash = [32]byte{}
	return true
}

func newSecret(prefix string, n int) (string, [32]byte, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", [32]byte{}, err
	}
	plain := prefix + base64.RawURLEncoding.EncodeToString(buf)
	return plain, sha256.Sum256([]byte(plain)), nil
}

func newID(prefix string) (string, error) {
	var buf [9]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(buf[:]), nil
}

func cleanLabel(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	for _, r := range s {
		if r < 32 || r == 127 {
			continue
		}
		b.WriteRune(r)
		if b.Len() >= 64 {
			break
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return "browser"
	}
	return out
}

func (s *Server) NoteBind(addr string) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return
	}
	s.opts.Addr = addr
	if s.pairs != nil {
		s.pairs.setBind(addr)
	}
}

// GatewayInfo reports bind, loopback, and tunnel reachability. No secrets.
func (s *Server) GatewayInfo() GatewayInfo {
	if s.pairs == nil {
		return buildGatewayInfo(s.opts.Addr, s.opts.Auth, s.opts.AdvertiseURL, nil)
	}
	return s.pairs.info()
}

// MintPair creates a single-use pairing code. The daemon token is not part
// of the returned URL. Auth must be on.
func (s *Server) MintPair(label string) (PairMint, error) {
	if !s.opts.Auth {
		return PairMint{}, errors.New("auth_disabled")
	}
	if s.pairs == nil {
		return PairMint{}, errors.New("auth_disabled")
	}
	s.pairs.observe(s.currentToken())
	return s.pairs.mint(cleanLabel(label))
}

func (s *Server) handlePairMint(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.opts.Auth {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error":   "auth_disabled",
			"gateway": s.GatewayInfo(),
		})
		return
	}
	if !s.daemonAuthorized(r) {
		if s.pairs != nil && s.pairs.sessionOK(presentedCredential(r)) {
			writeJSON(w, http.StatusForbidden, map[string]any{
				"error":   "daemon_token_required",
				"gateway": s.GatewayInfo(),
			})
			return
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	label := "browser"
	var body struct {
		Label string `json:"label"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048)).Decode(&body)
	if strings.TrimSpace(body.Label) != "" {
		label = body.Label
	}
	minted, err := s.MintPair(label)
	if err != nil {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too_many_codes"})
		return
	}
	tok := s.currentToken()
	if tok != "" && (strings.Contains(minted.URL, tok) || strings.Contains(minted.Code, tok) || strings.Contains(minted.URL, "?token=") || strings.Contains(minted.URL, "?code=")) {
		http.Error(w, "pairing url unsafe", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, minted)
}

func (s *Server) handlePairRedeem(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	ip := requestIP(r)
	allowed := s.pairs == nil || s.pairs.allowRedeem(ip)
	q := r.URL.Query()
	if q.Get("token") != "" || q.Get("code") != "" || q.Get("pair") != "" {
		if !allowed {
			writePairRateLimited(w)
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "query_secret"})
		return
	}
	if !allowed {
		writePairRateLimited(w)
		return
	}
	if !s.opts.Auth || s.pairs == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error":   "auth_disabled",
			"gateway": s.GatewayInfo(),
		})
		return
	}
	s.pairs.observe(s.currentToken())
	var body struct {
		Code     string `json:"code"`
		Label    string `json:"label"`
		Delivery string `json:"delivery"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_body"})
		return
	}
	delivery := strings.TrimSpace(body.Delivery)
	if delivery == "" {
		delivery = "cookie"
	}
	if delivery != "cookie" && delivery != "token" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_delivery"})
		return
	}
	res, kind := s.pairs.redeem(body.Code, body.Label)
	switch kind {
	case "":
	case "code_used", "code_expired":
		writeJSON(w, http.StatusGone, map[string]string{"error": kind})
		return
	default:
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_code"})
		return
	}
	if strings.Contains(res.session, s.currentToken()) && s.currentToken() != "" {
		http.Error(w, "pairing url unsafe", http.StatusInternalServerError)
		return
	}
	if delivery == "cookie" {
		http.SetCookie(w, sessionCookie(res.session, 0))
		writeJSON(w, http.StatusOK, pairRedeemResponse{OK: true, DeviceID: res.id, Delivery: "cookie"})
		return
	}
	writeJSON(w, http.StatusOK, pairRedeemResponse{
		OK:           true,
		DeviceID:     res.id,
		Delivery:     "token",
		SessionToken: res.session,
	})
}

func (s *Server) handlePairDevices(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.opts.Auth || s.pairs == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "auth_disabled"})
		return
	}
	if !s.daemonAuthorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": s.pairs.list()})
}

func (s *Server) handlePairRevoke(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.pairs == nil || !s.daemonAuthorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !s.pairs.revoke(r.PathValue("id")) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handlePairRevokeAll(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.pairs == nil || !s.daemonAuthorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	n := s.pairs.revokeAll()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "revoked": n})
}

func (s *Server) handlePairRevokeSelf(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.opts.Auth || s.pairs == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "auth_disabled"})
		return
	}
	got := presentedCredential(r)
	if tokenEqual(got, s.currentToken()) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "use_device_revoke"})
		return
	}
	if !s.pairs.revokeSession(got) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	http.SetCookie(w, sessionCookie("", -1))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func sessionCookie(value string, maxAge int) *http.Cookie {
	c := &http.Cookie{
		Name:     authCookieName,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	}
	if maxAge != 0 {
		c.MaxAge = maxAge
	} else if value != "" {
		c.MaxAge = 30 * 24 * 3600
	}
	return c
}

func writePairRateLimited(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "60")
	writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "rate_limited"})
}

func presentedCredential(r *http.Request) string {
	if got := bearerToken(r.Header.Get("Authorization")); got != "" {
		return got
	}
	return cookieToken(r)
}

func requestIP(r *http.Request) string {
	ip := ClientIP(r.RemoteAddr)
	if ip == nil {
		return "unknown"
	}
	return ip.String()
}

func (s *Server) daemonAuthorized(r *http.Request) bool {
	if !s.opts.Auth {
		return false
	}
	if s.pairs != nil {
		s.pairs.observe(s.currentToken())
	}
	want := s.currentToken()
	if want == "" {
		return false
	}
	if r.URL.Query().Get("token") != "" || r.URL.Query().Get("code") != "" || r.URL.Query().Get("pair") != "" {
		return false
	}
	got := presentedCredential(r)
	return tokenEqual(got, want)
}
