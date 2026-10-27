package server

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jonathanung/strike/internal/persist/session"
	"github.com/jonathanung/strike/pkg/redact"
)

// Notification kinds the centre and the browser banner share.
// gateway.disconnected is page-local: the daemon cannot deliver it.
const (
	noteApproval        = "approval.needed"
	noteChildFinished   = "child.finished"
	noteChildBlocked    = "child.blocked"
	noteSessionFinished = "session.finished"
)

const (
	notifyMaxItems   = 200
	notifyMaxID      = 128
	notifyMaxMuted   = 200
	notifyPrefsLimit = 8 << 10
	notifyTitleRunes = 80
	notifyBodyRunes  = 140
)

// Notification is a redacted centre record. Patterns, tool output, handoff
// files, and verification reports are never fields on this struct.
type Notification struct {
	ID        string    `json:"id"`
	Seq       int64     `json:"seq"`
	Kind      string    `json:"kind"`
	SessionID string    `json:"sessionId"`
	SubjectID string    `json:"subjectId,omitempty"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	Href      string    `json:"href"`
	RequestID string    `json:"requestId,omitempty"`
	At        time.Time `json:"at"`
	// Banner is computed at read time from prefs. It is not a stored decision.
	Banner bool `json:"banner,omitempty"`
}

// QuietHours is a local-time window on the daemon host. Start inclusive,
// end exclusive. A window that passes midnight (22:00–07:00) wraps.
type QuietHours struct {
	Enabled bool   `json:"enabled"`
	Start   string `json:"start"`
	End     string `json:"end"`
}

// NotifyPrefs is daemon-wide banner policy. Mute and quiet hours suppress
// banners; they do not drop the record.
type NotifyPrefs struct {
	Enabled bool       `json:"enabled"`
	Quiet   QuietHours `json:"quiet"`
	Muted   []string   `json:"mutedSessions"`
}

type notificationsResponse struct {
	Notifications []Notification `json:"notifications"`
	Prefs         NotifyPrefs    `json:"prefs"`
	Latest        int64          `json:"latest"`
}

type notifyState struct {
	Ready   bool             `json:"ready"`
	Cursors map[string]int64 `json:"cursors"`
	NextSeq int64            `json:"nextSeq"`
}

type notifyLog struct {
	mu         sync.Mutex
	dir        string
	sessionDir string
	state      notifyState
	prefs      NotifyPrefs
	items      []Notification
	ids        map[string]struct{}
	now        func() time.Time
}

func defaultNotifyPrefs() NotifyPrefs {
	return NotifyPrefs{
		Enabled: true,
		Quiet:   QuietHours{Enabled: false, Start: "22:00", End: "07:00"},
		Muted:   []string{},
	}
}

func (s *Server) notifications() *notifyLog {
	s.notifyOnce.Do(func() {
		s.notifyLog = openNotifyLog(filepath.Join(s.opts.SessionDir, "notifications"), s.opts.SessionDir)
	})
	return s.notifyLog
}

func openNotifyLog(dir, sessionDir string) *notifyLog {
	n := &notifyLog{
		dir:        dir,
		sessionDir: sessionDir,
		prefs:      defaultNotifyPrefs(),
		ids:        map[string]struct{}{},
		now:        time.Now,
		state:      notifyState{Cursors: map[string]int64{}},
	}
	if err := os.MkdirAll(dir, 0o700); err == nil {
		_ = os.Chmod(dir, 0o700)
	}
	n.load()
	return n
}

func (n *notifyLog) load() {
	if b, err := os.ReadFile(filepath.Join(n.dir, "prefs.json")); err == nil {
		var prefs NotifyPrefs
		if json.Unmarshal(b, &prefs) == nil {
			if norm, err := normalizePrefs(prefs); err == nil {
				n.prefs = norm
			}
		}
	}
	if b, err := os.ReadFile(filepath.Join(n.dir, "state.json")); err == nil {
		var st notifyState
		if json.Unmarshal(b, &st) == nil && st.Cursors != nil {
			n.state = st
		}
	}
	if n.state.Cursors == nil {
		n.state.Cursors = map[string]int64{}
	}
	f, err := os.Open(filepath.Join(n.dir, "log.jsonl"))
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	var maxSeq int64
	for sc.Scan() {
		var item Notification
		if err := json.Unmarshal(sc.Bytes(), &item); err != nil || item.ID == "" || item.Seq <= 0 {
			continue
		}
		if _, ok := n.ids[item.ID]; ok {
			continue
		}
		item.Banner = false
		n.ids[item.ID] = struct{}{}
		n.items = append(n.items, item)
		if item.Seq > maxSeq {
			maxSeq = item.Seq
		}
	}
	if n.state.NextSeq < maxSeq+1 {
		n.state.NextSeq = maxSeq + 1
	}
	n.trimLocked()
}

func (s *Server) handleNotifications(w http.ResponseWriter, r *http.Request) {
	since, err := parseSince(r)
	if err != nil {
		http.Error(w, "invalid since", http.StatusBadRequest)
		return
	}
	log := s.notifications()
	if err := log.sync(); err != nil {
		http.Error(w, "notifications unavailable", http.StatusInternalServerError)
		return
	}
	items, prefs, latest := log.snapshot(since)
	writeJSON(w, http.StatusOK, notificationsResponse{
		Notifications: items,
		Prefs:         prefs,
		Latest:        latest,
	})
}

func (s *Server) handleNotificationPrefs(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, notifyPrefsLimit)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var in NotifyPrefs
	if err := dec.Decode(&in); err != nil {
		http.Error(w, "invalid notification settings", http.StatusBadRequest)
		return
	}
	if err := ensureEOF(dec); err != nil {
		http.Error(w, "invalid notification settings", http.StatusBadRequest)
		return
	}
	prefs, err := normalizePrefs(in)
	if err != nil {
		http.Error(w, "invalid notification settings", http.StatusBadRequest)
		return
	}
	if err := s.notifications().savePrefs(prefs); err != nil {
		http.Error(w, "notification settings unavailable", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, prefs)
}

func (s *Server) handleNotificationStream(w http.ResponseWriter, r *http.Request) {
	since, err := parseSince(r)
	if err != nil {
		http.Error(w, "invalid since", http.StatusBadRequest)
		return
	}
	if raw := strings.TrimSpace(r.Header.Get("Last-Event-ID")); raw != "" {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil && n > since {
			since = n
		}
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	log := s.notifications()
	if err := log.sync(); err != nil {
		http.Error(w, "notifications unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	sent := since
	writeNew := func() error {
		if err := log.sync(); err != nil {
			return err
		}
		items, _, _ := log.snapshot(sent)
		for _, item := range items {
			if err := writeNotifySSE(w, flusher, item); err != nil {
				return err
			}
			if item.Seq > sent {
				sent = item.Seq
			}
		}
		return nil
	}
	if err := writeNew(); err != nil {
		return
	}
	ticker := time.NewTicker(s.opts.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if err := writeNew(); err != nil {
				return
			}
		}
	}
}

func writeNotifySSE(w http.ResponseWriter, flusher http.Flusher, item Notification) error {
	payload, err := json.Marshal(item)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "id: %d\ndata: %s\n\n", item.Seq, payload); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func parseSince(r *http.Request) (int64, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("since"))
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return 0, errors.New("invalid since")
	}
	return n, nil
}

func (n *notifyLog) sync() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.syncLocked()
}

func (n *notifyLog) syncLocked() error {
	entries, err := os.ReadDir(n.sessionDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if !n.state.Ready {
		for _, ent := range entries {
			id, size, ok := sessionJSONL(n.sessionDir, ent)
			if !ok {
				continue
			}
			n.state.Cursors[id] = size
		}
		n.state.Ready = true
		return n.persistLocked()
	}
	changed := false
	for _, ent := range entries {
		id, size, ok := sessionJSONL(n.sessionDir, ent)
		if !ok {
			continue
		}
		cur, seen := n.state.Cursors[id]
		if !seen || size < cur {
			cur = 0
		}
		if seen && size == cur {
			continue
		}
		next, added, err := n.scanFileLocked(id, cur)
		if err != nil {
			return err
		}
		n.state.Cursors[id] = next
		if !seen || next != cur || added {
			changed = true
		}
	}
	if changed {
		return n.persistLocked()
	}
	return nil
}

func sessionJSONL(dir string, ent os.DirEntry) (id string, size int64, ok bool) {
	if ent.IsDir() {
		return "", 0, false
	}
	name := ent.Name()
	if !strings.HasSuffix(name, ".jsonl") {
		return "", 0, false
	}
	id = strings.TrimSuffix(name, ".jsonl")
	if _, good := safeID(id); !good {
		return "", 0, false
	}
	info, err := ent.Info()
	if err != nil {
		return "", 0, false
	}
	return id, info.Size(), true
}

func (n *notifyLog) scanFileLocked(id string, cursor int64) (int64, bool, error) {
	path := session.LogPath(n.sessionDir, id)
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cursor, false, nil
		}
		return cursor, false, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return cursor, false, err
	}
	if st.Size() < cursor {
		cursor = 0
	}
	if _, err := f.Seek(cursor, io.SeekStart); err != nil {
		return cursor, false, err
	}
	meta, err := session.ReadMeta(n.sessionDir, id)
	fileParent := ""
	if err == nil {
		if parent, ok := safeID(meta.ParentSessionID); ok {
			fileParent = parent
		}
	}
	reader := bufio.NewReaderSize(f, 64*1024)
	added := false
	offset := cursor
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			offset += int64(len(line))
			if item, ok := projectLine(id, fileParent, line, n.now()); ok {
				if n.appendLocked(item) {
					added = true
				}
			}
		} else {
			break
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return offset, added, err
		}
	}
	return offset, added, nil
}

func (n *notifyLog) appendLocked(item Notification) bool {
	if item.ID == "" {
		return false
	}
	if _, ok := n.ids[item.ID]; ok {
		return false
	}
	if n.state.NextSeq < 1 {
		n.state.NextSeq = 1
	}
	item.Seq = n.state.NextSeq
	n.state.NextSeq++
	item.Banner = false
	item.Title = safeLabel(item.Title, notifyTitleRunes)
	item.Body = safeLabel(item.Body, notifyBodyRunes)
	if item.Title == "" {
		item.Title = fallbackTitle(item.Kind)
	}
	n.ids[item.ID] = struct{}{}
	n.items = append(n.items, item)
	n.trimLocked()
	return true
}

func (n *notifyLog) trimLocked() {
	if len(n.items) <= notifyMaxItems {
		return
	}
	drop := len(n.items) - notifyMaxItems
	for _, item := range n.items[:drop] {
		delete(n.ids, item.ID)
	}
	n.items = n.items[drop:]
}

func (n *notifyLog) snapshot(since int64) ([]Notification, NotifyPrefs, int64) {
	n.mu.Lock()
	defer n.mu.Unlock()
	prefs := n.prefs
	if prefs.Muted == nil {
		prefs.Muted = []string{}
	}
	quiet := inQuiet(n.now(), prefs.Quiet)
	muted := make(map[string]struct{}, len(prefs.Muted))
	for _, id := range prefs.Muted {
		muted[id] = struct{}{}
	}
	var latest int64
	items := make([]Notification, 0)
	for _, item := range n.items {
		if item.Seq > latest {
			latest = item.Seq
		}
		if item.Seq <= since {
			continue
		}
		cp := item
		_, muteSession := muted[cp.SessionID]
		_, muteSubject := muted[cp.SubjectID]
		cp.Banner = prefs.Enabled && !quiet && !muteSession && !muteSubject
		items = append(items, cp)
	}
	return items, prefs, latest
}

func (n *notifyLog) savePrefs(prefs NotifyPrefs) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.prefs = prefs
	return n.persistLocked()
}

func (n *notifyLog) persistLocked() error {
	if err := os.MkdirAll(n.dir, 0o700); err != nil {
		return err
	}
	_ = os.Chmod(n.dir, 0o700)
	state := n.state
	if state.Cursors == nil {
		state.Cursors = map[string]int64{}
	}
	sb, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if err := writeAtomic0600(filepath.Join(n.dir, "state.json"), sb); err != nil {
		return err
	}
	pb, err := json.Marshal(n.prefs)
	if err != nil {
		return err
	}
	if err := writeAtomic0600(filepath.Join(n.dir, "prefs.json"), pb); err != nil {
		return err
	}
	var buf strings.Builder
	for _, item := range n.items {
		stored := item
		stored.Banner = false
		line, err := json.Marshal(stored)
		if err != nil {
			return err
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	return writeAtomic0600(filepath.Join(n.dir, "log.jsonl"), []byte(buf.String()))
}

func writeAtomic0600(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func normalizePrefs(in NotifyPrefs) (NotifyPrefs, error) {
	out := NotifyPrefs{Enabled: in.Enabled, Quiet: in.Quiet, Muted: []string{}}
	if strings.TrimSpace(out.Quiet.Start) == "" {
		out.Quiet.Start = "22:00"
	}
	if strings.TrimSpace(out.Quiet.End) == "" {
		out.Quiet.End = "07:00"
	}
	if _, ok := parseHHMM(out.Quiet.Start); !ok {
		return NotifyPrefs{}, errors.New("invalid quiet hours")
	}
	if _, ok := parseHHMM(out.Quiet.End); !ok {
		return NotifyPrefs{}, errors.New("invalid quiet hours")
	}
	if len(in.Muted) > notifyMaxMuted {
		return NotifyPrefs{}, errors.New("too many muted sessions")
	}
	seen := map[string]struct{}{}
	for _, id := range in.Muted {
		safe, ok := safeID(strings.TrimSpace(id))
		if !ok {
			return NotifyPrefs{}, errors.New("invalid session id")
		}
		if _, ok := seen[safe]; ok {
			continue
		}
		seen[safe] = struct{}{}
		out.Muted = append(out.Muted, safe)
	}
	return out, nil
}

func parseHHMM(s string) (int, bool) {
	if len(s) != 5 || s[2] != ':' {
		return 0, false
	}
	hour, errH := strconv.Atoi(s[:2])
	min, errM := strconv.Atoi(s[3:])
	if errH != nil || errM != nil || hour < 0 || hour > 23 || min < 0 || min > 59 {
		return 0, false
	}
	return hour*60 + min, true
}

func inQuiet(now time.Time, q QuietHours) bool {
	if !q.Enabled {
		return false
	}
	start, okS := parseHHMM(q.Start)
	end, okE := parseHHMM(q.End)
	if !okS || !okE {
		return false
	}
	mins := now.Hour()*60 + now.Minute()
	if start == end {
		return true
	}
	if start < end {
		return mins >= start && mins < end
	}
	return mins >= start || mins < end
}

// projectLine maps one session JSONL record onto a notification.
// It copies an allowlist of short fields. Patterns, tool output, metadata,
// handoff files, and verification reports are not taken. Names and summaries
// that are copied are scrubbed and clamped.
func projectLine(logSession, fileParent string, line []byte, now time.Time) (Notification, bool) {
	var env struct {
		Type string          `json:"type"`
		Time time.Time       `json:"time"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(line, &env); err != nil || env.Type == "" || env.Type == "session.header" {
		return Notification{}, false
	}
	var data struct {
		SessionID       string `json:"sessionId"`
		ParentSessionID string `json:"parentSessionId"`
		TurnID          string `json:"turnId"`
		RequestID       string `json:"requestId"`
		Permission      string `json:"permission"`
		Status          string `json:"status"`
		Name            string `json:"name"`
		Summary         string `json:"summary"`
		StopReason      string `json:"stopReason"`
		State           string `json:"state"`
		Handoff         struct {
			Summary string `json:"summary"`
		} `json:"handoff"`
	}
	if len(env.Data) > 0 && string(env.Data) != "null" {
		if err := json.Unmarshal(env.Data, &data); err != nil {
			return Notification{}, false
		}
	}
	at := env.Time
	if at.IsZero() {
		at = now
	}
	focus := firstSafe(data.ParentSessionID, fileParent, logSession)
	if focus == "" {
		return Notification{}, false
	}
	switch env.Type {
	case "permission.asked":
		req, _ := safeID(data.RequestID)
		subject := firstSafe(data.SessionID, focus)
		return Notification{
			ID:        noteID(noteApproval, subject, req),
			Kind:      noteApproval,
			SessionID: focus,
			SubjectID: subject,
			Title:     "Approval needed",
			Body:      safePerm(data.Permission) + " is waiting",
			Href:      noteHref(focus, req),
			RequestID: req,
			At:        at.UTC(),
		}, true
	case "child.completed":
		return childNote(data.Status, data.Name, firstSummary(data.Summary, data.Handoff.Summary), data.SessionID, focus, at)
	case "delegation.changed":
		return childNote(delegationStatus(data.State), data.Name, "", data.SessionID, focus, at)
	case "turn.completed":
		if fileParent != "" || strings.TrimSpace(data.ParentSessionID) != "" {
			return Notification{}, false
		}
		extra, ok := safeID(data.TurnID)
		if !ok {
			extra = at.UTC().Format(time.RFC3339Nano)
		}
		body := "The turn finished"
		if reason := safeToken(data.StopReason); reason != "" {
			body = "Stopped: " + reason
		}
		return Notification{
			ID:        noteID(noteSessionFinished, focus, extra),
			Kind:      noteSessionFinished,
			SessionID: focus,
			SubjectID: focus,
			Title:     "Session finished",
			Body:      body,
			Href:      noteHref(focus, ""),
			At:        at.UTC(),
		}, true
	default:
		return Notification{}, false
	}
}

