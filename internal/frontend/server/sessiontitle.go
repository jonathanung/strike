package server

// On-demand session title regeneration (#1370):
// POST /v1/sessions/{id}/title/propose re-derives a title from the
// conversation as it now stands and returns it WITHOUT storing it. The
// cockpit shows the proposal in a confirm dialog and only a confirmed
// proposal is written, through the existing PATCH /v1/sessions/{id} rename
// route, so no code path here can replace a title.
//
// Generation reuses the creation-time titling logic (#48,
// session.TitleFromText — whitespace collapse, control strip, truncation;
// no model call) and applies it to the newest substantive user message
// instead of the first one, so a session whose topic drifted gets a title
// for what it is about now.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/jonathanung/strike/internal/persist/session"
	"github.com/jonathanung/strike/pkg/protocol"
	"github.com/jonathanung/strike/pkg/redact"
)

const (
	// titleCodeEmpty: the session has no user message to title from.
	titleCodeEmpty = "title_session_empty"
	// titleCodeSingle: one user message only — its title is already the
	// creation-time title, so regenerating would only repeat it.
	titleCodeSingle = "title_single_message"
	// titleMinWords is how many words a message's derived title needs to
	// count as substantive ("yes", "go on" are skipped when a longer
	// message exists).
	titleMinWords = 3
)

type titleProposal struct {
	Title   string `json:"title"`
	Current string `json:"current"`
	// Manual is true when the current title is neither the auto-title nor
	// a title derived from one of the session's own messages: the user
	// typed it. The cockpit's confirm copy says so.
	Manual bool `json:"manual"`
	// Unchanged is true when the proposal equals the current title.
	Unchanged bool `json:"unchanged"`
	// Messages is the number of user messages considered.
	Messages int `json:"messages"`
}

// proposeSessionTitle scans a raw session log for the session's own user
// messages and SessionTitled events. Malformed or unknown lines are skipped:
// a damaged line must not block retitling from the rest of the record.
// ok is false (with an error code) when there is nothing new to title from.
func proposeSessionTitle(sessionID, current string, raw []byte) (titleProposal, string) {
	var msgs []string // derived titles of own user messages, oldest first
	var autoTitle string
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 0, 64*1024), 16<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var env protocol.Envelope
		if json.Unmarshal(line, &env) != nil || env.Type == "" || env.Type == "session.header" {
			continue
		}
		ev, err := env.Decode()
		if err != nil {
			continue
		}
		switch e := ev.(type) {
		case protocol.UserMessage:
			if !ownSessionEvent(sessionID, e.Correlation) {
				continue
			}
			if t := session.TitleFromText(redact.String(e.Text)); t != "" {
				msgs = append(msgs, t)
			}
		case protocol.SessionTitled:
			if !ownSessionEvent(sessionID, e.Correlation) {
				continue
			}
			if t := strings.TrimSpace(e.Title); t != "" {
				autoTitle = t
			}
		}
	}
	current = strings.TrimSpace(current)
	p := titleProposal{Current: current, Messages: len(msgs)}
	switch len(msgs) {
	case 0:
		return p, titleCodeEmpty
	case 1:
		return p, titleCodeSingle
	}
	p.Title = msgs[len(msgs)-1]
	for i := len(msgs) - 1; i >= 0; i-- {
		if len(strings.Fields(msgs[i])) >= titleMinWords {
			p.Title = msgs[i]
			break
		}
	}
	p.Unchanged = p.Title == current
	p.Manual = current != "" && current != autoTitle && !containsString(msgs, current)
	return p, ""
}

// ownSessionEvent keeps child-session streams interleaved in a parent's log
// out of the parent's title.
func ownSessionEvent(sessionID string, c protocol.Correlation) bool {
	return c.SessionID == "" || c.SessionID == sessionID
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func (s *Server) handleSessionTitlePropose(w http.ResponseWriter, r *http.Request) {
	if s.opts.Services == nil || s.opts.Services.Sessions == nil {
		capabilityUnavailable(w, "sessions")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "session id is required"})
		return
	}
	sess, found, err := s.opts.Services.Sessions.Get(id)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, lifecycleHTTPError(err))
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, opErrorResponse{Error: "session not found", Code: protocol.ErrorCodeSessionNotFound})
		return
	}
	raw, err := s.opts.Services.Sessions.ReplayJSONL(id)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, lifecycleHTTPError(err))
		return
	}
	p, code := proposeSessionTitle(id, sess.Title, raw)
	switch code {
	case titleCodeEmpty:
		writeJSON(w, http.StatusUnprocessableEntity, opErrorResponse{Error: "session has no messages to title from", Code: code})
		return
	case titleCodeSingle:
		writeJSON(w, http.StatusUnprocessableEntity, opErrorResponse{Error: "session has a single message; its title already comes from it", Code: code})
		return
	}
	writeJSON(w, http.StatusOK, p)
}
