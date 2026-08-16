package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/jonathanung/strike-cli/internal/frontend/host"
)

// Web-safe pane DTOs. No TUI types, no PluginRoot, no env secret values.

type paneDTO struct {
	ID            string         `json:"id"`
	PluginID      string         `json:"pluginId"`
	PluginVersion string         `json:"pluginVersion,omitempty"`
	Scope         string         `json:"scope,omitempty"`
	Title         string         `json:"title,omitempty"`
	Mode          string         `json:"mode"`
	Trusted       bool           `json:"trusted"`
	LoadError     string         `json:"loadError,omitempty"`
	Provenance    string         `json:"provenance,omitempty"`
	Definition    map[string]any `json:"definition,omitempty"`
}

type paneSnapshotDTO struct {
	ID      string          `json:"id"`
	Title   string          `json:"title,omitempty"`
	Status  string          `json:"status,omitempty"`
	Error   string          `json:"error,omitempty"`
	Mode    string          `json:"mode"`
	View    json.RawMessage `json:"view,omitempty"`
	Feeds   map[string]any  `json:"feeds,omitempty"`
	Rev     int64           `json:"rev,omitempty"`
	Mounted bool            `json:"mounted,omitempty"`
}

func (s *Server) panes() host.Panes {
	if s.opts.Services == nil {
		return nil
	}
	return s.opts.Services.Panes
}

// sanitizePaneDefinition strips secret-bearing fields and absolute host paths
// from a pane definition before it crosses the web boundary.
func sanitizePaneDefinition(raw []byte) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	// Env values may hold resolved secrets — expose keys only.
	if env, ok := m["env"].(map[string]any); ok && len(env) > 0 {
		keys := make([]string, 0, len(env))
		for k := range env {
			keys = append(keys, k)
		}
		m["envKeys"] = keys
	}
	delete(m, "env")
	return m
}

func toPaneDTO(p host.PaneInfo) paneDTO {
	return paneDTO{
		ID:            p.ID,
		PluginID:      p.PluginID,
		PluginVersion: p.PluginVersion,
		Scope:         p.Scope,
		Title:         p.Title,
		Mode:          p.Mode,
		Trusted:       p.Trusted,
		LoadError:     p.LoadError,
		Provenance:    p.Provenance(),
		Definition:    sanitizePaneDefinition(p.DefinitionJSON),
	}
}

func (s *Server) findPane(id string) (host.PaneInfo, bool, error) {
	p := s.panes()
	if p == nil {
		return host.PaneInfo{}, false, nil
	}
	list, err := p.List()
	if err != nil {
		return host.PaneInfo{}, false, err
	}
	id = strings.TrimSpace(id)
	for _, info := range list {
		if info.ID == id {
			return info, true, nil
		}
	}
	return host.PaneInfo{}, false, nil
}

func (s *Server) handlePanesList(w http.ResponseWriter, r *http.Request) {
	p := s.panes()
	if p == nil {
		capabilityUnavailable(w, "panes")
		return
	}
	list, err := p.List()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	out := make([]paneDTO, 0, len(list))
	for _, info := range list {
		out = append(out, toPaneDTO(info))
	}
	writeJSON(w, http.StatusOK, map[string]any{"panes": out})
}

func (s *Server) handlePaneGet(w http.ResponseWriter, r *http.Request) {
	if s.panes() == nil {
		capabilityUnavailable(w, "panes")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	info, ok, err := s.findPane(id)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, opErrorResponse{Error: "pane not found"})
		return
	}
	writeJSON(w, http.StatusOK, toPaneDTO(info))
}

func (s *Server) handlePaneSnapshot(w http.ResponseWriter, r *http.Request) {
	if s.panes() == nil {
		capabilityUnavailable(w, "panes")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	info, ok, err := s.findPane(id)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, opErrorResponse{Error: "pane not found"})
		return
	}
	snap := s.buildPaneSnapshot(r, info)
	writeJSON(w, http.StatusOK, snap)
}