func childNote(status, name, summary, subjectRaw, focus string, at time.Time) (Notification, bool) {
	kind, verb := childKind(status)
	if kind == "" {
		return Notification{}, false
	}
	subject, ok := safeID(subjectRaw)
	if !ok {
		return Notification{}, false
	}
	who := safeLabel(name, 40)
	if who == "" {
		who = "Delegated child"
	}
	body := safeLabel(summary, 120)
	if body == "" {
		body = verb
	}
	return Notification{
		ID:        noteID(kind, subject, ""),
		Kind:      kind,
		SessionID: focus,
		SubjectID: subject,
		Title:     who + " " + strings.ToLower(verb),
		Body:      body,
		Href:      noteHref(focus, ""),
		At:        at.UTC(),
	}, true
}

func childKind(status string) (kind, verb string) {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "blocked":
		return noteChildBlocked, "Blocked"
	case "completed", "done":
		return noteChildFinished, "Finished"
	case "failed":
		return noteChildFinished, "Failed"
	case "canceled", "cancelled":
		return noteChildFinished, "Canceled"
	default:
		return "", ""
	}
}

func delegationStatus(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "blocked":
		return "blocked"
	case "done":
		return "completed"
	case "failed":
		return "failed"
	case "canceled", "cancelled":
		return "canceled"
	default:
		return ""
	}
}

