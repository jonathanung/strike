package server

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jonathanung/strike/internal/persist/session"
)

// Session share links (#1565). A share grant lets a browser with no gateway
// credential read one session's transcript, read-only:
//
//   - The token is high-entropy, shown once at mint, and stored only as a
//     SHA-256 hash (shares.json, 0600).
//   - It is never a gateway credential: authorized() does not know it, so
//     every /v1 route refuses it. The only route that accepts it is
//     POST /share/v1/transcript, which takes it in the JSON body and reads
//     exactly the session bound at mint time. There is no session parameter.
//   - It travels in the URL fragment (#share=...), which browsers never send
//     to a server or put in a Referer header.
//   - Grants expire (default 7 days, max 30) and are revocable. Mint, revoke,
//     and every redeem attempt are audit-logged without the token.
//   - Expired, revoked, unknown tokens and deleted sessions get one identical
//     answer.

const (
	shareTokenPrefix   = "sh1."
	shareTokenBytes    = 32
	shareTokenMaxLen   = 80
	shareDefaultTTL    = 7 * 24 * time.Hour
	shareMinTTL        = time.Minute
	shareMaxTTL        = 30 * 24 * time.Hour
	shareMaxPerSession = 32
	shareMaxTotal      = 512
	shareBodyLimit     = 4096
	shareChunkBytes    = 2 << 20
	shareRedeemPerMin  = 120
	shareUnavailable   = "share_unavailable"
)

type shareGrant struct {
	ID        string    `json:"id"`
	SessionID string    `json:"sessionId"`
	Label     string    `json:"label"`
	Hash      string    `json:"hash"`
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// ShareView is one live grant as the owner sees it. The token and its hash
// are never included.
type ShareView struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	CreatedAt string `json:"createdAt"`
	ExpiresAt string `json:"expiresAt"`
}

type shareMintResponse struct {
	ShareView
	// Token is returned once. Path is the fragment-carried link to open.
	Token string `json:"token"`
	Path  string `json:"path"`
}

type shareTranscriptResponse struct {
	Title  string            `json:"title"`
	Events []json.RawMessage `json:"events"`
	// Next is the byte offset to pass as "after" on the next poll.
	Next int64 `json:"next"`
	// More is true when this chunk stopped before the end of the log.
	More bool `json:"more"`
}

type shareStore struct {
	mu     sync.Mutex
	path   string
	clock  func() time.Time
	grants []shareGrant
	loaded bool
	redeem *hitWindow
}

func newShareStore(path string, clock func() time.Time) *shareStore {
	if clock == nil {
		clock = time.Now
	}
	return &shareStore{
		path:   path,
		clock:  clock,
		redeem: newHitWindow(shareRedeemPerMin, time.Minute, clock),
	}
}

func (s *Server) shares() *shareStore {
	s.shareOnce.Do(func() {
		if strings.TrimSpace(s.opts.SessionDir) == "" {
			return
		}
		s.shareStore = newShareStore(filepath.Join(s.opts.SessionDir, "shares.json"), nil)
	})
	return s.shareStore
}

func (st *shareStore) loadLocked() {
	if st.loaded {
		return
	}
	st.loaded = true
	b, err := os.ReadFile(st.path)
	if err != nil {
		return
	}
	var grants []shareGrant
	if json.Unmarshal(b, &grants) != nil {
		return
	}
	for _, g := range grants {
		if g.ID == "" || g.SessionID == "" || len(g.Hash) != sha256.Size*2 {
			continue
		}
		st.grants = append(st.grants, g)
	}
}

func (st *shareStore) persistLocked() error {
	if err := os.MkdirAll(filepath.Dir(st.path), 0o700); err != nil {
		return err
	}
	grants := st.grants
	if grants == nil {
		grants = []shareGrant{}
	}
	b, err := json.Marshal(grants)
	if err != nil {
		return err
	}
	return writeAtomic0600(st.path, b)
}

// pruneLocked drops expired grants. It reports whether anything changed.
func (st *shareStore) pruneLocked(now time.Time) bool {
	kept := st.grants[:0]
	changed := false
	for _, g := range st.grants {
		if !now.Before(g.ExpiresAt) {
			changed = true
			continue
		}
		kept = append(kept, g)
	}
	st.grants = kept
	return changed
}

var errShareLimit = errors.New("too many share links")