func (s *Server) buildPaneSnapshot(r *http.Request, info host.PaneInfo) paneSnapshotDTO {
	snap := paneSnapshotDTO{
		ID:    info.ID,
		Title: info.Title,
		Mode:  info.Mode,
	}
	if info.LoadError != "" {
		snap.Error = info.LoadError
	}
	feeds := s.paneFeeds(r)
	snap.Feeds = feeds

	if info.Mode == host.PaneModeProcess || strings.EqualFold(info.Mode, "process") {
		if s.paneHost != nil {
			if st, ok := s.paneHost.snapshot(info.ID); ok {
				snap.Title = st.Title
				snap.Status = st.Status
				snap.Error = firstNonEmpty(st.Error, snap.Error)
				snap.View = st.View
				snap.Rev = st.Rev
				snap.Mounted = st.Mounted
				return snap
			}
		}
		if snap.Error == "" && !info.Trusted {
			snap.Error = "process pane blocked until plugin trust is granted"
		}
		return snap
	}

	// Static: expose definition view; client resolves valueFrom against feeds.
	if len(info.DefinitionJSON) > 0 {
		var def struct {
			Title string          `json:"title"`
			View  json.RawMessage `json:"view"`
		}
		if err := json.Unmarshal(info.DefinitionJSON, &def); err == nil {
			if def.Title != "" {
				snap.Title = def.Title
			}
			if len(def.View) > 0 {
				snap.View = def.View
			}
		}
	}
	return snap
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// paneFeeds builds host data feed snapshots for pane bindings (docs/plugin-panes.md §7).
func (s *Server) paneFeeds(r *http.Request) map[string]any {
	feeds := map[string]any{}
	// clock always available
	feeds["clock"] = map[string]any{
		"unix": time.Now().Unix(),
		"iso":  time.Now().UTC().Format(time.RFC3339),
	}
	// session.summary + usage from live status when present
	var st *StatusSnapshot
	if s.opts.LiveHub != nil {
		if live := s.opts.LiveHub.LiveFor(rootParam(r)); live != nil {
			v := live.Status()
			st = &v
		}
	} else if s.opts.Live != nil {
		v := s.opts.Live.Status()
		st = &v
	}
	if st != nil {
		feeds["session.summary"] = map[string]any{
			"cwd":            st.CWD,
			"sessionId":      st.SessionID,
			"provider":       st.Provider,
			"model":          st.Model,
			"agent":          st.Agent,
			"agentState":     busyState(st.Busy),
			"effort":         st.Effort,
			"autonomy":       st.Autonomy,
			"permissionMode": st.PermissionMode,
			"workflow":       st.Workflow,
			"phase":          st.Phase,
		}
		feeds["usage"] = map[string]any{
			"contextUsed":  st.ContextUsed,
			"contextLimit": st.ContextLimit,
		}
	} else {
		feeds["session.summary"] = map[string]any{}
		feeds["usage"] = map[string]any{}
	}
	// agents.roster — empty list when no live roster surface
	feeds["agents.roster"] = map[string]any{"agents": []any{}}
	if s.opts.Services != nil && len(s.opts.Services.Agents) > 0 {
		agents := make([]map[string]any, 0, len(s.opts.Services.Agents))
		for _, name := range s.opts.Services.Agents {
			agents = append(agents, map[string]any{"name": name})
		}
		feeds["agents.roster"] = map[string]any{"agents": agents}
	}
	return feeds
}

func busyState(busy bool) string {
	if busy {
		return "busy"
	}
	return "idle"
}

func (s *Server) handlePaneMount(w http.ResponseWriter, r *http.Request) {
	if s.panes() == nil {
		capabilityUnavailable(w, "panes")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	info, ok, err := s.findPane(id)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, opErrorResponse{Error: "pane not found"})
		return
	}
	var body struct {
		Width  int `json:"width"`
		Height int `json:"height"`
	}
	_ = decodeOptionalBody(w, r, &body)
	if body.Width <= 0 {
		body.Width = 40
	}
	if body.Height <= 0 {
		body.Height = 14
	}
	if info.Mode == host.PaneModeStatic || strings.EqualFold(info.Mode, "static") {
		// Static panes need no process; snapshot is enough.
		snap := s.buildPaneSnapshot(r, info)
		snap.Mounted = true
		writeJSON(w, http.StatusOK, snap)
		return
	}
	if !info.Trusted || info.LoadError != "" {
		msg := info.LoadError
		if msg == "" {
			msg = "process pane blocked until plugin trust is granted"
		}
		writeJSON(w, http.StatusConflict, opErrorResponse{Error: msg})
		return
	}
	if s.paneHost == nil {
		s.paneHost = newPaneHost()
	}
	if err := s.paneHost.mount(info, body.Width, body.Height); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	// Push initial feeds.
	_ = s.paneHost.pushFeeds(info.ID, s.paneFeeds(r))
	writeJSON(w, http.StatusOK, s.buildPaneSnapshot(r, info))
}

func (s *Server) handlePaneUnmount(w http.ResponseWriter, r *http.Request) {
	if s.panes() == nil {
		capabilityUnavailable(w, "panes")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if s.paneHost != nil {
		s.paneHost.unmount(id, "unmount")
	}
	writeJSON(w, http.StatusOK, opOKResponse{OK: true})
}

func (s *Server) handlePaneInput(w http.ResponseWriter, r *http.Request) {
	if s.panes() == nil {
		capabilityUnavailable(w, "panes")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	var body struct {
		Event map[string]any `json:"event"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	if body.Event == nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "event is required"})
		return
	}
	if s.paneHost == nil {
		writeJSON(w, http.StatusConflict, opErrorResponse{Error: "pane not mounted"})
		return
	}
	if err := s.paneHost.input(id, body.Event); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, opOKResponse{OK: true})
}

func (s *Server) handlePaneResize(w http.ResponseWriter, r *http.Request) {
	if s.panes() == nil {
		capabilityUnavailable(w, "panes")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	var body struct {
		Width  int `json:"width"`
		Height int `json:"height"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	if s.paneHost == nil {
		writeJSON(w, http.StatusConflict, opErrorResponse{Error: "pane not mounted"})
		return
	}
	if err := s.paneHost.resize(id, body.Width, body.Height); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, opOKResponse{OK: true})
}
