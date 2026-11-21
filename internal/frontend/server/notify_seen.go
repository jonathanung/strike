package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
)

// Notification read state (#1392). The daemon owns it so reading on one
// browser clears the badge on every browser paired to the gateway.
//
// A row is read when its seq is at or below UpTo (set by "Mark all read")
// or its id is in IDs (set by opening one row). IDs is pruned to the
// rows still in the centre's window, so it is bounded by notifyMaxItems.

const notifySeenLimit = 16 << 10

// NotifySeen is the shared read state for the notification centre.
type NotifySeen struct {
	UpTo int64    `json:"upTo"`
	IDs  []string `json:"ids"`
}

type notifySeenRequest struct {
	UpTo int64    `json:"upTo"`
	IDs  []string `json:"ids"`
}

func (n *notifyLog) loadSeen() {
	b, err := os.ReadFile(filepath.Join(n.dir, "seen.json"))
	if err != nil {
		return
	}
	var st NotifySeen
	if json.Unmarshal(b, &st) != nil {
		return
	}
	if st.UpTo < 0 {
		st.UpTo = 0
	}
	n.seen = st
	n.clampSeenLocked()
}

// clampSeenLocked drops ids no longer in the window and caps UpTo at the
// last issued seq, so a reset log (seq restarting at 1) is not read
// wholesale by a stale watermark.
func (n *notifyLog) clampSeenLocked() {
	if last := n.state.NextSeq - 1; n.seen.UpTo > last {
		if last < 0 {
			last = 0
		}
		n.seen.UpTo = last
	}
	kept := make([]string, 0, len(n.seen.IDs))
	dup := map[string]struct{}{}
	for _, id := range n.seen.IDs {
		if _, ok := n.ids[id]; !ok {
			continue
		}
		if _, ok := dup[id]; ok {
			continue
		}
		dup[id] = struct{}{}
		kept = append(kept, id)
	}
	sort.Strings(kept)
	n.seen.IDs = kept
}

// seenSnapshotLocked returns a copy with ids already covered by UpTo
// removed, so clients get the smallest equivalent set.
func (n *notifyLog) seenSnapshotLocked() NotifySeen {
	n.clampSeenLocked()
	covered := map[string]struct{}{}
	for _, item := range n.items {
		if item.Seq <= n.seen.UpTo {
			covered[item.ID] = struct{}{}
		}
	}
	out := NotifySeen{UpTo: n.seen.UpTo, IDs: make([]string, 0, len(n.seen.IDs))}
	for _, id := range n.seen.IDs {
		if _, ok := covered[id]; !ok {
			out.IDs = append(out.IDs, id)
		}
	}
	return out
}

// markSeen merges a read request. Reads are monotonic: UpTo never moves
// backwards and ids are only added. Unknown ids (rows outside the window,
// page-local rows like gateway.disconnected) are ignored rather than
// stored, which bounds the file and keeps it to daemon-issued ids.
func (n *notifyLog) markSeen(req notifySeenRequest) (NotifySeen, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if req.UpTo > n.seen.UpTo {
		n.seen.UpTo = req.UpTo
	}
	n.seen.IDs = append(n.seen.IDs, req.IDs...)
	n.clampSeenLocked()
	if err := n.persistSeenLocked(); err != nil {
		return NotifySeen{}, err
	}
	return n.seenSnapshotLocked(), nil
}

func (n *notifyLog) persistSeenLocked() error {
	if err := os.MkdirAll(n.dir, 0o700); err != nil {
		return err
	}
	_ = os.Chmod(n.dir, 0o700)
	st := n.seen
	if st.IDs == nil {
		st.IDs = []string{}
	}
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return writeAtomic0600(filepath.Join(n.dir, "seen.json"), b)
}

func normalizeSeenRequest(in notifySeenRequest) (notifySeenRequest, error) {
	if in.UpTo < 0 {
		return notifySeenRequest{}, errors.New("invalid upTo")
	}
	if len(in.IDs) > notifyMaxItems {
		return notifySeenRequest{}, errors.New("too many ids")
	}
	out := notifySeenRequest{UpTo: in.UpTo, IDs: make([]string, 0, len(in.IDs))}
	for _, id := range in.IDs {
		safe, ok := safeID(id)
		if !ok {
			return notifySeenRequest{}, errors.New("invalid id")
		}
		out.IDs = append(out.IDs, safe)
	}
	if out.UpTo == 0 && len(out.IDs) == 0 {
		return notifySeenRequest{}, errors.New("nothing to mark")
	}
	return out, nil
}

// handleNotificationSeen marks centre rows read for every browser
// (POST /v1/notifications/seen). Body: {"ids":[...]} for single rows,
// {"upTo":seq} for "Mark all read" up to the newest row the client showed.
func (s *Server) handleNotificationSeen(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, notifySeenLimit)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var in notifySeenRequest
	if err := dec.Decode(&in); err != nil {
		http.Error(w, "invalid read state", http.StatusBadRequest)
		return
	}
	if err := ensureEOF(dec); err != nil {
		http.Error(w, "invalid read state", http.StatusBadRequest)
		return
	}
	req, err := normalizeSeenRequest(in)
	if err != nil {
		http.Error(w, "invalid read state", http.StatusBadRequest)
		return
	}
	log := s.notifications()
	if err := log.sync(); err != nil {
		http.Error(w, "notifications unavailable", http.StatusInternalServerError)
		return
	}
	seen, err := log.markSeen(req)
	if err != nil {
		http.Error(w, "read state unavailable", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, seen)
}