func (st *shareStore) mint(sessionID, label string, ttl time.Duration) (string, shareGrant, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.loadLocked()
	now := st.clock()
	st.pruneLocked(now)
	perSession := 0
	for _, g := range st.grants {
		if g.SessionID == sessionID {
			perSession++
		}
	}
	if perSession >= shareMaxPerSession || len(st.grants) >= shareMaxTotal {
		return "", shareGrant{}, errShareLimit
	}
	token, sum, err := newSecret(shareTokenPrefix, shareTokenBytes)
	if err != nil {
		return "", shareGrant{}, err
	}
	id, err := newID("shr_")
	if err != nil {
		return "", shareGrant{}, err
	}
	g := shareGrant{
		ID:        id,
		SessionID: sessionID,
		Label:     cleanShareLabel(label, now),
		Hash:      hex.EncodeToString(sum[:]),
		CreatedAt: now.UTC(),
		ExpiresAt: now.Add(ttl).UTC(),
	}
	st.grants = append(st.grants, g)
	if err := st.persistLocked(); err != nil {
		st.grants = st.grants[:len(st.grants)-1]
		return "", shareGrant{}, err
	}
	return token, g, nil
}

func (st *shareStore) list(sessionID string) []ShareView {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.loadLocked()
	if st.pruneLocked(st.clock()) {
		_ = st.persistLocked()
	}
	out := []ShareView{}
	for _, g := range st.grants {
		if g.SessionID == sessionID {
			out = append(out, viewOf(g))
		}
	}
	return out
}

func (st *shareStore) revoke(sessionID, id string) (bool, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.loadLocked()
	for i, g := range st.grants {
		if g.ID == id && g.SessionID == sessionID {
			st.grants = append(st.grants[:i], st.grants[i+1:]...)
			return true, st.persistLocked()
		}
	}
	return false, nil
}

// resolve maps a presented token to its session. The hash scan is constant
// time over all live grants.
func (st *shareStore) resolve(token string) (string, bool) {
	token = strings.TrimSpace(token)
	if token == "" || len(token) > shareTokenMaxLen || !strings.HasPrefix(token, shareTokenPrefix) {
		return "", false
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	st.loadLocked()
	if st.pruneLocked(st.clock()) {
		_ = st.persistLocked()
	}
	hashes := make([][32]byte, 0, len(st.grants))
	for _, g := range st.grants {
		var h [32]byte
		if raw, err := hex.DecodeString(g.Hash); err == nil && len(raw) == len(h) {
			copy(h[:], raw)
		}
		hashes = append(hashes, h)
	}
	idx, ok := matchHash(token, hashes)
	if !ok {
		return "", false
	}
	return st.grants[idx].SessionID, true
}

func viewOf(g shareGrant) ShareView {
	return ShareView{
		ID:        g.ID,
		Label:     g.Label,
		CreatedAt: g.CreatedAt.UTC().Format(time.RFC3339),
		ExpiresAt: g.ExpiresAt.UTC().Format(time.RFC3339),
	}
}

func cleanShareLabel(label string, now time.Time) string {
	if clean := safeLabel(label, 64); clean != "" {
		return clean
	}
	return "Created " + now.UTC().Format("2006-01-02 15:04 UTC")
}

func shareTTL(seconds int64) (time.Duration, bool) {
	if seconds == 0 {
		return shareDefaultTTL, true
	}
	ttl := time.Duration(seconds) * time.Second
	if seconds < 0 || ttl < shareMinTTL || ttl > shareMaxTTL {
		return 0, false
	}
	return ttl, true
}

// shareSessionID validates the path id and confirms the session log exists.
func (s *Server) shareSessionID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := strings.TrimSpace(r.PathValue("id"))
	if err := validateSessionID(id); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "invalid session id"})
		return "", false
	}
	if _, err := os.Stat(session.LogPath(s.opts.SessionDir, id)); err != nil {
		writeJSON(w, http.StatusNotFound, opErrorResponse{Error: "session not found"})
		return "", false
	}
	return id, true
}

// handleShareMint creates a share link (POST /v1/sessions/{id}/shares).
func (s *Server) handleShareMint(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	st := s.shares()
	if st == nil {
		writeJSON(w, http.StatusServiceUnavailable, opErrorResponse{Error: "sharing unavailable"})
		return
	}
	id, ok := s.shareSessionID(w, r)
	if !ok {
		return
	}
	var body struct {
		Label      string `json:"label"`
		TTLSeconds int64  `json:"ttlSeconds"`
	}
	if r.ContentLength != 0 {
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, shareBodyLimit))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&body); err != nil && !errors.Is(err, io.EOF) {
			writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "invalid share request"})
			return
		}
	}
	ttl, ok := shareTTL(body.TTLSeconds)
	if !ok {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "expiry must be between 1 minute and 30 days"})
		return
	}
	token, g, err := st.mint(id, body.Label, ttl)
	if err != nil {
		if errors.Is(err, errShareLimit) {
			writeJSON(w, http.StatusTooManyRequests, opErrorResponse{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: "could not create share link"})
		return
	}
	s.recordServeOp(id, "share.mint", requestIP(r), "http", opOutcomeOK)
	writeJSON(w, http.StatusCreated, shareMintResponse{
		ShareView: viewOf(g),
		Token:     token,
		Path:      "/#share=" + token,
	})
}

