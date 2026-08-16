package server

import (
	"net/http"
	"strings"

	"github.com/jonathanung/strike-cli/internal/frontend/host"
)

// Web-safe plugin DTOs (camelCase JSON). No TUI types; no secret/env values.

type pluginMCPDTO struct {
	Name       string   `json:"name"`
	Transport  string   `json:"transport,omitempty"`
	Command    string   `json:"command,omitempty"`
	Args       []string `json:"args,omitempty"`
	EnvKeys    []string `json:"envKeys,omitempty"`
	URL        string   `json:"url,omitempty"`
	HeaderKeys []string `json:"headerKeys,omitempty"`
}

type pluginHarnessDTO struct {
	Name    string   `json:"name"`
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
}

type pluginDTO struct {
	ID              string             `json:"id"`
	Version         string             `json:"version,omitempty"`
	Name            string             `json:"name,omitempty"`
	Scope           string             `json:"scope,omitempty"`
	Enabled         bool               `json:"enabled"`
	Status          string             `json:"status,omitempty"`
	Digest          string             `json:"digest,omitempty"`
	SourceType      string             `json:"sourceType,omitempty"`
	SourceLabel     string             `json:"sourceLabel,omitempty"`
	TrustState      string             `json:"trustState,omitempty"`
	LoadError       string             `json:"loadError,omitempty"`
	Agents          int                `json:"agents"`
	Skills          int                `json:"skills"`
	Workflows       int                `json:"workflows"`
	Themes          int                `json:"themes"`
	Providers       int                `json:"providers"`
	Hooks           int                `json:"hooks"`
	Panes           int                `json:"panes"`
	MCP             []pluginMCPDTO     `json:"mcp,omitempty"`
	Harnesses       []pluginHarnessDTO `json:"harnesses,omitempty"`
	Capabilities    []string           `json:"capabilities,omitempty"`
	HasExecutable   bool               `json:"hasExecutable"`
	Findings        []string           `json:"findings,omitempty"`
	UpdateAvailable string             `json:"updateAvailable,omitempty"`
}

