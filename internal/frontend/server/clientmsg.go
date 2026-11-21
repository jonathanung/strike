package server

import (
	"net/http"
	"strings"
	"sync"
	"time"
)

// Client message ids (#1325). The web outbox queues composer sends while the
// gateway is unreachable and retries them on reconnect. A retry after a
// half-open connection (the daemon took the op, the answer was lost) must not
// post the prompt twice, so every send carries an id and the daemon remembers
// the ids it delivered, per live root.
//
// Memory only: a daemon restart forgets the ids. The outbox only retries
// sends whose answer it never saw, so the window is one restart racing one
// lost response.

const (
	clientMessageHeader = "X-Strike-Client-Message-Id"
	clientMessageMaxLen = 128
	clientMessageTTL    = 24 * time.Hour
	clientMessageMax    = 4096
)

type clientMsgState int

const (
	clientMsgNew clientMsgState = iota
	clientMsgInFlight
	clientMsgDelivered
)

type clientMsgEntry struct {
	delivered bool
	at        time.Time
}

type clientMsgDedupe struct {
	mu      sync.Mutex
	now     func() time.Time
	entries map[string]clientMsgEntry
}

func newClientMsgDedupe() *clientMsgDedupe {
	return &clientMsgDedupe{now: time.Now, entries: map[string]clientMsgEntry{}}
}

func (s *Server) clientMsgs() *clientMsgDedupe {
	s.clientMsgOnce.Do(func() { s.clientMsgStore = newClientMsgDedupe() })
	return s.clientMsgStore
}

// clientMessageID reads the optional header. ok is false when the header is
// present but malformed; an absent header returns ("", true).
func clientMessageID(r *http.Request) (string, bool) {
	raw := strings.TrimSpace(r.Header.Get(clientMessageHeader))
	if raw == "" {
		return "", true
	}
	if len(raw) > clientMessageMaxLen {
		return "", false
	}
	for _, c := range raw {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == '.', c == ':':
		default:
			return "", false
		}
	}
	return raw, true
}

func clientMsgKey(sessionID, id string) string { return sessionID + "\x00" + id }

// reserve claims id for one delivery attempt. clientMsgNew means the caller
// owns the attempt and must call finish.
func (d *clientMsgDedupe) reserve(sessionID, id string) clientMsgState {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	key := clientMsgKey(sessionID, id)
	if e, ok := d.entries[key]; ok && now.Sub(e.at) < clientMessageTTL {
		if e.delivered {
			return clientMsgDelivered
		}
		return clientMsgInFlight
	}
	d.pruneLocked(now)
	d.entries[key] = clientMsgEntry{at: now}
	return clientMsgNew
}

// finish records the outcome of a reserved attempt. A failed attempt is
// forgotten so the client may retry with the same id.
func (d *clientMsgDedupe) finish(sessionID, id string, delivered bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	key := clientMsgKey(sessionID, id)
	if !delivered {
		delete(d.entries, key)
		return
	}
	d.entries[key] = clientMsgEntry{delivered: true, at: d.now()}
}

// pruneLocked drops expired entries, then the oldest delivered ones while
// over the cap. In-flight entries are never evicted.
func (d *clientMsgDedupe) pruneLocked(now time.Time) {
	for k, e := range d.entries {
		if now.Sub(e.at) >= clientMessageTTL {
			delete(d.entries, k)
		}
	}
	for len(d.entries) >= clientMessageMax {
		oldestKey := ""
		var oldest time.Time
		for k, e := range d.entries {
			if !e.delivered {
				continue
			}
			if oldestKey == "" || e.at.Before(oldest) {
				oldestKey, oldest = k, e.at
			}
		}
		if oldestKey == "" {
			return
		}
		delete(d.entries, oldestKey)
	}
}
