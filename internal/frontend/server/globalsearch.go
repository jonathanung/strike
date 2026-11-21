package server

// Global transcript search (#1355): GET /v1/search?q=… finds text across
// every root session's user and assistant messages, not just titles or the
// open transcript.
//
// Index design (slice 1): an on-demand scan, not a maintained index. Each
// request streams the JSONL logs newest-first, so results are always fresh
// (a message is searchable as soon as it is appended) and there is no index
// to build, persist, or corrupt. The cost is bounded and stated: a request
// stops after searchTimeBudget and reports `partial`, and at most
// searchMaxHits sessions are returned with `capped` when more matched.
//
// Corpus: user.message text and assistant text (text.delta, joined per
// message). Tool input/output, reasoning, and child-session streams are
// excluded. All text passes through pkg/redact before matching, so a
// redacted secret can neither match nor appear in a snippet.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jonathanung/strike/internal/persist/session"
	"github.com/jonathanung/strike/pkg/protocol"
	"github.com/jonathanung/strike/pkg/redact"
)

const (
	searchMaxHits     = 50
	searchMinRunes    = 2
	searchMaxRunes    = 200
	searchTimeBudget  = 4 * time.Second
	snippetBeforeRune = 60
	snippetAfterRune  = 120
)

type globalSearchHit struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	WorkspaceID string `json:"workspaceId,omitempty"`
	UpdatedAt   int64  `json:"updatedAt"` // unix ms
	Role        string `json:"role"`      // user | assistant
	Snippet     string `json:"snippet"`
	MatchStart  int    `json:"matchStart"` // byte offsets into Snippet
	MatchEnd    int    `json:"matchEnd"`
	Matches     int    `json:"matches"` // matching messages in the session
}

type globalSearchResponse struct {
	Query    string            `json:"query"`
	Hits     []globalSearchHit `json:"hits"`
	Limit    int               `json:"limit"`
	Capped   bool              `json:"capped"`
	Partial  bool              `json:"partial"`
	Scanned  int               `json:"scanned"`
	Sessions int               `json:"sessions"`
	// Skipped counts unreadable or malformed log lines that were ignored.
	Skipped int `json:"skipped"`
}

type searchMessage struct {
	Role string
	Text string // redacted
}

// searchLineTypes are the only envelope types decoded; everything else is
// skipped by a cheap substring check before JSON decoding.
var searchLineTypes = [][]byte{
	[]byte(`"user.message"`), []byte(`"text.delta"`), []byte(`"turn.started"`), []byte(`"turn.completed"`),
}

// sessionSearchMessages reads one session log into its redacted user and
// assistant messages. Malformed lines are skipped and counted.
func sessionSearchMessages(r io.Reader, id string) ([]searchMessage, int, error) {
	var (
		out       []searchMessage
		assistant strings.Builder
		skipped   int
	)
	flush := func() {
		if t := strings.TrimSpace(assistant.String()); t != "" {
			out = append(out, searchMessage{Role: "assistant", Text: redact.String(t)})
		}
		assistant.Reset()
	}
	br := bufio.NewReaderSize(r, 64*1024)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			if interesting(line) {
				var env protocol.Envelope
				if json.Unmarshal(bytes.TrimSpace(line), &env) != nil {
					skipped++
				} else if ev, derr := env.Decode(); derr != nil {
					skipped++
				} else {
					switch e := ev.(type) {
					case protocol.UserMessage:
						if ownSearchEvent(id, e.Correlation) {
							flush()
							if t := strings.TrimSpace(e.Text); t != "" {
								out = append(out, searchMessage{Role: "user", Text: redact.String(t)})
							}
						}
					case protocol.TextDelta:
						if ownSearchEvent(id, e.Correlation) {
							assistant.WriteString(e.Text)
						}
					case protocol.TurnStarted:
						if ownSearchEvent(id, e.Correlation) {
							flush()
						}
					case protocol.TurnCompleted:
						if ownSearchEvent(id, e.Correlation) {
							flush()
						}
					}
				}
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			flush()
			return out, skipped, err
		}
	}
	flush()
	return out, skipped, nil
}

func interesting(line []byte) bool {
	for _, t := range searchLineTypes {
		if bytes.Contains(line, t) {
			return true
		}
	}
	return false
}

func ownSearchEvent(id string, c protocol.Correlation) bool {
	return c.SessionID == "" || c.SessionID == id
}

