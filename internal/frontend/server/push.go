package server

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Web Push for needs-you notifications (#1564).
//
// A browser that turns on push registers its PushSubscription here. When a
// new centre record would banner (prefs enabled, not quiet, not muted — the
// existing policy, no second one) and its kind needs the user, the daemon
// encrypts a generic payload and POSTs it to each subscription's endpoint.
//
// Payload rule: kind + deep link + record id only. The service worker maps
// the kind to fixed banner text; no session names, summaries, prompts, or
// tool text are ever sent, even redacted.
//
// The VAPID private key lives beside the session dir (push/vapid.json, 0600),
// not inside it. Subscription endpoints are delivery addresses: they are
// stored 0600 and never logged or echoed back.

const (
	pushMaxSubs     = 20
	pushEndpointMax = 2048
	pushBodyLimit   = 8 << 10
	pushTTLSeconds  = "3600"
	pushTickEvery   = 2 * time.Second
)

// pushKinds are the kinds that push by default: the ones that need you.
// Finished/failed runs stay in the centre (opt-in is a follow-up).
var pushKinds = map[string]bool{
	noteApproval:     true,
	noteChildBlocked: true,
}

type pushSub struct {
	ID        string    `json:"id"`
	Endpoint  string    `json:"endpoint"`
	P256dh    string    `json:"p256dh"`
	Auth      string    `json:"auth"`
	CreatedAt time.Time `json:"createdAt"`
}

type pushFile struct {
	Subs []pushSub `json:"subs"`
	// Since is the last centre seq considered for push. Rows at or below it
	// never push, so a restart or a new subscription does not replay history.
	Since      int64     `json:"since"`
	LastResult string    `json:"lastResult,omitempty"`
	LastAt     time.Time `json:"lastAt,omitempty"`
}

// PushStatus is the honest delivery state for the prefs UI.
type PushStatus struct {
	Subscriptions int    `json:"subscriptions"`
	LastResult    string `json:"lastResult,omitempty"`
	LastAt        string `json:"lastAt,omitempty"`
}

type pushPayload struct {
	Type string `json:"type"`
	Kind string `json:"kind"`
	Href string `json:"href"`
	Tag  string `json:"tag"`
}

type pushStore struct {
	mu        sync.Mutex
	path      string
	keyPath   string
	state     pushFile
	loaded    bool
	key       *ecdsa.PrivateKey
	client    *http.Client
	now       func() time.Time
	allowHTTP bool // tests: stand-in push endpoint on httptest
	sending   sync.Mutex
}

func (s *Server) pushes() *pushStore {
	s.pushOnce.Do(func() {
		dir := strings.TrimSpace(s.opts.SessionDir)
		if dir == "" {
			return
		}
		s.pushStore = &pushStore{
			path:    filepath.Join(dir, "notifications", "push.json"),
			keyPath: filepath.Join(filepath.Dir(filepath.Clean(dir)), "push", "vapid.json"),
			client:  &http.Client{Timeout: 10 * time.Second},
			now:     time.Now,
		}
	})
	return s.pushStore
}

func (p *pushStore) loadLocked() {
	if p.loaded {
		return
	}
	p.loaded = true
	if b, err := os.ReadFile(p.path); err == nil {
		var st pushFile
		if json.Unmarshal(b, &st) == nil {
			p.state = st
		}
	}
}

func (p *pushStore) persistLocked() error {
	if err := os.MkdirAll(filepath.Dir(p.path), 0o700); err != nil {
		return err
	}
	st := p.state
	if st.Subs == nil {
		st.Subs = []pushSub{}
	}
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return writeAtomic0600(p.path, b)
}

type vapidFile struct {
	Private string `json:"private"`
}

// vapidKeyLocked loads or creates the VAPID signing key on first use.
func (p *pushStore) vapidKeyLocked() (*ecdsa.PrivateKey, error) {
	if p.key != nil {
		return p.key, nil
	}
	if b, err := os.ReadFile(p.keyPath); err == nil {
		var vf vapidFile
		if json.Unmarshal(b, &vf) == nil {
			if raw, err := decodeB64Any(vf.Private); err == nil {
				if key, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), raw); err == nil {
					p.key = key
					return key, nil
				}
			}
		}
	}
	key, err := newVAPIDKey()
	if err != nil {
		return nil, err
	}
	raw, err := key.Bytes()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(p.keyPath), 0o700); err != nil {
		return nil, err
	}
	b, _ := json.Marshal(vapidFile{Private: b64.EncodeToString(raw)})
	if err := writeAtomic0600(p.keyPath, b); err != nil {
		return nil, err
	}
	p.key = key
	return key, nil
}

