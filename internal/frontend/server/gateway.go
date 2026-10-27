package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jonathanung/strike/internal/product/version"
)

// maxGatewayGrants caps in-memory short-lived bearers. The accept path
// always compares against this many slots so a miss does not return faster
// when fewer grants are live.
const maxGatewayGrants = 32

const (
	defaultGrantTTL = 15 * time.Minute
	minGrantTTL     = time.Minute
	maxGrantTTL     = 24 * time.Hour
)

// storedGrant keeps the SHA-256 of a grant, not the grant. The raw value is
// returned once at mint and is never written to disk.
type storedGrant struct {
	sum     [32]byte
	expires time.Time
}

func (s *Server) nowGrant() time.Time {
	if s.grantNow != nil {
		return s.grantNow()
	}
	return time.Now()
}

// observeRoot drops every grant when the host token changes. The first
// observation records the hash and does not revoke, so startup is a no-op.
func (s *Server) observeRoot(tok string) {
	sum := sha256.Sum256([]byte(tok))
	s.grantMu.Lock()
	defer s.grantMu.Unlock()
	if !s.rootSeen {
		s.rootSum = sum
		s.rootSeen = true
		return
	}
	if subtle.ConstantTimeCompare(s.rootSum[:], sum[:]) != 1 {
		s.rootSum = sum
		s.grants = nil
	}
}

// acceptAnyGrant reports whether any presented value matches a live grant.
// Empty strings are hashed and compared too, so a missing header does not
// skip the scan. The slot count is fixed at maxGatewayGrants.
func (s *Server) acceptAnyGrant(presented ...string) bool {
	sums := make([][32]byte, len(presented))
	for i, p := range presented {
		sums[i] = sha256.Sum256([]byte(p))
	}
	now := s.nowGrant()
	s.grantMu.Lock()
	defer s.grantMu.Unlock()
	var slots [maxGatewayGrants][32]byte
	n := 0
	kept := 0
	for _, g := range s.grants {
		if !now.Before(g.expires) {
			continue
		}
		s.grants[kept] = g
		kept++
		if n < maxGatewayGrants {
			slots[n] = g.sum
			n++
		}
	}
	s.grants = s.grants[:kept]
	var match int
	for _, sum := range sums {
		for i := 0; i < maxGatewayGrants; i++ {
			match |= subtle.ConstantTimeCompare(sum[:], slots[i][:])
		}
	}
	return match == 1
}

func (s *Server) mintGrant(ttl time.Duration) (string, time.Time, error) {
	if ttl < minGrantTTL || ttl > maxGrantTTL {
		return "", time.Time{}, errors.New("ttl must be between 1m and 24h")
	}
	tok, err := MintToken()
	if err != nil {
		return "", time.Time{}, err
	}
	sum := sha256.Sum256([]byte(tok))
	exp := s.nowGrant().Add(ttl)
	s.grantMu.Lock()
	defer s.grantMu.Unlock()
	now := s.nowGrant()
	kept := 0
	for _, g := range s.grants {
		if now.Before(g.expires) {
			s.grants[kept] = g
			kept++
		}
	}
	s.grants = s.grants[:kept]
	if len(s.grants) >= maxGatewayGrants {
		return "", time.Time{}, errors.New("too many grants")
	}
	s.grants = append(s.grants, storedGrant{sum: sum, expires: exp})
	return tok, exp, nil
}

func (s *Server) revokeAllGrants() int {
	s.grantMu.Lock()
	defer s.grantMu.Unlock()
	n := len(s.grants)
	s.grants = nil
	return n
}

func (s *Server) revokeGrantValue(presented string) bool {
	if presented == "" {
		return false
	}
	sum := sha256.Sum256([]byte(presented))
	s.grantMu.Lock()
	defer s.grantMu.Unlock()
	var match int
	kept := 0
	for _, g := range s.grants {
		eq := subtle.ConstantTimeCompare(sum[:], g.sum[:])
		match |= eq
		if eq != 1 {
			s.grants[kept] = g
			kept++
		}
	}
	s.grants = s.grants[:kept]
	return match == 1
}

func (s *Server) liveGrantCount() int {
	now := s.nowGrant()
	s.grantMu.Lock()
	defer s.grantMu.Unlock()
	n := 0
	for _, g := range s.grants {
		if now.Before(g.expires) {
			n++
		}
	}
	return n
}

func writeDaemonTokenFile(path, tok string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(tok+"\n"), 0o600)
}

