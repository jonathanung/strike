package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jonathanung/strike/internal/protocol"
)

// Review queue (#1324): the operator's per-hunk review of the live
// session's pending working-tree changes.
//
//   - GET    /v1/review                 files + hunks (git diff), with the
//     stored accept marks, comments and rejections for the session.
//   - POST   /v1/review/state           mark one hunk accepted (or clear it).
//   - POST   /v1/review/reject          revert one hunk with `git apply -R`.
//   - POST   /v1/review/comments        comment anchored to file + line range.
//   - DELETE /v1/review/comments/{id}   drop an unsent comment.
//   - POST   /v1/review/send            deliver every comment and rejection
//     to the session as one user turn, then clear them.
//
// Hunk diffs come from the same `git diff` the Git panel uses; nothing here
// rewrites files by hand. Every route takes the usual `?root=` session
// scope. Without a live session the queue is read-only.

const (
	reviewDiffCap        = gitDiffCap
	reviewCommentMax     = 4000
	reviewCommentsMax    = 200
	reviewRejectedMax    = 200
	reviewHunkLinesMax   = 400
	reviewTurnLinesMax   = 80
	reviewStoreVersion   = 1
	reviewStateAccepted  = "accepted"
	reviewReadOnlyReason = "read-only attach mode: there is no live session to review or send to"
	reviewBusyReason     = "the session is mid-turn; send the review once it is idle"
)

// ReviewTurnHeader opens every review turn. The web transcript keys on it
// to render the turn as a review instead of a typed message; keep it in
// sync with REVIEW_TURN_HEADER in webui/src/review_queue.rs.
const ReviewTurnHeader = "Review of your pending changes (strike review queue)."

var reviewHunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

type reviewHunkJSON struct {
	ID       string   `json:"id"`
	Header   string   `json:"header"`
	OldStart int      `json:"oldStart"`
	OldLines int      `json:"oldLines"`
	NewStart int      `json:"newStart"`
	NewLines int      `json:"newLines"`
	Lines    []string `json:"lines"`
	State    string   `json:"state"`
}

type reviewFileJSON struct {
	Path string `json:"path"`
	// Note explains a file the queue lists but cannot split into hunks
	// (binary, mode-only, or a path git had to quote).
	Note  string           `json:"note,omitempty"`
	Hunks []reviewHunkJSON `json:"hunks"`
}

type reviewCommentJSON struct {
	ID        string `json:"id"`
	Path      string `json:"path"`
	HunkID    string `json:"hunkId,omitempty"`
	StartLine int    `json:"startLine"`
	EndLine   int    `json:"endLine"`
	Body      string `json:"body"`
	CreatedAt string `json:"createdAt"`
}

type reviewRejectedJSON struct {
	ID     string   `json:"id"`
	Path   string   `json:"path"`
	Header string   `json:"header"`
	Lines  []string `json:"lines"`
	At     string   `json:"at"`
}

type reviewQueueJSON struct {
	SessionID string               `json:"sessionId"`
	ReadOnly  bool                 `json:"readOnly"`
	Busy      bool                 `json:"busy"`
	Reason    string               `json:"reason,omitempty"`
	Truncated bool                 `json:"truncated"`
	Files     []reviewFileJSON     `json:"files"`
	Comments  []reviewCommentJSON  `json:"comments"`
	Rejected  []reviewRejectedJSON `json:"rejected"`
}

// reviewDoc is one session's stored review: what the operator has said
// that the agent has not yet received.
type reviewDoc struct {
	Version  int                  `json:"version"`
	Accepted map[string]bool      `json:"accepted,omitempty"`
	Comments []reviewCommentJSON  `json:"comments,omitempty"`
	Rejected []reviewRejectedJSON `json:"rejected,omitempty"`
}

func (d *reviewDoc) empty() bool {
	return len(d.Comments) == 0 && len(d.Rejected) == 0
}

// reviewStore persists one JSON file per session under
// <SessionDir>/review/<session>.json. A missing session dir keeps the
// review in memory only.
type reviewStore struct {
	mu  sync.Mutex
	dir string
	mem map[string]*reviewDoc
}

