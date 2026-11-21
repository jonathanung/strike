package server

import (
	"errors"
	"net/http"
	"unicode/utf8"

	"github.com/jonathanung/strike/internal/composer/sessionref"
	"github.com/jonathanung/strike/internal/composer/triggers"
)

// sessionReferenceResponse sizes an @session reference (#1550) before
// send: chars are the exact block the daemon will attach, tokens a ~4
// chars/token estimate. Display only — the expansion itself happens in
// the user.input path.
type sessionReferenceResponse struct {
	ID         string `json:"id"`
	Title      string `json:"title,omitempty"`
	Chars      int    `json:"chars"`
	Tokens     int    `json:"tokens"`
	Estimated  bool   `json:"estimated"`
	Turns      int    `json:"turns"`
	Omitted    int    `json:"omitted,omitempty"`
	HasSummary bool   `json:"hasSummary"`
	// Self is true when id is the session the request targets; sending
	// a self-reference attaches nothing.
	Self bool `json:"self,omitempty"`
}

func (s *Server) handleSessionReference(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := validateSessionID(id); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	self := false
	if live := s.resolveLive(w, r); live != nil && live.SessionID() == id {
		self = true
	}
	ref, err := sessionref.Read(s.opts.SessionDir, id, sessionref.DefaultTurns)
	if err != nil {
		code := http.StatusInternalServerError
		if errors.Is(err, triggers.ErrSessionNotFound) {
			code = http.StatusNotFound
		}
		writeJSON(w, code, opErrorResponse{Error: err.Error()})
		return
	}
	chars := 0
	if !self {
		chars = utf8.RuneCountInString(triggers.RenderSessionRef(ref))
	}
	writeJSON(w, http.StatusOK, sessionReferenceResponse{
		ID:         ref.ID,
		Title:      ref.Title,
		Chars:      chars,
		Tokens:     (chars + 3) / 4,
		Estimated:  true,
		Turns:      len(ref.Turns),
		Omitted:    ref.Omitted,
		HasSummary: ref.Summary != "",
		Self:       self,
	})
}
