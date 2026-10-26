package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jonathanung/strike/internal/protocol"
)

// SeenStore is the daemon-wide unread ack for one operator.
// A missing id is unread only when its last activity is after Epoch, so
// sessions written before this file existed do not all light up at once.
type SeenStore struct {
	mu    sync.Mutex
	path  string
	epoch int64
	seen  map[string]int64
}

type seenFile struct {
	Epoch int64            `json:"epoch"`
	Seen  map[string]int64 `json:"seen"`
}

// OpenSeenStore loads path, or starts a new store at path on first save.
func OpenSeenStore(path string) *SeenStore {
	s := &SeenStore{
		path:  path,
		epoch: time.Now().UnixMilli(),
		seen:  map[string]int64{},
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	var doc seenFile
	if err := json.Unmarshal(raw, &doc); err != nil || doc.Epoch <= 0 {
		return s
	}
	s.epoch = doc.Epoch
	if doc.Seen != nil {
		s.seen = doc.Seen
	}
	return s
}

// Unread reports whether id has activity the operator has not acked.
// lastActivity is unix milliseconds. A negative seen value forces unread.
func (s *SeenStore) Unread(id string, lastActivity int64) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if seen, ok := s.seen[id]; ok {
		if seen < 0 {
			return true
		}
		return lastActivity > seen
	}
	if lastActivity <= 0 {
		return false
	}
	return lastActivity > s.epoch
}

// MarkSeen records an ack at unix milliseconds (now when at <= 0).
func (s *SeenStore) MarkSeen(id string, at int64) error {
	if s == nil || id == "" {
		return nil
	}
	if at <= 0 {
		at = time.Now().UnixMilli()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen[id] = at
	return s.saveLocked()
}

// MarkUnread forces id unread until the next MarkSeen.
func (s *SeenStore) MarkUnread(id string) error {
	if s == nil || id == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen[id] = -1
	return s.saveLocked()
}

// saveLocked writes the file while mu is held so two acks cannot swap order.
func (s *SeenStore) saveLocked() error {
	doc := seenFile{Epoch: s.epoch, Seen: make(map[string]int64, len(s.seen))}
	for id, at := range s.seen {
		doc.Seen[id] = at
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return writePrivateFile(s.path, raw)
}

func writePrivateFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".status-seen-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	cleanup = false
	return os.Chmod(path, 0o600)
}

func (s *Server) seenStore() *SeenStore {
	s.seenOnce.Do(func() {
		s.seen = OpenSeenStore(filepath.Join(s.opts.SessionDir, "status-seen.json"))
	})
	return s.seen
}

type statusBoard struct {
	Now                 int64                    `json:"now"`
	Gateway             string                   `json:"gateway"`
	HeartbeatIntervalMs int64                    `json:"heartbeatIntervalMs"`
	HeartbeatStaleMs    int64                    `json:"heartbeatStaleMs"`
	Sessions            []protocol.SessionStatus `json:"sessions"`
}

type statusEvent struct {
	Type                string                   `json:"type"`
	At                  int64                    `json:"at"`
	Gateway             string                   `json:"gateway,omitempty"`
	Session             *protocol.SessionStatus  `json:"session,omitempty"`
	Sessions            []protocol.SessionStatus `json:"sessions,omitempty"`
	HeartbeatIntervalMs int64                    `json:"heartbeatIntervalMs,omitempty"`
	HeartbeatStaleMs    int64                    `json:"heartbeatStaleMs,omitempty"`
}

func (s *Server) handleSessionStatusBoard(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.buildStatusBoard(time.Now()))
}

func (s *Server) handleSessionStatusEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	board := s.buildStatusBoard(time.Now())
	if err := writeSSE(w, flusher, statusEvent{
		Type:                "snapshot",
		At:                  board.Now,
		Gateway:             board.Gateway,
		Sessions:            board.Sessions,
		HeartbeatIntervalMs: board.HeartbeatIntervalMs,
		HeartbeatStaleMs:    board.HeartbeatStaleMs,
	}); err != nil {
		return
	}
	lastFP := statusFingerprint(board.Sessions)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	beats := 0
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			beats++
			now := time.Now()
			board = s.buildStatusBoard(now)
			if beats%2 == 0 {
				if err := writeSSE(w, flusher, statusEvent{
					Type:    "heartbeat",
					At:      board.Now,
					Gateway: board.Gateway,
				}); err != nil {
					return
				}
			}
			fp := statusFingerprint(board.Sessions)
			if fp == lastFP {
				continue
			}
			lastFP = fp
			for i := range board.Sessions {
				row := board.Sessions[i]
				if err := writeSSE(w, flusher, statusEvent{
					Type:    "session.status",
					At:      board.Now,
					Gateway: board.Gateway,
					Session: &row,
				}); err != nil {
					return
				}
			}
		}
	}
}

func (s *Server) handleSessionSeen(w http.ResponseWriter, r *http.Request) {
	id, ok := cleanSessionID(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "invalid session id"})
		return
	}
	var body struct {
		At int64 `json:"at"`
	}
	if r.ContentLength != 0 {
		if err := decodeBody(w, r, &body); err != nil {
			writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
			return
		}
	}
	if err := s.seenStore().MarkSeen(id, body.At); err != nil {
		writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: "could not store seen"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sessionId": id, "unread": false})
}

func (s *Server) handleSessionUnseen(w http.ResponseWriter, r *http.Request) {
	id, ok := cleanSessionID(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "invalid session id"})
		return
	}
	if err := s.seenStore().MarkUnread(id); err != nil {
		writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: "could not store unread"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sessionId": id, "unread": true})
}