func newReviewStore(sessionDir string) *reviewStore {
	dir := ""
	if strings.TrimSpace(sessionDir) != "" {
		dir = filepath.Join(sessionDir, "review")
	}
	return &reviewStore{dir: dir, mem: map[string]*reviewDoc{}}
}

var reviewSessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

func (st *reviewStore) path(id string) (string, error) {
	if !reviewSessionIDPattern.MatchString(id) || id == "." || id == ".." {
		return "", errors.New("invalid session id")
	}
	if st.dir == "" {
		return "", nil
	}
	return filepath.Join(st.dir, id+".json"), nil
}

// loadLocked returns the doc for id; callers hold st.mu.
func (st *reviewStore) loadLocked(id string) (*reviewDoc, error) {
	if doc, ok := st.mem[id]; ok {
		return doc, nil
	}
	p, err := st.path(id)
	if err != nil {
		return nil, err
	}
	doc := &reviewDoc{Version: reviewStoreVersion}
	if p != "" {
		raw, err := os.ReadFile(p)
		switch {
		case err == nil:
			if err := json.Unmarshal(raw, doc); err != nil {
				return nil, errors.New("review store is unreadable: " + err.Error())
			}
		case !errors.Is(err, os.ErrNotExist):
			return nil, err
		}
	}
	st.mem[id] = doc
	return doc, nil
}

func (st *reviewStore) saveLocked(id string, doc *reviewDoc) error {
	p, err := st.path(id)
	if err != nil || p == "" {
		return err
	}
	if doc.empty() && len(doc.Accepted) == 0 {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	doc.Version = reviewStoreVersion
	raw, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	return writePrivateFile(p, append(raw, '\n'))
}

// update loads id, applies fn, and saves when fn returns nil. On a save
// failure the in-memory copy is reloaded from disk next time.
func (st *reviewStore) update(id string, fn func(*reviewDoc) error) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	doc, err := st.loadLocked(id)
	if err != nil {
		return err
	}
	snapshot := cloneReviewDoc(doc)
	if err := fn(doc); err != nil {
		st.mem[id] = snapshot
		return err
	}
	if err := st.saveLocked(id, doc); err != nil {
		st.mem[id] = snapshot
		return err
	}
	return nil
}

func (st *reviewStore) get(id string) (reviewDoc, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	doc, err := st.loadLocked(id)
	if err != nil {
		return reviewDoc{}, err
	}
	return *cloneReviewDoc(doc), nil
}

func cloneReviewDoc(doc *reviewDoc) *reviewDoc {
	out := &reviewDoc{Version: doc.Version}
	if len(doc.Accepted) > 0 {
		out.Accepted = make(map[string]bool, len(doc.Accepted))
		for k, v := range doc.Accepted {
			out.Accepted[k] = v
		}
	}
	out.Comments = append([]reviewCommentJSON(nil), doc.Comments...)
	out.Rejected = append([]reviewRejectedJSON(nil), doc.Rejected...)
	return out
}

// reviewFileDiff is one file block of `git diff`: the header lines a
// single-hunk patch needs, plus its hunks.
type reviewFileDiff struct {
	path   string
	header []string
	note   string
	hunks  []reviewHunkJSON
}

