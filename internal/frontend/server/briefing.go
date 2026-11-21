package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Away-briefing watermark (#1488). The cockpit's "While you were away"
// panel groups daemon records newer than this time. The daemon owns it so
// "Mark caught up" on one device is caught up on every device paired to
// the gateway (the multi-device finding of #1392); a per-browser
// localStorage mark would re-show the same briefing on each device.
//
// The watermark is unix milliseconds on the daemon clock, moves forward
// only, and is clamped to the daemon's now so a fast client clock cannot
// hide future records. Zero means "never seen": the cockpit initialises it
// silently instead of briefing the whole history.

const briefingSeenLimit = 1 << 10

// BriefingSeen is the GET/POST /v1/briefing/seen body. Now is the daemon
// clock so the client measures the away gap without trusting its own.
type BriefingSeen struct {
	SeenAt int64 `json:"seenAt"`
	Now    int64 `json:"now"`
}

type briefingSeenFile struct {
	SeenAt int64 `json:"seenAt"`
}

type briefingSeenRequest struct {
	SeenAt int64 `json:"seenAt"`
}

type briefingStore struct {
	mu     sync.Mutex
	path   string
	seenAt int64
	now    func() time.Time
}

func (s *Server) briefing() *briefingStore {
	s.briefingOnce.Do(func() {
		s.briefingStore = openBriefingStore(filepath.Join(s.opts.SessionDir, "notifications", "briefing.json"))
	})
	return s.briefingStore
}

func openBriefingStore(path string) *briefingStore {
	b := &briefingStore{path: path, now: time.Now}
	raw, err := os.ReadFile(path)
	if err != nil {
		return b
	}
	var st briefingSeenFile
	if json.Unmarshal(raw, &st) == nil && st.SeenAt > 0 {
		b.seenAt = st.SeenAt
	}
	return b
}

func (b *briefingStore) snapshot() BriefingSeen {
	b.mu.Lock()
	defer b.mu.Unlock()
	return BriefingSeen{SeenAt: b.seenAt, Now: b.now().UnixMilli()}
}

// advance moves the watermark forward to at (clamped to now). An older or
// equal value is a no-op, so two devices racing never move it backwards.
func (b *briefingStore) advance(at int64) (BriefingSeen, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now().UnixMilli()
	if at > now {
		at = now
	}
	if at > b.seenAt {
		dir := filepath.Dir(b.path)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return BriefingSeen{}, err
		}
		_ = os.Chmod(dir, 0o700)
		raw, err := json.Marshal(briefingSeenFile{SeenAt: at})
		if err != nil {
			return BriefingSeen{}, err
		}
		if err := writeAtomic0600(b.path, raw); err != nil {
			return BriefingSeen{}, err
		}
		b.seenAt = at
	}
	return BriefingSeen{SeenAt: b.seenAt, Now: now}, nil
}

// handleBriefingSeen returns the shared watermark (GET /v1/briefing/seen).
func (s *Server) handleBriefingSeen(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.briefing().snapshot())
}

// handleBriefingSeenSet advances the watermark (POST /v1/briefing/seen,
// body {"seenAt": unixMs}).
func (s *Server) handleBriefingSeenSet(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, briefingSeenLimit)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var in briefingSeenRequest
	if err := dec.Decode(&in); err != nil {
		http.Error(w, "invalid briefing watermark", http.StatusBadRequest)
		return
	}
	if err := ensureEOF(dec); err != nil {
		http.Error(w, "invalid briefing watermark", http.StatusBadRequest)
		return
	}
	if in.SeenAt <= 0 {
		http.Error(w, "invalid briefing watermark", http.StatusBadRequest)
		return
	}
	out, err := s.briefing().advance(in.SeenAt)
	if err != nil {
		http.Error(w, "briefing watermark unavailable", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
