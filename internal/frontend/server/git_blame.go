package server

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Blame (#1479) backs the document viewer's read-only Blame toggle:
// per-line author, author date, commit, and subject for one file, as
// `git blame` reports it for the working tree (uncommitted lines are
// flagged). The response is windowed by start/limit like the log
// route; a file git cannot blame (untracked, never committed, no repo)
// is an honest available=false with a reason, not an error.

const (
	gitBlameDefault = 2000
	gitBlameMax     = 5000
)

type gitBlameLineJSON struct {
	Line        int    `json:"line"`
	Hash        string `json:"hash"`
	Author      string `json:"author"`
	Date        string `json:"date"`
	Time        int64  `json:"time"`
	Subject     string `json:"subject"`
	Uncommitted bool   `json:"uncommitted,omitempty"`
}

type gitBlameJSON struct {
	Path      string             `json:"path"`
	Available bool               `json:"available"`
	Reason    string             `json:"reason,omitempty"`
	Head      string             `json:"head,omitempty"`
	Start     int                `json:"start"`
	Total     int                `json:"total"`
	Truncated bool               `json:"truncated"`
	Lines     []gitBlameLineJSON `json:"lines"`
}

func (s *Server) handleGitBlame(w http.ResponseWriter, r *http.Request) {
	s.gitMu.Lock()
	defer s.gitMu.Unlock()
	cwd, ok := s.gitCWD(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	rel, err := safeRepoPath(q.Get("path"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	start, err := positiveQueryInt(q.Get("start"), 1, 0)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "start " + err.Error()})
		return
	}
	limit, err := positiveQueryInt(q.Get("limit"), gitBlameDefault, gitBlameMax)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "limit " + err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), gitReadTimeout)
	defer cancel()
	body, err := gitBlame(ctx, cwd, rel, start, limit)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, body)
}

// positiveQueryInt parses an optional positive integer query value:
// empty means def, and a positive max caps the result.
func positiveQueryInt(raw string, def, max int) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return 0, errors.New("must be a positive integer")
	}
	if max > 0 && n > max {
		n = max
	}
	return n, nil
}

func gitBlame(ctx context.Context, cwd, rel string, start, limit int) (gitBlameJSON, error) {
	body := gitBlameJSON{Path: rel, Start: start, Lines: []gitBlameLineJSON{}}
	head, _, err := gitRun(ctx, cwd, "rev-parse", "--verify", "-q", "HEAD")
	if err != nil {
		body.Reason = "No blame available: not a git repository, or it has no commits yet."
		return body, nil
	}
	if _, _, err := gitRun(ctx, cwd, "ls-files", "--error-unmatch", "--", rel); err != nil {
		body.Reason = "No blame available: this file is not tracked by git."
		return body, nil
	}
	out, stderr, err := gitRun(ctx, cwd, "blame", "--porcelain", "--", rel)
	if err != nil {
		if strings.Contains(stderr, "no such path") {
			body.Reason = "No blame available: this file has not been committed yet."
			return body, nil
		}
		return body, errors.New(gitErr("blame", err, stderr))
	}
	all := parseBlamePorcelain(out)
	body.Available = true
	body.Head = strings.TrimSpace(string(head))
	body.Total = len(all)
	from := min(start-1, len(all))
	to := min(from+limit, len(all))
	body.Lines = append(body.Lines, all[from:to]...)
	body.Truncated = to < len(all)
	return body, nil
}

// parseBlamePorcelain reads `git blame --porcelain`: each line starts
// with "<hash> <orig> <final> [<group>]", commit headers (author,
// author-time, summary, …) appear only the first time a hash is seen,
// and the source line itself follows a tab. Time is the author time in
// Unix seconds (the client renders it relative); Date is the same
// instant as RFC 3339 UTC.
func parseBlamePorcelain(out []byte) []gitBlameLineJSON {
	type commitInfo struct {
		author, date, subject string
		time                  int64
	}
	commits := map[string]*commitInfo{}
	lines := []gitBlameLineJSON{}
	var cur *gitBlameLineJSON
	var info *commitInfo
	for _, raw := range strings.Split(string(out), "\n") {
		if cur == nil {
			f := strings.Fields(raw)
			if len(f) < 3 || !isBlameHash(f[0]) {
				continue
			}
			final, err := strconv.Atoi(f[2])
			if err != nil {
				continue
			}
			info = commits[f[0]]
			if info == nil {
				info = &commitInfo{}
				commits[f[0]] = info
			}
			cur = &gitBlameLineJSON{Line: final, Hash: f[0], Uncommitted: strings.Trim(f[0], "0") == ""}
			continue
		}
		if strings.HasPrefix(raw, "\t") {
			cur.Author, cur.Date, cur.Time, cur.Subject = info.author, info.date, info.time, info.subject
			lines = append(lines, *cur)
			cur = nil
			continue
		}
		key, val, _ := strings.Cut(raw, " ")
		switch key {
		case "author":
			info.author = val
		case "author-time":
			if sec, err := strconv.ParseInt(val, 10, 64); err == nil {
				info.time = sec
				info.date = time.Unix(sec, 0).UTC().Format(time.RFC3339)
			}
		case "summary":
			info.subject = val
		}
	}
	// Windowing slices by line number; keep that independent of the
	// order git emits blame groups in.
	slices.SortFunc(lines, func(a, b gitBlameLineJSON) int { return cmp.Compare(a.Line, b.Line) })
	return lines
}

// isBlameHash accepts a full SHA-1 or SHA-256 object name.
func isBlameHash(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