type gatewayInfoBody struct {
	OK               bool         `json:"ok"`
	Auth             bool         `json:"auth"`
	Bind             string       `json:"bind"`
	Loopback         bool         `json:"loopback"`
	TokenFile        bool         `json:"tokenFile"`
	Grants           int          `json:"grants"`
	Credential       string       `json:"credential"`
	Version          string       `json:"version"`
	PhoneReachable   bool         `json:"phoneReachable"`
	LAN              bool         `json:"lan"`
	URLs             []GatewayURL `json:"urls"`
	OptIn            string       `json:"optIn,omitempty"`
	LanWarning       string       `json:"lanWarning,omitempty"`
	AdvertiseWarning string       `json:"advertiseWarning,omitempty"`
	Unbound          []string     `json:"unbound,omitempty"`
}

func (s *Server) gatewayInfo(r *http.Request) gatewayInfoBody {
	cred := "open"
	if s.opts.Auth {
		switch {
		case s.rootAuthorized(r):
			cred = "root"
		case s.sessionCredentialOK(r):
			cred = "session"
		default:
			cred = "grant"
		}
	}
	reach := s.GatewayInfo()
	bind := reach.Bind
	if bind == "" {
		bind = s.opts.Addr
	}
	return gatewayInfoBody{
		OK:               true,
		Auth:             s.opts.Auth,
		Bind:             bind,
		Loopback:         reach.Loopback,
		TokenFile:        strings.TrimSpace(s.opts.TokenFile) != "",
		Grants:           s.liveGrantCount(),
		Credential:       cred,
		Version:          version.Version,
		PhoneReachable:   reach.PhoneReachable,
		LAN:              reach.LAN,
		URLs:             reach.URLs,
		OptIn:            reach.OptIn,
		LanWarning:       reach.LanWarning,
		AdvertiseWarning: reach.AdvertiseWarning,
		Unbound:          reach.Unbound,
	}
}

func (s *Server) handleGatewayInfo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, s.gatewayInfo(r))
}

type grantMintBody struct {
	TTLSeconds int `json:"ttlSeconds"`
}

func (s *Server) handleGatewayMint(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.opts.Auth {
		writeJSON(w, http.StatusForbidden, opErrorResponse{Error: "gateway grants require --auth"})
		return
	}
	if !s.rootAuthorized(r) {
		writeJSON(w, http.StatusForbidden, opErrorResponse{Error: "host token required"})
		return
	}
	var body grantMintBody
	dec := json.NewDecoder(io.LimitReader(r.Body, 1024))
	if err := dec.Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "invalid json"})
		return
	}
	ttl := defaultGrantTTL
	if body.TTLSeconds != 0 {
		ttl = time.Duration(body.TTLSeconds) * time.Second
	}
	tok, exp, err := s.mintGrant(ttl)
	if err != nil {
		code := http.StatusBadRequest
		if err.Error() == "too many grants" {
			code = http.StatusTooManyRequests
		}
		writeJSON(w, code, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"token":      tok,
		"expiresAt":  exp.UTC().Format(time.RFC3339),
		"ttlSeconds": int(ttl.Seconds()),
	})
}

func (s *Server) handleGatewayRevoke(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.opts.Auth {
		writeJSON(w, http.StatusForbidden, opErrorResponse{Error: "gateway grants require --auth"})
		return
	}
	if !s.rootAuthorized(r) {
		writeJSON(w, http.StatusForbidden, opErrorResponse{Error: "host token required"})
		return
	}
	n := s.revokeAllGrants()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "revoked": n})
}

func (s *Server) handleGatewayRotate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.opts.Auth {
		writeJSON(w, http.StatusForbidden, opErrorResponse{Error: "gateway grants require --auth"})
		return
	}
	if !s.rootAuthorized(r) {
		writeJSON(w, http.StatusForbidden, opErrorResponse{Error: "host token required"})
		return
	}
	path := strings.TrimSpace(s.opts.TokenFile)
	if path == "" {
		writeJSON(w, http.StatusConflict, opErrorResponse{Error: "daemon was not started with --token-file"})
		return
	}
	tok, err := MintToken()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: "could not rotate token"})
		return
	}
	if err := writeDaemonTokenFile(path, tok); err != nil {
		writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: "could not rotate token"})
		return
	}
	s.revokeAllGrants()
	// currentToken observes the new file (grants) and pairing sessions.
	if s.pairs != nil {
		s.pairs.observe(s.currentToken())
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "token": tok})
}

func (s *Server) handleGatewayLogout(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	bearer := bearerToken(r.Header.Get("Authorization"))
	cookie := cookieToken(r)
	revoked := s.revokeGrantValue(bearer) || s.revokeGrantValue(cookie)
	http.SetCookie(w, &http.Cookie{
		Name:     authCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "revokedGrant": revoked})
}
