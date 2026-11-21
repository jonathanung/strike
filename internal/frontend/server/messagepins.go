package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Message pins (#1528) mark individual transcript messages in a session so
// the cockpit can list and jump back to them from any browser on the same
// gateway. They live in message-pins.json beside the JSONL logs, next to
// session-list.json (whole-session pins). Manager.List only reads *.jsonl,
// so this file is not a session.
//
// A pin's anchor is the event ordinal the transcript already trusts for
// fork_at (`keep`: the keep-count at the end of the message's turn), plus
// the message role and its position among same-role prose messages of
// that turn (`seq`). The excerpt is a display fallback and lets the
// client refuse to re-point a pin whose anchor now lands on different
// text. Message ids in the cockpit are per-build counters and are never
// stored.

const (
	messagePinsVersion    = 1
	messagePinsFile       = "message-pins.json"
	messagePinsPerSession = 100
	messagePinExcerptMax  = 160
)

type messagePin struct {
	Keep     int     `json:"keep"`
	Role     string  `json:"role"`
	Seq      int     `json:"seq"`
	Excerpt  string  `json:"excerpt"`
	AtMs     float64 `json:"atMs,omitempty"`
	PinnedAt string  `json:"pinnedAt,omitempty"`
}

type messagePinState struct {
	Version  int                     `json:"version"`
	Sessions map[string][]messagePin `json:"sessions"`
}

type messagePinsResponse struct {
	Pins []messagePin `json:"pins"`
}

var messagePinsMu sync.Mutex

var errMessagePinLimit = fmt.Errorf("a session can hold at most %d message pins", messagePinsPerSession)

func messagePinsPath(dir string) string {
	return filepath.Join(dir, messagePinsFile)
}

func loadMessagePins(dir string) (messagePinState, error) {
	empty := messagePinState{Version: messagePinsVersion, Sessions: map[string][]messagePin{}}
	data, err := os.ReadFile(messagePinsPath(dir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return empty, nil
		}
		return messagePinState{}, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return empty, nil
	}
	var st messagePinState
	if err := json.Unmarshal(data, &st); err != nil {
		return messagePinState{}, fmt.Errorf("message pins: %w", err)
	}
	if st.Sessions == nil {
		st.Sessions = map[string][]messagePin{}
	}
	return st, nil
}

func saveMessagePins(dir string, st messagePinState) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	st.Version = messagePinsVersion
	for id, pins := range st.Sessions {
		if len(pins) == 0 {
			delete(st.Sessions, id)
		}
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	path := messagePinsPath(dir)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// withMessagePins loads, optionally mutates and saves the pin file under
// one lock, returning the session's pins in transcript order.
func withMessagePins(dir, id string, mutate func([]messagePin) ([]messagePin, error)) ([]messagePin, error) {
	messagePinsMu.Lock()
	defer messagePinsMu.Unlock()
	st, err := loadMessagePins(dir)
	if err != nil {
		return nil, err
	}
	pins := st.Sessions[id]
	if mutate != nil {
		had := len(pins) > 0
		pins, err = mutate(append([]messagePin(nil), pins...))
		if err != nil {
			return nil, err
		}
		// Nothing pinned before or after: leave the file untouched.
		if had || len(pins) > 0 {
			sortMessagePins(pins)
			st.Sessions[id] = pins
			if err := saveMessagePins(dir, st); err != nil {
				return nil, err
			}
		}
	}
	out := append([]messagePin{}, pins...)
	sortMessagePins(out)
	return out, nil
}

func sortMessagePins(pins []messagePin) {
	sort.SliceStable(pins, func(i, j int) bool {
		a, b := pins[i], pins[j]
		if a.Keep != b.Keep {
			return a.Keep < b.Keep
		}
		if a.Role != b.Role {
			// A turn's user message precedes its replies.
			return a.Role == "you"
		}
		return a.Seq < b.Seq
	})
}

func sameAnchor(a, b messagePin) bool {
	return a.Keep == b.Keep && a.Role == b.Role && a.Seq == b.Seq
}

func validPinRole(role string) bool {
	return role == "you" || role == "strike"
}

func clipPinExcerpt(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= messagePinExcerptMax {
		return s
	}
	return string([]rune(s)[:messagePinExcerptMax])
}

// normalizeMessagePin validates a pin from the client. Slice 1 pins prose
// only: the user's messages and the agent's replies.
func normalizeMessagePin(p messagePin) (messagePin, error) {
	p.Role = strings.TrimSpace(p.Role)
	if !validPinRole(p.Role) {
		return messagePin{}, errors.New(`role must be "you" or "strike"`)
	}
	if p.Keep < 0 || p.Seq < 0 {
		return messagePin{}, errors.New("keep and seq must be non-negative")
	}
	p.Excerpt = clipPinExcerpt(p.Excerpt)
	if p.Excerpt == "" {
		return messagePin{}, errors.New("excerpt is empty")
	}
	if p.AtMs < 0 {
		p.AtMs = 0
	}
	return p, nil
}

// removeMessagePins drops every pin for a deleted session. Best effort.
func removeMessagePins(dir, id string) error {
	_, err := withMessagePins(dir, id, func([]messagePin) ([]messagePin, error) {
		return nil, nil
	})
	return err
}

func (s *Server) handleMessagePinsList(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if err := validateSessionID(id); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	pins, err := withMessagePins(s.opts.SessionDir, id, nil)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, messagePinsResponse{Pins: pins})
}

// handleMessagePinsAdd pins one message. Pinning the same anchor again
// refreshes its excerpt and time instead of adding a duplicate.
func (s *Server) handleMessagePinsAdd(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if err := validateSessionID(id); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	var body messagePin
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	pin, err := normalizeMessagePin(body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	pin.PinnedAt = time.Now().UTC().Format(time.RFC3339)
	pins, err := withMessagePins(s.opts.SessionDir, id, func(cur []messagePin) ([]messagePin, error) {
		for i := range cur {
			if sameAnchor(cur[i], pin) {
				cur[i] = pin
				return cur, nil
			}
		}
		if len(cur) >= messagePinsPerSession {
			return nil, errMessagePinLimit
		}
		return append(cur, pin), nil
	})
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, errMessagePinLimit) {
			status = http.StatusConflict
		}
		writeJSON(w, status, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, messagePinsResponse{Pins: pins})
}

// handleMessagePinsDelete unpins the anchor named by ?keep=&role=&seq=.
// Removing a pin that is already gone is not an error.
func (s *Server) handleMessagePinsDelete(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if err := validateSessionID(id); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	q := r.URL.Query()
	keep, kerr := strconv.Atoi(q.Get("keep"))
	seq, serr := strconv.Atoi(q.Get("seq"))
	role := strings.TrimSpace(q.Get("role"))
	if kerr != nil || serr != nil || keep < 0 || seq < 0 || !validPinRole(role) {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "keep, role and seq name the pin to remove"})
		return
	}
	target := messagePin{Keep: keep, Role: role, Seq: seq}
	pins, err := withMessagePins(s.opts.SessionDir, id, func(cur []messagePin) ([]messagePin, error) {
		out := cur[:0]
		for _, p := range cur {
			if !sameAnchor(p, target) {
				out = append(out, p)
			}
		}
		return out, nil
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, messagePinsResponse{Pins: pins})
}