func cleanSessionID(id string) (string, bool) {
	id = strings.TrimSpace(id)
	if id == "" || strings.ContainsAny(id, "/\\") || strings.Contains(id, "..") {
		return "", false
	}
	return id, true
}

func (s *Server) buildStatusBoard(now time.Time) statusBoard {
	rows := s.liveStatusRows(now)
	have := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		have[row.SessionID] = struct{}{}
	}
	if items, err := listSessionFiles(s.opts.SessionDir); err == nil {
		items = s.decorateSessions(items)
		for _, item := range items {
			if _, ok := have[item.ID]; ok {
				continue
			}
			status := item.Status
			if status == "" {
				status = protocol.StatusIdle
			}
			rows = append(rows, protocol.SessionStatus{
				SessionID:      item.ID,
				Title:          item.Title,
				Status:         status,
				LastActivityAt: item.LastActivityAt,
				Unread:         item.Unread,
			})
		}
	}
	if rows == nil {
		rows = []protocol.SessionStatus{}
	}
	return statusBoard{
		Now:                 now.UnixMilli(),
		Gateway:             protocol.AggregateGateway(rows),
		HeartbeatIntervalMs: protocol.HeartbeatIntervalMs,
		HeartbeatStaleMs:    protocol.HeartbeatStaleMs,
		Sessions:            rows,
	}
}

func (s *Server) liveStatusRows(now time.Time) []protocol.SessionStatus {
	if s.opts.LiveHub != nil {
		roots := s.opts.LiveHub.List()
		s.decorateRoots(roots)
		out := make([]protocol.SessionStatus, 0, len(roots))
		for _, root := range roots {
			out = append(out, rootAsStatus(root, now))
		}
		return out
	}
	if s.opts.Live == nil {
		return nil
	}
	row := liveAsStatus(s.opts.Live, now)
	row.Unread = s.seenStore().Unread(row.SessionID, row.LastActivityAt)
	return []protocol.SessionStatus{row}
}

func (s *Server) decorateRoots(roots []RootSummary) {
	seen := s.seenStore()
	now := time.Now()
	for i := range roots {
		var row protocol.SessionStatus
		if s.opts.LiveHub != nil {
			if live := s.opts.LiveHub.LiveFor(roots[i].ID); live != nil {
				row = liveAsStatus(live, now)
			}
		}
		if row.Status == "" {
			row.Status = protocol.StatusIdle
		}
		roots[i].Status = row.Status
		roots[i].StatusSince = row.Since
		roots[i].LastActivityAt = row.LastActivityAt
		roots[i].Unread = seen.Unread(roots[i].ID, row.LastActivityAt)
	}
}

func (s *Server) decorateSessions(items []sessionListItem) []sessionListItem {
	liveRows := map[string]protocol.SessionStatus{}
	for _, row := range s.liveStatusRows(time.Now()) {
		liveRows[row.SessionID] = row
	}
	seen := s.seenStore()
	for i := range items {
		if live, ok := liveRows[items[i].ID]; ok {
			items[i].Status = live.Status
			items[i].Unread = live.Unread
			if live.LastActivityAt > 0 {
				items[i].LastActivityAt = live.LastActivityAt
			} else if items[i].Mtime > 0 {
				items[i].LastActivityAt = items[i].Mtime * 1000
			}
			continue
		}
		if items[i].Status == "" {
			items[i].Status = protocol.StatusIdle
		}
		if items[i].LastActivityAt == 0 && items[i].Mtime > 0 {
			items[i].LastActivityAt = items[i].Mtime * 1000
		}
		items[i].Unread = seen.Unread(items[i].ID, items[i].LastActivityAt)
	}
	return items
}

func liveAsStatus(live *Live, now time.Time) protocol.SessionStatus {
	snap := live.Status()
	status := snap.TurnState
	if status == "" {
		status = protocol.StatusIdle
	}
	row := protocol.SessionStatus{
		SessionID:      live.SessionID(),
		Status:         status,
		Since:          snap.TurnSince,
		LastActivityAt: snap.LastEventAt,
		Live:           true,
	}
	if (status == protocol.StatusRunning || status == protocol.StatusWaiting) && snap.TurnSince > 0 {
		elapsed := (now.UnixMilli() - snap.TurnSince) / 1000
		if elapsed < 0 {
			elapsed = 0
		}
		row.ElapsedS = elapsed
	}
	return row
}

func rootAsStatus(root RootSummary, now time.Time) protocol.SessionStatus {
	status := root.Status
	if status == "" {
		status = protocol.StatusIdle
	}
	row := protocol.SessionStatus{
		SessionID:      root.ID,
		Title:          root.Title,
		Status:         status,
		Since:          root.StatusSince,
		LastActivityAt: root.LastActivityAt,
		Unread:         root.Unread,
		Live:           true,
	}
	if (status == protocol.StatusRunning || status == protocol.StatusWaiting) && root.StatusSince > 0 {
		elapsed := (now.UnixMilli() - root.StatusSince) / 1000
		if elapsed < 0 {
			elapsed = 0
		}
		row.ElapsedS = elapsed
	}
	return row
}

func statusFingerprint(rows []protocol.SessionStatus) string {
	var b strings.Builder
	for _, row := range rows {
		fmt.Fprintf(&b, "%s\t%s\t%t\t%d\n", row.SessionID, row.Status, row.Unread, row.LastActivityAt)
	}
	return b.String()
}

func writeSSE(w http.ResponseWriter, flusher http.Flusher, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", raw); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}