// handleShareList lists a session's live share links.
func (s *Server) handleShareList(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	st := s.shares()
	if st == nil {
		writeJSON(w, http.StatusOK, map[string]any{"shares": []ShareView{}})
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if err := validateSessionID(id); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "invalid session id"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"shares": st.list(id)})
}

// handleShareRevoke revokes one share link.
func (s *Server) handleShareRevoke(w http.ResponseWriter, r *http.Request) {
	st := s.shares()
	id := strings.TrimSpace(r.PathValue("id"))
	shareID := strings.TrimSpace(r.PathValue("shareId"))
	if err := validateSessionID(id); err != nil || shareID == "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "invalid share"})
		return
	}
	if st == nil {
		writeJSON(w, http.StatusNotFound, opErrorResponse{Error: "share not found"})
		return
	}
	found, err := st.revoke(id, shareID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: "could not revoke share link"})
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, opErrorResponse{Error: "share not found"})
		return
	}
	s.recordServeOp(id, "share.revoke", requestIP(r), "http", opOutcomeOK)
	w.WriteHeader(http.StatusNoContent)
}

// handleShareTranscript is the only route a share token opens
// (POST /share/v1/transcript, body {"token","after"}). It is outside /v1, so
// gateway auth does not apply; the token in the body is the credential and
// it reads exactly the session bound at mint.
func (s *Server) handleShareTranscript(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	ip := requestIP(r)
	st := s.shares()
	if st == nil || !st.redeem.allow(ip) {
		if st != nil {
			s.recordServeOp("", "share.redeem", ip, "http", opOutcomeRateLimited)
			writePairRateLimited(w)
			return
		}
		writeShareUnavailable(w)
		return
	}
	var body struct {
		Token string `json:"token"`
		After int64  `json:"after"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, shareBodyLimit))
	dec.DisallowUnknownFields()
	// A token in the query string is refused like an unknown one: it would
	// sit in logs and history.
	if len(r.URL.RawQuery) > 0 || dec.Decode(&body) != nil || body.After < 0 {
		s.recordServeOp("", "share.redeem", ip, "http", "denied")
		writeShareUnavailable(w)
		return
	}
	id, ok := st.resolve(body.Token)
	if !ok {
		s.recordServeOp("", "share.redeem", ip, "http", "denied")
		writeShareUnavailable(w)
		return
	}
	// Deleting the session kills the share: the record is gone.
	path := session.LogPath(s.opts.SessionDir, id)
	events, next, more, err := readShareChunk(path, body.After, shareChunkBytes)
	if err != nil {
		s.recordServeOp(id, "share.redeem", ip, "http", "denied")
		writeShareUnavailable(w)
		return
	}
	// Audit the first read of a link, not every poll.
	if body.After == 0 {
		s.recordServeOp(id, "share.redeem", ip, "http", opOutcomeOK)
	}
	title := ""
	if meta, err := session.ReadMeta(s.opts.SessionDir, id); err == nil {
		title = safeLabel(meta.Title, 120)
	}
	writeJSON(w, http.StatusOK, shareTranscriptResponse{Title: title, Events: events, Next: next, More: more})
}

func writeShareUnavailable(w http.ResponseWriter) {
	writeJSON(w, http.StatusNotFound, map[string]string{"error": shareUnavailable})
}

// readShareChunk returns complete event lines from offset, up to about max
// bytes, skipping the session header and malformed lines (same rules as the
// cockpit's event stream).
func readShareChunk(path string, offset int64, max int) ([]json.RawMessage, int64, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, offset, false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, offset, false, err
	}
	if offset > info.Size() {
		// Truncated or rewritten log: start again.
		offset = 0
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, offset, false, err
	}
	events := []json.RawMessage{}
	reader := bufio.NewReaderSize(io.LimitReader(f, info.Size()-offset), 64*1024)
	read := 0
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) == 0 || line[len(line)-1] != '\n' {
			return events, offset, false, nil
		}
		offset += int64(len(line))
		read += len(line)
		payload := bytes.TrimSpace(line)
		if len(payload) > 0 && json.Valid(payload) && !isSessionLogHeader(payload) {
			events = append(events, json.RawMessage(append([]byte(nil), payload...)))
		}
		if read >= max {
			return events, offset, offset < info.Size(), nil
		}
		if err != nil {
			return events, offset, false, nil
		}
	}
}