type pluginCatalogHitDTO struct {
	ID           string   `json:"id"`
	Name         string   `json:"name,omitempty"`
	Description  string   `json:"description,omitempty"`
	Version      string   `json:"version,omitempty"`
	Registry     string   `json:"registry,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
}

type pluginTrustPreviewDTO struct {
	ID           string             `json:"id"`
	Scope        string             `json:"scope,omitempty"`
	Digest       string             `json:"digest,omitempty"`
	Capabilities []string           `json:"capabilities,omitempty"`
	MCP          []pluginMCPDTO     `json:"mcp,omitempty"`
	Harnesses    []pluginHarnessDTO `json:"harnesses,omitempty"`
	Hooks        int                `json:"hooks"`
	ReviewLines  []string           `json:"reviewLines,omitempty"`
}

type pluginUpdateReviewDTO struct {
	ID                string   `json:"id"`
	OldVersion        string   `json:"oldVersion,omitempty"`
	NewVersion        string   `json:"newVersion,omitempty"`
	OldDigest         string   `json:"oldDigest,omitempty"`
	NewDigest         string   `json:"newDigest,omitempty"`
	SourceLabel       string   `json:"sourceLabel,omitempty"`
	CapabilityAdded   []string `json:"capabilityAdded,omitempty"`
	CapabilityRemoved []string `json:"capabilityRemoved,omitempty"`
	ContribAdded      []string `json:"contribAdded,omitempty"`
	ContribRemoved    []string `json:"contribRemoved,omitempty"`
	ExecutableChanged bool     `json:"executableChanged"`
	ExecutableDiffs   []string `json:"executableDiffs,omitempty"`
	TrustInvalidated  bool     `json:"trustInvalidated"`
	HadTrust          bool     `json:"hadTrust"`
	Summary           string   `json:"summary,omitempty"`
}

type pluginInstallResultDTO struct {
	ID      string `json:"id"`
	Version string `json:"version,omitempty"`
	Scope   string `json:"scope,omitempty"`
	Digest  string `json:"digest,omitempty"`
	Enabled bool   `json:"enabled"`
}

func (s *Server) plugins() host.Plugins {
	if s.opts.Services == nil {
		return nil
	}
	return s.opts.Services.Plugins
}

// requirePluginMutations gates lifecycle writes on the Plugins capability.
// Plugin install roots are host config (not engine state), so attach-only
// hosts may still mutate when Services.Plugins is wired.
func (s *Server) requirePluginMutations(w http.ResponseWriter) host.Plugins {
	p := s.plugins()
	if p == nil {
		capabilityUnavailable(w, "plugins")
		return nil
	}
	return p
}

func toPluginMCPDTO(m host.PluginMCP) pluginMCPDTO {
	return pluginMCPDTO{
		Name:       m.Name,
		Transport:  m.Transport,
		Command:    m.Command,
		Args:       append([]string(nil), m.Args...),
		EnvKeys:    append([]string(nil), m.EnvKeys...),
		URL:        m.URL,
		HeaderKeys: append([]string(nil), m.HeaderKeys...),
	}
}

func toPluginHarnessDTO(h host.PluginHarness) pluginHarnessDTO {
	return pluginHarnessDTO{
		Name:    h.Name,
		Command: h.Command,
		Args:    append([]string(nil), h.Args...),
	}
}

func toPluginDTO(p host.PluginInfo) pluginDTO {
	out := pluginDTO{
		ID:              p.ID,
		Version:         p.Version,
		Name:            p.Name,
		Scope:           p.Scope,
		Enabled:         p.Enabled,
		Status:          p.Status,
		Digest:          p.Digest,
		SourceType:      p.SourceType,
		SourceLabel:     p.SourceLabel,
		TrustState:      p.TrustState,
		LoadError:       p.LoadError,
		Agents:          p.Agents,
		Skills:          p.Skills,
		Workflows:       p.Workflows,
		Themes:          p.Themes,
		Providers:       p.Providers,
		Hooks:           p.Hooks,
		Panes:           p.Panes,
		Capabilities:    append([]string(nil), p.Capabilities...),
		HasExecutable:   p.HasExecutable,
		Findings:        append([]string(nil), p.Findings...),
		UpdateAvailable: p.UpdateAvailable,
	}
	if len(p.MCP) > 0 {
		out.MCP = make([]pluginMCPDTO, len(p.MCP))
		for i, m := range p.MCP {
			out.MCP[i] = toPluginMCPDTO(m)
		}
	}
	if len(p.Harnesses) > 0 {
		out.Harnesses = make([]pluginHarnessDTO, len(p.Harnesses))
		for i, h := range p.Harnesses {
			out.Harnesses[i] = toPluginHarnessDTO(h)
		}
	}
	return out
}

func toPluginDTOs(list []host.PluginInfo) []pluginDTO {
	out := make([]pluginDTO, 0, len(list))
	for _, p := range list {
		out = append(out, toPluginDTO(p))
	}
	return out
}

// pluginIDScope resolves id from path (preferred) or JSON body, and scope from
// query or body. Empty body is OK for path-based mutations.
func pluginIDScope(w http.ResponseWriter, r *http.Request, bodyID, bodyScope string) (id, scope string, ok bool) {
	id = strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		id = strings.TrimSpace(bodyID)
	}
	if id == "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "id is required"})
		return "", "", false
	}
	scope = strings.TrimSpace(bodyScope)
	if scope == "" {
		scope = strings.TrimSpace(r.URL.Query().Get("scope"))
	}
	return id, scope, true
}

func (s *Server) handlePluginsList(w http.ResponseWriter, r *http.Request) {
	p := s.plugins()
	if p == nil {
		capabilityUnavailable(w, "plugins")
		return
	}
	list, err := p.List()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	if list == nil {
		list = []host.PluginInfo{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"plugins": toPluginDTOs(list)})
}

func (s *Server) handlePluginGet(w http.ResponseWriter, r *http.Request) {
	p := s.plugins()
	if p == nil {
		capabilityUnavailable(w, "plugins")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "id is required"})
		return
	}
	scope := strings.TrimSpace(r.URL.Query().Get("scope"))
	info, err := p.Inspect(id, scope)
	if err != nil {
		writeJSON(w, http.StatusNotFound, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, toPluginDTO(info))
}

func (s *Server) handlePluginEnable(w http.ResponseWriter, r *http.Request) {
	p := s.requirePluginMutations(w)
	if p == nil {
		return
	}
	var body struct {
		ID    string `json:"id"`
		Scope string `json:"scope"`
	}
	_ = decodeOptionalBody(w, r, &body)
	id, scope, ok := pluginIDScope(w, r, body.ID, body.Scope)
	if !ok {
		return
	}
	if err := p.Enable(id, scope); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, opOKResponse{OK: true})
}

func (s *Server) handlePluginDisable(w http.ResponseWriter, r *http.Request) {
	p := s.requirePluginMutations(w)
	if p == nil {
		return
	}
	var body struct {
		ID    string `json:"id"`
		Scope string `json:"scope"`
	}
	_ = decodeOptionalBody(w, r, &body)
	id, scope, ok := pluginIDScope(w, r, body.ID, body.Scope)
	if !ok {
		return
	}
	if err := p.Disable(id, scope); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	if s.paneHost != nil {
		s.paneHost.unmountPlugin(id)
	}
	writeJSON(w, http.StatusOK, opOKResponse{OK: true})
}

func (s *Server) handlePluginRemove(w http.ResponseWriter, r *http.Request) {
	p := s.requirePluginMutations(w)
	if p == nil {
		return
	}
	var body struct {
		ID      string `json:"id"`
		Scope   string `json:"scope"`
		Confirm bool   `json:"confirm"`
	}
	if err := decodeOptionalBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	id, scope, ok := pluginIDScope(w, r, body.ID, body.Scope)
	if !ok {
		return
	}
	if !body.Confirm {
		writeJSON(w, http.StatusUnprocessableEntity, opErrorResponse{Error: "confirm:true required to remove a plugin"})
		return
	}
	if err := p.Remove(id, scope, true); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	if s.paneHost != nil {
		s.paneHost.unmountPlugin(id)
	}
	writeJSON(w, http.StatusOK, opOKResponse{OK: true})
}

func (s *Server) handlePluginTrustPreview(w http.ResponseWriter, r *http.Request) {
	p := s.plugins()
	if p == nil {
		capabilityUnavailable(w, "plugins")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "id is required"})
		return
	}
	scope := strings.TrimSpace(r.URL.Query().Get("scope"))
	prev, err := p.TrustPreview(id, scope)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	out := pluginTrustPreviewDTO{
		ID:           prev.ID,
		Scope:        prev.Scope,
		Digest:       prev.Digest,
		Capabilities: append([]string(nil), prev.Capabilities...),
		Hooks:        prev.Hooks,
		ReviewLines:  append([]string(nil), prev.ReviewLines...),
	}
	if len(prev.MCP) > 0 {
		out.MCP = make([]pluginMCPDTO, len(prev.MCP))
		for i, m := range prev.MCP {
			out.MCP[i] = toPluginMCPDTO(m)
		}
	}
	if len(prev.Harnesses) > 0 {
		out.Harnesses = make([]pluginHarnessDTO, len(prev.Harnesses))
		for i, h := range prev.Harnesses {
			out.Harnesses[i] = toPluginHarnessDTO(h)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handlePluginTrust(w http.ResponseWriter, r *http.Request) {
	p := s.requirePluginMutations(w)
	if p == nil {
		return
	}
	var body struct {
		ID    string `json:"id"`
		Scope string `json:"scope"`
	}
	_ = decodeOptionalBody(w, r, &body)
	id, scope, ok := pluginIDScope(w, r, body.ID, body.Scope)
	if !ok {
		return
	}
	// Capability review must succeed before grant (TUI confirm parity).
	if _, err := p.TrustPreview(id, scope); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	if err := p.Trust(id, scope); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, opOKResponse{OK: true})
}

func (s *Server) handlePluginUntrust(w http.ResponseWriter, r *http.Request) {
	p := s.requirePluginMutations(w)
	if p == nil {
		return
	}
	var body struct {
		ID    string `json:"id"`
		Scope string `json:"scope"`
	}
	_ = decodeOptionalBody(w, r, &body)
	id, scope, ok := pluginIDScope(w, r, body.ID, body.Scope)
	if !ok {
		return
	}
	if err := p.Untrust(id, scope); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	if s.paneHost != nil {
		s.paneHost.unmountPlugin(id)
	}
	writeJSON(w, http.StatusOK, opOKResponse{OK: true})
}

func (s *Server) handlePluginSearch(w http.ResponseWriter, r *http.Request) {
	p := s.plugins()
	if p == nil {
		capabilityUnavailable(w, "plugins")
		return
	}
	var body struct {
		Registry string `json:"registry"`
		Query    string `json:"query"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	hits, err := p.Search(r.Context(), strings.TrimSpace(body.Registry), body.Query)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	out := make([]pluginCatalogHitDTO, 0, len(hits))
	for _, h := range hits {
		out = append(out, pluginCatalogHitDTO{
			ID:           h.ID,
			Name:         h.Name,
			Description:  h.Description,
			Version:      h.Version,
			Registry:     h.Registry,
			Capabilities: append([]string(nil), h.Capabilities...),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"hits": out})
}

func (s *Server) handlePluginInstall(w http.ResponseWriter, r *http.Request) {
	p := s.requirePluginMutations(w)
	if p == nil {
		return
	}
	var body struct {
		Source   string `json:"source"`
		Scope    string `json:"scope"`
		Registry string `json:"registry"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	src := strings.TrimSpace(body.Source)
	if src == "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "source is required"})
		return
	}
	res, err := p.Install(r.Context(), src, strings.TrimSpace(body.Scope), strings.TrimSpace(body.Registry))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, pluginInstallResultDTO{
		ID: res.ID, Version: res.Version, Scope: res.Scope, Digest: res.Digest, Enabled: res.Enabled,
	})
}

func (s *Server) handlePluginOutdated(w http.ResponseWriter, r *http.Request) {
	p := s.plugins()
	if p == nil {
		capabilityUnavailable(w, "plugins")
		return
	}
	registry := strings.TrimSpace(r.URL.Query().Get("registry"))
	list, err := p.CheckOutdated(r.Context(), registry)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	if list == nil {
		list = []host.PluginInfo{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"plugins": toPluginDTOs(list)})
}

func (s *Server) handlePluginPreviewUpdate(w http.ResponseWriter, r *http.Request) {
	p := s.plugins()
	if p == nil {
		capabilityUnavailable(w, "plugins")
		return
	}
	var body struct {
		ID       string `json:"id"`
		Scope    string `json:"scope"`
		Registry string `json:"registry"`
	}
	_ = decodeOptionalBody(w, r, &body)
	id, scope, ok := pluginIDScope(w, r, body.ID, body.Scope)
	if !ok {
		return
	}
	registry := strings.TrimSpace(body.Registry)
	if registry == "" {
		registry = strings.TrimSpace(r.URL.Query().Get("registry"))
	}
	rev, err := p.PreviewUpdate(r.Context(), id, scope, registry)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, pluginUpdateReviewDTO{
		ID:                rev.ID,
		OldVersion:        rev.OldVersion,
		NewVersion:        rev.NewVersion,
		OldDigest:         rev.OldDigest,
		NewDigest:         rev.NewDigest,
		SourceLabel:       rev.SourceLabel,
		CapabilityAdded:   append([]string(nil), rev.CapabilityAdded...),
		CapabilityRemoved: append([]string(nil), rev.CapabilityRemoved...),
		ContribAdded:      append([]string(nil), rev.ContribAdded...),
		ContribRemoved:    append([]string(nil), rev.ContribRemoved...),
		ExecutableChanged: rev.ExecutableChanged,
		ExecutableDiffs:   append([]string(nil), rev.ExecutableDiffs...),
		TrustInvalidated:  rev.TrustInvalidated,
		HadTrust:          rev.HadTrust,
		Summary:           rev.Summary,
	})
}

func (s *Server) handlePluginUpdate(w http.ResponseWriter, r *http.Request) {
	p := s.requirePluginMutations(w)
	if p == nil {
		return
	}
	var body struct {
		ID       string `json:"id"`
		Scope    string `json:"scope"`
		Registry string `json:"registry"`
		Confirm  bool   `json:"confirm"`
	}
	if err := decodeOptionalBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	id, scope, ok := pluginIDScope(w, r, body.ID, body.Scope)
	if !ok {
		return
	}
	if !body.Confirm {
		writeJSON(w, http.StatusUnprocessableEntity, opErrorResponse{Error: "confirm:true required after update review"})
		return
	}
	registry := strings.TrimSpace(body.Registry)
	if _, err := p.PreviewUpdate(r.Context(), id, scope, registry); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	res, err := p.Update(r.Context(), id, scope, registry, true)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	if s.paneHost != nil {
		s.paneHost.unmountPlugin(id)
	}
	writeJSON(w, http.StatusOK, pluginInstallResultDTO{
		ID: res.ID, Version: res.Version, Scope: res.Scope, Digest: res.Digest, Enabled: res.Enabled,
	})
}
