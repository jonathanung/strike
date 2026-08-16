package server

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jonathanung/strike-cli/internal/frontend/host"
)

// Wire DTOs — no internal store paths or Go types leak to the browser.

type artifactMetaDTO struct {
	ID           string     `json:"id"`
	Type         string     `json:"type"`
	Title        string     `json:"title,omitempty"`
	Version      int        `json:"version"`
	Scope        string     `json:"scope"`
	SessionID    string     `json:"sessionId,omitempty"`
	Access       string     `json:"access"`
	OwnerSession string     `json:"ownerSession"`
	OwnerRoot    string     `json:"ownerRoot"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
	ExpiresAt    *time.Time `json:"expiresAt,omitempty"`
}

type artifactDTO struct {
	artifactMetaDTO
	Content string `json:"content"`
}

type ledgerEntryDTO struct {
	ID                 string     `json:"id"`
	Kind               string     `json:"kind"`
	Statement          string     `json:"statement"`
	Confidence         string     `json:"confidence,omitempty"`
	EvidenceRefs       []string   `json:"evidenceRefs,omitempty"`
	Status             string     `json:"status"`
	ScopePaths         []string   `json:"scopePaths,omitempty"`
	ScopeTaskIDs       []string   `json:"scopeTaskIds,omitempty"`
	AuthorSession      string     `json:"authorSession"`
	AuthorAgent        string     `json:"authorAgent,omitempty"`
	AuthorRoot         string     `json:"authorRoot,omitempty"`
	CreatedAt          time.Time  `json:"createdAt"`
	UpdatedAt          time.Time  `json:"updatedAt"`
	InvalidateReason   string     `json:"invalidateReason,omitempty"`
	InvalidateEvidence []string   `json:"invalidateEvidence,omitempty"`
	InvalidatedAt      *time.Time `json:"invalidatedAt,omitempty"`
	SupersededBy       string     `json:"supersededBy,omitempty"`
	Supersedes         string     `json:"supersedes,omitempty"`
}

func (s *Server) artifactsService() host.Artifacts {
	if s.opts.Services == nil {
		return nil
	}
	return s.opts.Services.Artifacts
}

func (s *Server) ledgerService() host.Ledger {
	if s.opts.Services == nil {
		return nil
	}
	return s.opts.Services.Ledger
}

// actorFromRequest resolves the browser actor for artifact visibility.
// Prefer explicit query params; fall back to the live root session id.
func (s *Server) actorFromRequest(r *http.Request) (sessionID, rootID string) {
	q := r.URL.Query()
	sessionID = strings.TrimSpace(q.Get("actorSession"))
	rootID = strings.TrimSpace(q.Get("actorRoot"))
	if rootID == "" {
		rootID = rootParam(r)
	}
	if sessionID == "" {
		sessionID = rootID
	}
	if sessionID == "" {
		if live := s.liveForRequest(r); live != nil {
			st := live.Status()
			if st.SessionID != "" {
				sessionID = st.SessionID
				if rootID == "" {
					rootID = st.SessionID
				}
			}
		}
	}
	if rootID == "" {
		rootID = sessionID
	}
	return sessionID, rootID
}

func (s *Server) handleArtifactsList(w http.ResponseWriter, r *http.Request) {
	svc := s.artifactsService()
	if svc == nil {
		capabilityUnavailable(w, "artifacts")
		return
	}
	actorSession, actorRoot := s.actorFromRequest(r)
	if actorSession == "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "actorSession or root is required"})
		return
	}
	q := r.URL.Query()
	filter := host.ArtifactListFilter{
		Type:           q.Get("type"),
		Scope:          q.Get("scope"),
		SessionID:      q.Get("sessionId"),
		IncludeExpired: q.Get("includeExpired") == "1" || strings.EqualFold(q.Get("includeExpired"), "true"),
		Limit:          atoiDefault(q.Get("limit"), 0),
		Offset:         atoiDefault(q.Get("offset"), 0),
	}
	items, err := svc.List(actorSession, actorRoot, filter)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	out := make([]artifactMetaDTO, 0, len(items))
	for _, m := range items {
		out = append(out, toArtifactMetaDTO(m))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"artifacts":    out,
		"actorSession": actorSession,
		"actorRoot":    actorRoot,
		"limit":        filter.Limit,
		"offset":       filter.Offset,
	})
}

func (s *Server) handleArtifactGet(w http.ResponseWriter, r *http.Request) {
	svc := s.artifactsService()
	if svc == nil {
		capabilityUnavailable(w, "artifacts")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "artifact id is required"})
		return
	}
	actorSession, actorRoot := s.actorFromRequest(r)
	if actorSession == "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "actorSession or root is required"})
		return
	}
	var (
		art host.Artifact
		ok  bool
		err error
	)
	if v := strings.TrimSpace(r.URL.Query().Get("version")); v != "" {
		ver, convErr := strconv.Atoi(v)
		if convErr != nil || ver < 1 {
			writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "version must be a positive integer"})
			return
		}
		art, ok, err = svc.GetVersion(id, ver, actorSession, actorRoot)
	} else {
		art, ok, err = svc.Get(id, actorSession, actorRoot)
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, opErrorResponse{Error: "artifact not found"})
		return
	}
	writeJSON(w, http.StatusOK, toArtifactDTO(art))
}

func (s *Server) handleLedgerList(w http.ResponseWriter, r *http.Request) {
	svc := s.ledgerService()
	if svc == nil {
		capabilityUnavailable(w, "ledger")
		return
	}
	q := r.URL.Query()
	// active=1 selects the active-slice semantics (context bundle).
	if q.Get("active") == "1" || strings.EqualFold(q.Get("active"), "true") {
		items, err := svc.ActiveSlice(q.Get("path"), q.Get("taskId"))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"entries": toLedgerDTOs(items), "slice": "active"})
		return
	}
	filter := host.LedgerListFilter{
		Status:        q.Get("status"),
		Kind:          q.Get("kind"),
		Path:          q.Get("path"),
		TaskID:        q.Get("taskId"),
		AuthorSession: q.Get("authorSession"),
		Limit:         atoiDefault(q.Get("limit"), 0),
		Offset:        atoiDefault(q.Get("offset"), 0),
	}
	items, err := svc.List(filter)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"entries": toLedgerDTOs(items),
		"slice":   "history",
		"limit":   filter.Limit,
		"offset":  filter.Offset,
	})
}

func (s *Server) handleLedgerGet(w http.ResponseWriter, r *http.Request) {
	svc := s.ledgerService()
	if svc == nil {
		capabilityUnavailable(w, "ledger")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "ledger id is required"})
		return
	}
	e, ok, err := svc.Get(id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: err.Error()})
		return
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, opErrorResponse{Error: "ledger entry not found"})
		return
	}
	writeJSON(w, http.StatusOK, toLedgerDTO(e))
}

func toArtifactMetaDTO(m host.ArtifactMeta) artifactMetaDTO {
	return artifactMetaDTO{
		ID:           m.ID,
		Type:         m.Type,
		Title:        m.Title,
		Version:      m.Version,
		Scope:        m.Scope,
		SessionID:    m.SessionID,
		Access:       m.Access,
		OwnerSession: m.OwnerSession,
		OwnerRoot:    m.OwnerRoot,
		CreatedAt:    m.CreatedAt,
		UpdatedAt:    m.UpdatedAt,
		ExpiresAt:    m.ExpiresAt,
	}
}

func toArtifactDTO(a host.Artifact) artifactDTO {
	return artifactDTO{
		artifactMetaDTO: toArtifactMetaDTO(host.ArtifactMeta{
			ID:           a.ID,
			Type:         a.Type,
			Title:        a.Title,
			Version:      a.Version,
			Scope:        a.Scope,
			SessionID:    a.SessionID,
			Access:       a.Access,
			OwnerSession: a.OwnerSession,
			OwnerRoot:    a.OwnerRoot,
			CreatedAt:    a.CreatedAt,
			UpdatedAt:    a.UpdatedAt,
			ExpiresAt:    a.ExpiresAt,
		}),
		Content: a.Content,
	}
}

func toLedgerDTOs(items []host.LedgerEntry) []ledgerEntryDTO {
	out := make([]ledgerEntryDTO, 0, len(items))
	for _, e := range items {
		out = append(out, toLedgerDTO(e))
	}
	return out
}

func toLedgerDTO(e host.LedgerEntry) ledgerEntryDTO {
	return ledgerEntryDTO{
		ID:                 e.ID,
		Kind:               e.Kind,
		Statement:          e.Statement,
		Confidence:         e.Confidence,
		EvidenceRefs:       e.EvidenceRefs,
		Status:             e.Status,
		ScopePaths:         e.ScopePaths,
		ScopeTaskIDs:       e.ScopeTaskIDs,
		AuthorSession:      e.AuthorSession,
		AuthorAgent:        e.AuthorAgent,
		AuthorRoot:         e.AuthorRoot,
		CreatedAt:          e.CreatedAt,
		UpdatedAt:          e.UpdatedAt,
		InvalidateReason:   e.InvalidateReason,
		InvalidateEvidence: e.InvalidateEvidence,
		InvalidatedAt:      e.InvalidatedAt,
		SupersededBy:       e.SupersededBy,
		Supersedes:         e.Supersedes,
	}
}

func atoiDefault(s string, def int) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}