// parseReviewDiff splits unified `git diff` output into files and hunks.
// Hunk IDs hash the path and the hunk body (not its line numbers), so an
// edit above a hunk does not change its identity; identical bodies in one
// file get an occurrence suffix.
func parseReviewDiff(out string) []reviewFileDiff {
	var files []reviewFileDiff
	var cur *reviewFileDiff
	var hunk *reviewHunkJSON
	flushHunk := func() {
		if cur != nil && hunk != nil {
			cur.hunks = append(cur.hunks, *hunk)
		}
		hunk = nil
	}
	flushFile := func() {
		flushHunk()
		if cur != nil {
			files = append(files, *cur)
		}
		cur = nil
	}
	lines := strings.Split(out, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	for _, line := range lines {
		if strings.HasPrefix(line, "diff --git ") {
			flushFile()
			cur = &reviewFileDiff{header: []string{line}}
			continue
		}
		if cur == nil {
			continue
		}
		if hunk == nil {
			switch {
			case strings.HasPrefix(line, "@@ "):
			case strings.HasPrefix(line, "+++ "):
				cur.header = append(cur.header, line)
				if p := strings.TrimPrefix(line, "+++ "); p != "/dev/null" {
					cur.path = reviewDiffPath(p, "b/")
				}
				continue
			case strings.HasPrefix(line, "--- "):
				cur.header = append(cur.header, line)
				if p := strings.TrimPrefix(line, "--- "); p != "/dev/null" && cur.path == "" {
					cur.path = reviewDiffPath(p, "a/")
				}
				continue
			case strings.HasPrefix(line, "Binary files "):
				cur.note = "binary file: review it in the Git panel"
				continue
			default:
				cur.header = append(cur.header, line)
				continue
			}
		}
		if m := reviewHunkHeader.FindStringSubmatch(line); m != nil {
			flushHunk()
			hunk = &reviewHunkJSON{
				Header:   line,
				OldStart: atoiDefault(m[1], 0),
				OldLines: atoiDefault(m[2], 1),
				NewStart: atoiDefault(m[3], 0),
				NewLines: atoiDefault(m[4], 1),
				Lines:    []string{},
			}
			continue
		}
		if hunk != nil {
			hunk.Lines = append(hunk.Lines, line)
		}
	}
	flushFile()
	for i := range files {
		f := &files[i]
		if f.path == "" {
			f.path = reviewPathFromGitLine(f.header[0])
		}
		if strings.HasPrefix(f.path, "\"") {
			f.note = "git quoted this path: review it in the Git panel"
			f.hunks = nil
		}
		if f.note == "" && len(f.hunks) == 0 {
			f.note = "no text hunks (mode or rename only)"
		}
		seen := map[string]int{}
		for j := range f.hunks {
			sum := sha256.Sum256([]byte(f.path + "\x00" + strings.Join(f.hunks[j].Lines, "\n")))
			id := "h" + hex.EncodeToString(sum[:8])
			seen[id]++
			if n := seen[id]; n > 1 {
				id += "-" + strconv.Itoa(n)
			}
			f.hunks[j].ID = id
		}
	}
	return files
}

func reviewDiffPath(p, prefix string) string {
	p = strings.TrimSuffix(p, "\t")
	if strings.HasPrefix(p, "\"") {
		return p
	}
	return strings.TrimPrefix(p, prefix)
}

// reviewPathFromGitLine recovers the path of a header-only block (mode
// change) from `diff --git a/x b/x`.
func reviewPathFromGitLine(line string) string {
	rest := strings.TrimPrefix(line, "diff --git ")
	if i := strings.LastIndex(rest, " b/"); i >= 0 {
		return rest[i+3:]
	}
	return rest
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

func reviewGitDiff(ctx context.Context, cwd, rel string) ([]reviewFileDiff, bool, error) {
	args := []string{"diff", "--no-color", "--no-ext-diff", "--src-prefix=a/", "--dst-prefix=b/"}
	if rel != "" {
		args = append(args, "--", rel)
	}
	out, truncated, stderr, err := gitRunCapped(ctx, cwd, reviewDiffCap, args...)
	if err != nil {
		return nil, false, errors.New(gitErr("diff", err, stderr))
	}
	files := parseReviewDiff(string(out))
	if truncated && len(files) > 0 {
		// The last block may be cut mid-hunk; never offer it for review.
		files = files[:len(files)-1]
	}
	return files, truncated, nil
}

// reviewHunkPatch is the minimal patch that reverses exactly one hunk.
func reviewHunkPatch(f reviewFileDiff, h reviewHunkJSON) []byte {
	var b bytes.Buffer
	for _, line := range f.header {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	b.WriteString(h.Header)
	b.WriteByte('\n')
	for _, line := range h.Lines {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.Bytes()
}

func gitApplyReverse(ctx context.Context, cwd string, patch []byte) error {
	for _, check := range []bool{true, false} {
		args := []string{"-C", cwd, "apply", "-R", "--whitespace=nowarn"}
		if check {
			args = append(args, "--check")
		}
		args = append(args, "-")
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Env = gitEnv()
		cmd.Stdin = bytes.NewReader(patch)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return errors.New(gitErr("apply -R", err, stderr.String()))
		}
	}
	return nil
}

func reviewNewID(prefix string) string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return prefix + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return prefix + hex.EncodeToString(b[:])
}

// reviewScope resolves the live session for a review request. ok=false
// means the queue is read-only and reason says why.
func (s *Server) reviewScope(r *http.Request) (live *Live, reason string) {
	if s.opts.ReadOnly {
		return nil, reviewReadOnlyReason
	}
	if s.opts.LiveHub != nil {
		if l := s.opts.LiveHub.LiveFor(rootParam(r)); l != nil {
			return l, ""
		}
		return nil, reviewReadOnlyReason
	}
	if s.opts.Live != nil {
		return s.opts.Live, ""
	}
	return nil, reviewReadOnlyReason
}

func (s *Server) reviewMutableScope(w http.ResponseWriter, r *http.Request) (*Live, string, bool) {
	live, reason := s.reviewScope(r)
	if live == nil {
		writeJSON(w, http.StatusForbidden, opErrorResponse{Error: reason})
		return nil, "", false
	}
	cwd := strings.TrimSpace(live.Status().CWD)
	if cwd == "" {
		capabilityUnavailable(w, "git")
		return nil, "", false
	}
	return live, cwd, true
}

func (s *Server) handleReviewGet(w http.ResponseWriter, r *http.Request) {
	live, reason := s.reviewScope(r)
	body := reviewQueueJSON{
		ReadOnly: live == nil,
		Reason:   reason,
		Files:    []reviewFileJSON{},
		Comments: []reviewCommentJSON{},
		Rejected: []reviewRejectedJSON{},
	}
	cwd := strings.TrimSpace(s.currentCWD(r))
	if live != nil {
		st := live.Status()
		body.SessionID = live.SessionID()
		body.Busy = st.Busy
		cwd = strings.TrimSpace(st.CWD)
	}
	if cwd == "" {
		if live == nil {
			// Read-only attach: show the reason, not a capability error.
			writeJSON(w, http.StatusOK, body)
			return
		}
		capabilityUnavailable(w, "git")
		return
	}
	s.gitMu.Lock()
	ctx, cancel := context.WithTimeout(r.Context(), gitReadTimeout)
	files, truncated, err := reviewGitDiff(ctx, cwd, "")
	cancel()
	s.gitMu.Unlock()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	body.Truncated = truncated
	var doc reviewDoc
	if body.SessionID != "" {
		if doc, err = s.review.get(body.SessionID); err != nil {
			writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: err.Error()})
			return
		}
	}
	for _, f := range files {
		out := reviewFileJSON{Path: f.path, Note: f.note, Hunks: []reviewHunkJSON{}}
		for _, h := range f.hunks {
			if doc.Accepted[h.ID] {
				h.State = reviewStateAccepted
			}
			if len(h.Lines) > reviewHunkLinesMax {
				h.Lines = append(h.Lines[:reviewHunkLinesMax:reviewHunkLinesMax], "… (hunk truncated in the queue)")
			}
			out.Hunks = append(out.Hunks, h)
		}
		body.Files = append(body.Files, out)
	}
	body.Comments = append(body.Comments, doc.Comments...)
	body.Rejected = append(body.Rejected, doc.Rejected...)
	writeJSON(w, http.StatusOK, body)
}

