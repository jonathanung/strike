package server

import (
	"errors"
	"net/http"
	"strings"

	"github.com/jonathanung/strike/internal/frontend/host"
	"github.com/jonathanung/strike/internal/integrate/powers"
)

// Powers routes (#1347 / #1867): list packs, suggest for a prompt, and
// activate / deactivate / mute per pack. Activation is always an explicit
// call for a live session; suggestion never activates.

func (s *Server) powers() host.Powers {
	if s.opts.Services == nil {
		return nil
	}
	return s.opts.Services.Powers
}

type powersListResponse struct {
	Packs      []host.PowerPack       `json:"packs"`
	LoadErrors []string               `json:"loadErrors"`
	Active     []host.PowerActivation `json:"active"`
}

// handlePowersList is GET /v1/powers?session=<id>.
func (s *Server) handlePowersList(w http.ResponseWriter, r *http.Request) {
	p := s.powers()
	if p == nil {
		capabilityUnavailable(w, "powers")
		return
	}
	packs, loadErrs := p.Packs()
	out := powersListResponse{Packs: packs, LoadErrors: loadErrs, Active: []host.PowerActivation{}}
	if out.Packs == nil {
		out.Packs = []host.PowerPack{}
	}
	if out.LoadErrors == nil {
		out.LoadErrors = []string{}
	}
	if sid := strings.TrimSpace(r.URL.Query().Get("session")); sid != "" {
		if act := p.Active(sid); act != nil {
			out.Active = act
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// handlePowersSuggest is POST /v1/powers/suggest {session, prompt}.
func (s *Server) handlePowersSuggest(w http.ResponseWriter, r *http.Request) {
	p := s.powers()
	if p == nil {
		capabilityUnavailable(w, "powers")
		return
	}
	var body struct {
		Session string `json:"session"`
		Prompt  string `json:"prompt"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	names := p.Suggest(strings.TrimSpace(body.Session), body.Prompt)
	if names == nil {
		names = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"suggestions": names})
}

type powersPackRequest struct {
	Session string `json:"session"`
	Name    string `json:"name"`
}

// decodePowersPack decodes and validates an activate/deactivate body.
// It writes the error response and returns false on failure.
func (s *Server) decodePowersPack(w http.ResponseWriter, r *http.Request) (powersPackRequest, bool) {
	var body powersPackRequest
	if s.opts.ReadOnly {
		writeJSON(w, http.StatusForbidden, opErrorResponse{Error: "read-only attach mode does not allow mutations"})
		return body, false
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return body, false
	}
	body.Session, body.Name = strings.TrimSpace(body.Session), strings.TrimSpace(body.Name)
	if body.Session == "" || body.Name == "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "session and name are required"})
		return body, false
	}
	return body, true
}

// sessionIsLive reports whether id names a live root or session. Without a
// LiveHub (single-root serve) every id is accepted.
func (s *Server) sessionIsLive(id string) bool {
	if s.opts.LiveHub == nil {
		return true
	}
	return s.opts.LiveHub.LiveFor(id) != nil || s.opts.LiveHub.LiveForSession(id) != nil
}

// handlePowersActivate is POST /v1/powers/activate {session, name}.
func (s *Server) handlePowersActivate(w http.ResponseWriter, r *http.Request) {
	p := s.powers()
	if p == nil {
		capabilityUnavailable(w, "powers")
		return
	}
	body, ok := s.decodePowersPack(w, r)
	if !ok {
		return
	}
	// Nothing activates outside a session the user is looking at.
	if !s.sessionIsLive(body.Session) {
		writeJSON(w, http.StatusNotFound, opErrorResponse{Error: "session is not live"})
		return
	}
	res, err := p.Activate(body.Session, body.Name)
	if err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, powers.ErrDenied) {
			code = http.StatusForbidden
		}
		writeJSON(w, code, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handlePowersDeactivate is POST /v1/powers/deactivate {session, name}.
func (s *Server) handlePowersDeactivate(w http.ResponseWriter, r *http.Request) {
	p := s.powers()
	if p == nil {
		capabilityUnavailable(w, "powers")
		return
	}
	body, ok := s.decodePowersPack(w, r)
	if !ok {
		return
	}
	if err := p.Deactivate(body.Session, body.Name); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, opOKResponse{OK: true})
}

// handlePowersMute is POST /v1/powers/mute {name, muted}.
func (s *Server) handlePowersMute(w http.ResponseWriter, r *http.Request) {
	p := s.powers()
	if p == nil {
		capabilityUnavailable(w, "powers")
		return
	}
	if s.opts.ReadOnly {
		writeJSON(w, http.StatusForbidden, opErrorResponse{Error: "read-only attach mode does not allow mutations"})
		return
	}
	var body struct {
		Name  string `json:"name"`
		Muted bool   `json:"muted"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "name is required"})
		return
	}
	if err := p.SetMuted(name, body.Muted); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, opOKResponse{OK: true})
}

// endPowers unloads every Power for the given root/session ids.
func (s *Server) endPowers(ids ...string) {
	p := s.powers()
	if p == nil {
		return
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" && !seen[id] {
			seen[id] = true
			p.EndSession(id)
		}
	}
}
