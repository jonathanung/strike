package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/jonathanung/strike/internal/persist/suggestion"
)

// Ideas inbox routes (#1323). Producers (scheduler heartbeats, triggers,
// finished children, goal autopilot) deposit suggestions with
// POST /v1/suggestions; the cockpit lists them and accepts, snoozes or
// dismisses. Accept only records the decision: the cockpit then starts a
// session through its normal session-start path under the workspace's
// current permission mode. There is no execution path here.

const (
	suggestionBodyLimit = 32 << 10
	suggestionPrefLimit = 16 << 10
)

type suggestionCreateRequest struct {
	Title       string                `json:"title"`
	Why         string                `json:"why"`
	Prompt      string                `json:"prompt"`
	Source      string                `json:"source"`
	Kind        string                `json:"kind"`
	Effort      string                `json:"effort"`
	EvidenceKey string                `json:"evidenceKey"`
	Evidence    []suggestion.Evidence `json:"evidence"`
	TTLHours    int                   `json:"ttlHours"`
}

type suggestionCreateResponse struct {
	Suggestion suggestion.Suggestion `json:"suggestion"`
	Created    bool                  `json:"created"`
}

type suggestionsResponse struct {
	Suggestions []suggestion.Suggestion `json:"suggestions"`
	Prefs       suggestion.Prefs        `json:"prefs"`
	Open        int                     `json:"open"`
}

type suggestionDismissRequest struct {
	Never bool `json:"never"`
}

type suggestionSnoozeRequest struct {
	Hours int `json:"hours"`
}

type suggestionPrefsRequest struct {
	MutedSources []string `json:"mutedSources"`
	MutedKinds   []string `json:"mutedKinds"`
	// QuietHours is optional: an older client that omits it keeps the
	// stored window.
	QuietHours *suggestion.QuietHours `json:"quietHours"`
}

func (s *Server) suggestions() (*suggestion.Store, error) {
	s.suggestOnce.Do(func() {
		s.suggestStore, s.suggestErr = suggestion.Open(filepath.Join(s.opts.SessionDir, "suggestions"))
	})
	return s.suggestStore, s.suggestErr
}

// decodeStrict decodes one JSON object with no unknown fields. An empty
// body decodes to the zero value when allowEmpty is set.
func decodeStrict(w http.ResponseWriter, r *http.Request, limit int64, v any, allowEmpty bool) error {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		if allowEmpty && errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	return ensureEOF(dec)
}

func suggestionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, suggestion.ErrInvalid):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, suggestion.ErrMuted),
		errors.Is(err, suggestion.ErrQuiet),
		errors.Is(err, suggestion.ErrDismissed):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.Is(err, suggestion.ErrNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, suggestion.ErrState):
		http.Error(w, err.Error(), http.StatusConflict)
	default:
		http.Error(w, "suggestions unavailable", http.StatusInternalServerError)
	}
}

func (s *Server) handleSuggestionsList(w http.ResponseWriter, r *http.Request) {
	store, err := s.suggestions()
	if err != nil {
		suggestionError(w, err)
		return
	}
	list, prefs, err := store.List()
	if err != nil {
		suggestionError(w, err)
		return
	}
	open := 0
	for _, sg := range list {
		if sg.State == suggestion.StateOpen {
			open++
		}
	}
	if list == nil {
		list = []suggestion.Suggestion{}
	}
	writeJSON(w, http.StatusOK, suggestionsResponse{Suggestions: list, Prefs: prefs, Open: open})
}

func (s *Server) handleSuggestionCreate(w http.ResponseWriter, r *http.Request) {
	var in suggestionCreateRequest
	if err := decodeStrict(w, r, suggestionBodyLimit, &in, false); err != nil {
		http.Error(w, "invalid suggestion", http.StatusBadRequest)
		return
	}
	if in.TTLHours < 0 {
		http.Error(w, "invalid suggestion: ttlHours", http.StatusBadRequest)
		return
	}
	store, err := s.suggestions()
	if err != nil {
		suggestionError(w, err)
		return
	}
	sg, created, err := store.Create(suggestion.Draft{
		Title:       in.Title,
		Why:         in.Why,
		Prompt:      in.Prompt,
		Source:      in.Source,
		Kind:        in.Kind,
		Effort:      in.Effort,
		EvidenceKey: in.EvidenceKey,
		Evidence:    in.Evidence,
		TTL:         time.Duration(in.TTLHours) * time.Hour,
	})
	if err != nil {
		suggestionError(w, err)
		return
	}
	code := http.StatusOK
	if created {
		code = http.StatusCreated
	}
	writeJSON(w, code, suggestionCreateResponse{Suggestion: sg, Created: created})
}

func suggestionID(r *http.Request) string {
	return strings.TrimSpace(r.PathValue("id"))
}

func (s *Server) handleSuggestionAccept(w http.ResponseWriter, r *http.Request) {
	store, err := s.suggestions()
	if err != nil {
		suggestionError(w, err)
		return
	}
	sg, err := store.Accept(suggestionID(r))
	if err != nil {
		suggestionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sg)
}

func (s *Server) handleSuggestionDismiss(w http.ResponseWriter, r *http.Request) {
	var in suggestionDismissRequest
	if err := decodeStrict(w, r, suggestionPrefLimit, &in, true); err != nil {
		http.Error(w, "invalid dismiss", http.StatusBadRequest)
		return
	}
	store, err := s.suggestions()
	if err != nil {
		suggestionError(w, err)
		return
	}
	sg, err := store.Dismiss(suggestionID(r), in.Never)
	if err != nil {
		suggestionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sg)
}

func (s *Server) handleSuggestionSnooze(w http.ResponseWriter, r *http.Request) {
	var in suggestionSnoozeRequest
	if err := decodeStrict(w, r, suggestionPrefLimit, &in, false); err != nil {
		http.Error(w, "invalid snooze", http.StatusBadRequest)
		return
	}
	if in.Hours <= 0 {
		http.Error(w, "invalid snooze: hours", http.StatusBadRequest)
		return
	}
	store, err := s.suggestions()
	if err != nil {
		suggestionError(w, err)
		return
	}
	sg, err := store.Snooze(suggestionID(r), time.Duration(in.Hours)*time.Hour)
	if err != nil {
		suggestionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sg)
}

func (s *Server) handleSuggestionPrefs(w http.ResponseWriter, r *http.Request) {
	var in suggestionPrefsRequest
	if err := decodeStrict(w, r, suggestionPrefLimit, &in, false); err != nil {
		http.Error(w, "invalid suggestion settings", http.StatusBadRequest)
		return
	}
	store, err := s.suggestions()
	if err != nil {
		suggestionError(w, err)
		return
	}
	prefs, err := store.SetPrefs(in.MutedSources, in.MutedKinds, in.QuietHours)
	if err != nil {
		suggestionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, prefs)
}