func firstSummary(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

func firstSafe(ids ...string) string {
	for _, id := range ids {
		if safe, ok := safeID(id); ok {
			return safe
		}
	}
	return ""
}

func noteID(kind, subject, extra string) string {
	sum := sha256.Sum256([]byte(kind + "\n" + subject + "\n" + extra))
	return hex.EncodeToString(sum[:16])
}

func noteHref(sessionID, requestID string) string {
	if sessionID == "" {
		return "/#"
	}
	href := "/#session=" + sessionID
	if requestID != "" {
		href += "&approval=" + requestID
	}
	return href
}

func fallbackTitle(kind string) string {
	switch kind {
	case noteApproval:
		return "Approval needed"
	case noteChildBlocked:
		return "Delegated child blocked"
	case noteChildFinished:
		return "Delegated child finished"
	case noteSessionFinished:
		return "Session finished"
	default:
		return "Notice"
	}
}

func safeID(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > notifyMaxID {
		return "", false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '.' || r == '_' || r == ':' || r == '-':
		default:
			return "", false
		}
	}
	if strings.Contains(s, "..") {
		return "", false
	}
	// A session id is an opaque token. Reject credential-shaped strings so a
	// muted-session list cannot become a place to store a secret.
	if redact.ScrubToolOutput(s) != s {
		return "", false
	}
	return s, true
}

func safePerm(p string) string {
	p = strings.TrimSpace(p)
	if p == "" || len(p) > 32 {
		return "tool"
	}
	for _, r := range p {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '-' || r == '.':
		default:
			return "tool"
		}
	}
	return p
}

func safeToken(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 32 {
		return ""
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return ""
		}
	}
	return s
}

func safeLabel(s string, max int) string {
	s = redact.ScrubToolOutput(s)
	s = strings.Join(strings.Fields(s), " ")
	if s == "" || strings.HasPrefix(s, "{") || strings.HasPrefix(s, "[") {
		return ""
	}
	return clampRunes(s, max)
}

func clampRunes(s string, max int) string {
	if max <= 0 || s == "" {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max == 1 {
		return "…"
	}
	return string(r[:max-1]) + "…"
}