func (p *pushStore) publicKey() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	key, err := p.vapidKeyLocked()
	if err != nil {
		return "", err
	}
	pub, err := key.PublicKey.Bytes()
	if err != nil {
		return "", err
	}
	return b64.EncodeToString(pub), nil
}

func (p *pushStore) status() PushStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.loadLocked()
	st := PushStatus{Subscriptions: len(p.state.Subs), LastResult: p.state.LastResult}
	if !p.state.LastAt.IsZero() {
		st.LastAt = p.state.LastAt.UTC().Format(time.RFC3339)
	}
	return st
}

func (p *pushStore) validEndpoint(raw string) bool {
	if raw == "" || len(raw) > pushEndpointMax {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" {
		return false
	}
	return u.Scheme == "https" || (p.allowHTTP && u.Scheme == "http")
}

var errPushFull = errors.New("too many push subscriptions")

// subscribe adds or refreshes a subscription. latest primes Since on the
// first subscription so history never pushes.
func (p *pushStore) subscribe(endpoint, p256dh, auth string, latest int64) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.loadLocked()
	for i, sub := range p.state.Subs {
		if sub.Endpoint == endpoint {
			p.state.Subs[i].P256dh = p256dh
			p.state.Subs[i].Auth = auth
			return sub.ID, p.persistLocked()
		}
	}
	if len(p.state.Subs) >= pushMaxSubs {
		return "", errPushFull
	}
	id, err := newID("push_")
	if err != nil {
		return "", err
	}
	if len(p.state.Subs) == 0 && latest > p.state.Since {
		p.state.Since = latest
	}
	p.state.Subs = append(p.state.Subs, pushSub{ID: id, Endpoint: endpoint, P256dh: p256dh, Auth: auth, CreatedAt: p.now().UTC()})
	return id, p.persistLocked()
}

func (p *pushStore) unsubscribe(endpoint string) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.loadLocked()
	for i, sub := range p.state.Subs {
		if sub.Endpoint == endpoint {
			p.state.Subs = append(p.state.Subs[:i], p.state.Subs[i+1:]...)
			return true, p.persistLocked()
		}
	}
	return false, nil
}

// buildPushPayload is the whole wire payload for one record. It reads only
// the kind, the deep link, and the record id.
func buildPushPayload(n Notification) ([]byte, bool) {
	if !pushKinds[n.Kind] {
		return nil, false
	}
	href := n.Href
	if !strings.HasPrefix(href, "/#") {
		href = "/"
	}
	b, err := json.Marshal(pushPayload{Type: "notify", Kind: n.Kind, Href: href, Tag: n.ID})
	return b, err == nil
}