func (s *Server) handleReviewState(w http.ResponseWriter, r *http.Request) {
	live, _, ok := s.reviewMutableScope(w, r)
	if !ok {
		return
	}
	var body struct {
		Hunk  string `json:"hunk"`
		State string `json:"state"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	hunk := strings.TrimSpace(body.Hunk)
	if hunk == "" || len(hunk) > 64 {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "hunk is required"})
		return
	}
	if body.State != reviewStateAccepted && body.State != "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "state must be accepted or empty"})
		return
	}
	err := s.review.update(live.SessionID(), func(doc *reviewDoc) error {
		if body.State == "" {
			delete(doc.Accepted, hunk)
			return nil
		}
		if doc.Accepted == nil {
			doc.Accepted = map[string]bool{}
		}
		if len(doc.Accepted) >= reviewRejectedMax*5 {
			return errors.New("too many accepted hunks; send the review first")
		}
		doc.Accepted[hunk] = true
		return nil
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, opOKResponse{OK: true})
}

func (s *Server) handleReviewReject(w http.ResponseWriter, r *http.Request) {
	live, cwd, ok := s.reviewMutableScope(w, r)
	if !ok {
		return
	}
	var body struct {
		Path string `json:"path"`
		Hunk string `json:"hunk"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	rel, err := safeRepoPath(body.Path)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	if live.Status().Busy {
		// The agent may be editing this very file; a revert now races it.
		writeJSON(w, http.StatusConflict, opErrorResponse{Error: "the session is mid-turn; reject hunks once it is idle"})
		return
	}
	s.gitMu.Lock()
	defer s.gitMu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), gitWriteTimeout)
	defer cancel()
	files, _, err := reviewGitDiff(ctx, cwd, rel)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	var file reviewFileDiff
	var hunk reviewHunkJSON
	found := false
	for _, f := range files {
		if f.path != rel {
			continue
		}
		for _, h := range f.hunks {
			if h.ID == body.Hunk {
				file, hunk, found = f, h, true
			}
		}
	}
	if !found {
		writeJSON(w, http.StatusConflict, opErrorResponse{Error: "that hunk is no longer in the diff; reload the review queue"})
		return
	}
	if err := gitApplyReverse(ctx, cwd, reviewHunkPatch(file, hunk)); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	rejected := reviewRejectedJSON{
		ID:     hunk.ID,
		Path:   rel,
		Header: hunk.Header,
		Lines:  hunk.Lines,
		At:     time.Now().UTC().Format(time.RFC3339),
	}
	err = s.review.update(live.SessionID(), func(doc *reviewDoc) error {
		delete(doc.Accepted, hunk.ID)
		if len(doc.Rejected) >= reviewRejectedMax {
			return errors.New("too many rejected hunks; send the review first")
		}
		doc.Rejected = append(doc.Rejected, rejected)
		return nil
	})
	if err != nil {
		// The revert already happened; say so rather than pretend it did not.
		writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: "hunk reverted but not recorded for the review: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "rejected": rejected})
}