// snippetAround returns whitespace-flattened text around [start,end) with
// the match's byte offsets inside the snippet. Cuts land on rune boundaries.
func snippetAround(text string, start, end int) (string, int, int) {
	from := start
	for n := 0; from > 0 && n < snippetBeforeRune; n++ {
		_, size := utf8.DecodeLastRuneInString(text[:from])
		from -= size
	}
	to := end
	for n := 0; to < len(text) && n < snippetAfterRune; n++ {
		_, size := utf8.DecodeRuneInString(text[to:])
		to += size
	}
	prefix, suffix := "", ""
	if from > 0 {
		prefix = "…"
	}
	if to < len(text) {
		suffix = "…"
	}
	flat := func(s string) string {
		return strings.Map(func(r rune) rune {
			if r == '\n' || r == '\r' || r == '\t' {
				return ' '
			}
			return r
		}, s)
	}
	snip := prefix + flat(text[from:to]) + suffix
	ms := len(prefix) + (start - from)
	return snip, ms, ms + (end - start)
}

func searchSessionTitle(info session.Info, msgs []searchMessage) string {
	if t := strings.TrimSpace(info.Title); t != "" && !titleIsRawID(t, info.ID) {
		return t
	}
	for _, m := range msgs {
		if m.Role == "user" {
			if t := session.TitleFromText(m.Text); t != "" {
				return t
			}
		}
	}
	return ""
}

// runGlobalSearch searches infos (any order) newest-first. open opens a
// session log; now is the clock used for the time budget.
func runGlobalSearch(q string, infos []session.Info, open func(session.Info) (io.ReadCloser, error), now func() time.Time) globalSearchResponse {
	resp := globalSearchResponse{Query: q, Hits: []globalSearchHit{}, Limit: searchMaxHits}
	re := regexp.MustCompile(`(?i)` + regexp.QuoteMeta(q))
	roots := make([]session.Info, 0, len(infos))
	for _, info := range infos {
		if info.ParentSessionID == "" {
			roots = append(roots, info)
		}
	}
	sort.SliceStable(roots, func(i, j int) bool {
		if !roots[i].UpdatedAt.Equal(roots[j].UpdatedAt) {
			return roots[i].UpdatedAt.After(roots[j].UpdatedAt)
		}
		return roots[i].ID < roots[j].ID
	})
	resp.Sessions = len(roots)
	deadline := now().Add(searchTimeBudget)
	for _, info := range roots {
		if now().After(deadline) {
			resp.Partial = true
			break
		}
		rc, err := open(info)
		if err != nil {
			resp.Skipped++
			resp.Scanned++
			continue
		}
		msgs, skipped, rerr := sessionSearchMessages(rc, info.ID)
		rc.Close()
		resp.Scanned++
		resp.Skipped += skipped
		if rerr != nil {
			resp.Skipped++
		}
		var hit *globalSearchHit
		for _, m := range msgs {
			loc := re.FindStringIndex(m.Text)
			if loc == nil {
				continue
			}
			if hit == nil {
				snip, ms, me := snippetAround(m.Text, loc[0], loc[1])
				hit = &globalSearchHit{
					ID:          info.ID,
					Title:       searchSessionTitle(info, msgs),
					WorkspaceID: info.WorkspaceID,
					UpdatedAt:   info.UpdatedAt.UnixMilli(),
					Role:        m.Role,
					Snippet:     snip,
					MatchStart:  ms,
					MatchEnd:    me,
				}
			}
			hit.Matches++
		}
		if hit == nil {
			continue
		}
		if len(resp.Hits) >= searchMaxHits {
			resp.Capped = true
			break
		}
		resp.Hits = append(resp.Hits, *hit)
	}
	return resp
}

func (s *Server) handleGlobalSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	switch n := utf8.RuneCountInString(q); {
	case n == 0:
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "q is empty", Code: protocol.ErrorCodeInvalidArgs})
		return
	case n < searchMinRunes:
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "q needs at least 2 characters", Code: protocol.ErrorCodeInvalidArgs})
		return
	case n > searchMaxRunes:
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "q is too long", Code: protocol.ErrorCodeInvalidArgs})
		return
	}
	if s.opts.SessionDir == "" {
		capabilityUnavailable(w, "sessions")
		return
	}
	infos, err := session.NewManager(s.opts.SessionDir).List()
	if err != nil && !os.IsNotExist(err) {
		writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: err.Error()})
		return
	}
	open := func(info session.Info) (io.ReadCloser, error) { return os.Open(info.Path) }
	writeJSON(w, http.StatusOK, runGlobalSearch(q, infos, open, time.Now))
}