// pushTick sends pushes for centre rows that appeared since the last tick.
// The background loop calls it; tests call it directly.
func (s *Server) pushTick() {
	p := s.pushes()
	if p == nil {
		return
	}
	p.sending.Lock()
	defer p.sending.Unlock()
	p.mu.Lock()
	p.loadLocked()
	subs := append([]pushSub(nil), p.state.Subs...)
	since := p.state.Since
	p.mu.Unlock()
	if len(subs) == 0 {
		return
	}
	log := s.notifications()
	if err := log.sync(); err != nil {
		return
	}
	items, _, latest := log.snapshot(since)
	if latest < since {
		// The centre log was reset; start over from its current end.
		p.mu.Lock()
		p.state.Since = latest
		_ = p.persistLocked()
		p.mu.Unlock()
		return
	}
	var payloads [][]byte
	next := since
	for _, item := range items {
		if item.Seq > next {
			next = item.Seq
		}
		// Banner already folds in enabled, quiet hours, and mutes.
		if !item.Banner {
			continue
		}
		if body, ok := buildPushPayload(item); ok {
			payloads = append(payloads, body)
		}
	}
	gone := map[string]bool{}
	result := ""
	for _, body := range payloads {
		for _, sub := range subs {
			if gone[sub.ID] {
				continue
			}
			code, err := p.deliver(sub, body)
			switch {
			case code == http.StatusNotFound || code == http.StatusGone:
				gone[sub.ID] = true
			case err != nil || code < 200 || code > 299:
				result = "failed"
			case result == "":
				result = "ok"
			}
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if next > p.state.Since {
		p.state.Since = next
	}
	if len(gone) > 0 {
		kept := p.state.Subs[:0]
		for _, sub := range p.state.Subs {
			if !gone[sub.ID] {
				kept = append(kept, sub)
			}
		}
		p.state.Subs = kept
	}
	if result != "" {
		p.state.LastResult = result
		p.state.LastAt = p.now().UTC()
	}
	_ = p.persistLocked()
}

// deliver POSTs one encrypted payload. It returns the push service status.
func (p *pushStore) deliver(sub pushSub, payload []byte) (int, error) {
	if !p.validEndpoint(sub.Endpoint) {
		return http.StatusGone, nil
	}
	uaPub, auth, err := decodeSubscriptionKeys(sub.P256dh, sub.Auth)
	if err != nil {
		return http.StatusGone, nil
	}
	body, err := sealWebPush(uaPub, auth, payload)
	if err != nil {
		return 0, err
	}
	p.mu.Lock()
	key, err := p.vapidKeyLocked()
	p.mu.Unlock()
	if err != nil {
		return 0, err
	}
	authz, err := vapidAuthorization(key, sub.Endpoint, p.now())
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequest(http.MethodPost, sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("TTL", pushTTLSeconds)
	req.Header.Set("Urgency", "high")
	req.Header.Set("Authorization", authz)
	res, err := p.client.Do(req)
	if err != nil {
		return 0, err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
	res.Body.Close()
	return res.StatusCode, nil
}

// runPush is the background sender. It stops when the server shuts down.
func (s *Server) runPush() {
	if s.pushes() == nil {
		return
	}
	t := time.NewTicker(pushTickEvery)
	defer t.Stop()
	for {
		select {
		case <-s.streamDone():
			return
		case <-t.C:
			s.pushTick()
		}
	}
}

func (s *Server) handlePushKey(w http.ResponseWriter, r *http.Request) {
	p := s.pushes()
	if p == nil {
		writeJSON(w, http.StatusServiceUnavailable, opErrorResponse{Error: "push unavailable"})
		return
	}
	key, err := p.publicKey()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: "push key unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"publicKey": key})
}

type pushSubscribeRequest struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
	// Browsers include this in PushSubscription.toJSON(); it is ignored.
	ExpirationTime *int64 `json:"expirationTime"`
}

func decodePushBody(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, pushBodyLimit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	return ensureEOF(dec)
}

func (s *Server) handlePushSubscribe(w http.ResponseWriter, r *http.Request) {
	p := s.pushes()
	if p == nil {
		writeJSON(w, http.StatusServiceUnavailable, opErrorResponse{Error: "push unavailable"})
		return
	}
	var in pushSubscribeRequest
	if err := decodePushBody(w, r, &in); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "invalid push subscription"})
		return
	}
	endpoint := strings.TrimSpace(in.Endpoint)
	if !p.validEndpoint(endpoint) {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "push endpoint must be an https URL"})
		return
	}
	if _, _, err := decodeSubscriptionKeys(in.Keys.P256dh, in.Keys.Auth); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	log := s.notifications()
	if err := log.sync(); err != nil {
		writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: "notifications unavailable"})
		return
	}
	_, _, latest := log.snapshot(math.MaxInt64)
	id, err := p.subscribe(endpoint, in.Keys.P256dh, in.Keys.Auth, latest)
	if err != nil {
		if errors.Is(err, errPushFull) {
			writeJSON(w, http.StatusTooManyRequests, opErrorResponse{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: "could not save push subscription"})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

func (s *Server) handlePushUnsubscribe(w http.ResponseWriter, r *http.Request) {
	p := s.pushes()
	if p == nil {
		writeJSON(w, http.StatusNotFound, opErrorResponse{Error: "push subscription not found"})
		return
	}
	var in struct {
		Endpoint string `json:"endpoint"`
	}
	if err := decodePushBody(w, r, &in); err != nil || strings.TrimSpace(in.Endpoint) == "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "invalid push subscription"})
		return
	}
	found, err := p.unsubscribe(strings.TrimSpace(in.Endpoint))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: "could not remove push subscription"})
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, opErrorResponse{Error: "push subscription not found"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