func (s *Server) handleReviewCommentAdd(w http.ResponseWriter, r *http.Request) {
	live, _, ok := s.reviewMutableScope(w, r)
	if !ok {
		return
	}
	var body struct {
		Path      string `json:"path"`
		Hunk      string `json:"hunk"`
		StartLine int    `json:"startLine"`
		EndLine   int    `json:"endLine"`
		Body      string `json:"body"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	rel, err := safeRepoPath(body.Path)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	text := strings.TrimSpace(body.Body)
	switch {
	case text == "" || strings.ContainsRune(text, '\x00'):
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "comment is required"})
		return
	case len([]rune(text)) > reviewCommentMax:
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "comment is too long"})
		return
	case body.StartLine < 1 || body.EndLine < body.StartLine:
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "line range must start at 1 and not end before it starts"})
		return
	case len(body.Hunk) > 64:
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "hunk id is too long"})
		return
	}
	comment := reviewCommentJSON{
		ID:        reviewNewID("c"),
		Path:      rel,
		HunkID:    strings.TrimSpace(body.Hunk),
		StartLine: body.StartLine,
		EndLine:   body.EndLine,
		Body:      text,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	err = s.review.update(live.SessionID(), func(doc *reviewDoc) error {
		if len(doc.Comments) >= reviewCommentsMax {
			return errors.New("too many comments; send the review first")
		}
		doc.Comments = append(doc.Comments, comment)
		return nil
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, comment)
}

func (s *Server) handleReviewCommentDelete(w http.ResponseWriter, r *http.Request) {
	live, _, ok := s.reviewMutableScope(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	removed := false
	err := s.review.update(live.SessionID(), func(doc *reviewDoc) error {
		kept := doc.Comments[:0]
		for _, c := range doc.Comments {
			if c.ID == id {
				removed = true
				continue
			}
			kept = append(kept, c)
		}
		doc.Comments = kept
		return nil
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: err.Error()})
		return
	}
	if !removed {
		writeJSON(w, http.StatusNotFound, opErrorResponse{Error: "comment not found"})
		return
	}
	writeJSON(w, http.StatusOK, opOKResponse{OK: true})
}

func (s *Server) handleReviewSend(w http.ResponseWriter, r *http.Request) {
	live, _, ok := s.reviewMutableScope(w, r)
	if !ok {
		return
	}
	sessionID := live.SessionID()
	const opType = "review.send"
	if err := s.admitOp(r, "http", opType, sessionID); err != nil {
		status := http.StatusForbidden
		if errors.Is(err, errOpsRateLimited) {
			status = http.StatusTooManyRequests
		}
		writeJSON(w, status, opErrorResponse{Error: err.Error()})
		return
	}
	if live.Status().Busy {
		writeJSON(w, http.StatusConflict, opErrorResponse{Error: reviewBusyReason})
		return
	}
	// Hold the store lock across submit so a comment added meanwhile is
	// either in this turn or still queued afterwards, never lost.
	s.review.mu.Lock()
	defer s.review.mu.Unlock()
	doc, err := s.review.loadLocked(sessionID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: err.Error()})
		return
	}
	if doc.empty() {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "nothing to send: add a comment or reject a hunk first"})
		return
	}
	text := reviewTurnText(*doc)
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := live.Submit(ctx, protocol.UserInput{Text: text}); err != nil {
		s.recordServeOpError(r, "http", opType, sessionID)
		writeJSON(w, http.StatusConflict, opErrorResponse{Error: err.Error()})
		return
	}
	s.auditOpOK(r, "http", opType, sessionID)
	sent := map[string]int{"comments": len(doc.Comments), "rejected": len(doc.Rejected)}
	cleared := &reviewDoc{Version: reviewStoreVersion}
	s.review.mem[sessionID] = cleared
	if err := s.review.saveLocked(sessionID, cleared); err != nil {
		writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: "review sent but the queue was not cleared: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sent": sent})
}

// reviewTurnText renders the stored review as one user turn: grouped by
// file, rejections (already reverted) first, then comments by line.
func reviewTurnText(doc reviewDoc) string {
	var b strings.Builder
	b.WriteString(ReviewTurnHeader)
	b.WriteString("\nRejected hunks are already reverted in the working tree. Address every item, then summarize what you changed.\n")
	order := []string{}
	byPath := map[string]bool{}
	note := func(p string) {
		if !byPath[p] {
			byPath[p] = true
			order = append(order, p)
		}
	}
	for _, rj := range doc.Rejected {
		note(rj.Path)
	}
	for _, c := range doc.Comments {
		note(c.Path)
	}
	for _, p := range order {
		b.WriteString("\n### ")
		b.WriteString(p)
		b.WriteString("\n")
		for _, rj := range doc.Rejected {
			if rj.Path != p {
				continue
			}
			b.WriteString("- Rejected and reverted `")
			b.WriteString(rj.Header)
			b.WriteString("`:\n```diff\n")
			for i, line := range rj.Lines {
				if i == reviewTurnLinesMax {
					b.WriteString("… (" + strconv.Itoa(len(rj.Lines)-i) + " more lines)\n")
					break
				}
				b.WriteString(line)
				b.WriteByte('\n')
			}
			b.WriteString("```\n")
		}
		for _, c := range doc.Comments {
			if c.Path != p {
				continue
			}
			if c.StartLine == c.EndLine {
				b.WriteString("- Comment on line " + strconv.Itoa(c.StartLine) + ":\n")
			} else {
				b.WriteString("- Comment on lines " + strconv.Itoa(c.StartLine) + "-" + strconv.Itoa(c.EndLine) + ":\n")
			}
			for _, line := range strings.Split(c.Body, "\n") {
				b.WriteString("  > ")
				b.WriteString(line)
				b.WriteByte('\n')
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
