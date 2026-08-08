package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/pkg/redact"
)

// pendingAuth holds in-flight device/OAuth logins for the web cockpit (WEBUI.10).
// Tokens never leave the host; the browser only sees codes, URLs, and status.
type pendingAuth struct {
	ID              string
	Provider        string
	Kind            string // device | oauth
	UserCode        string
	VerificationURI string
	AuthorizeURL    string
	ExpiresAt       time.Time
	Status          string // pending | completed | failed | canceled | expired
	Message         string
	cancel          context.CancelFunc
	device          *host.DeviceLogin
	oauth           *host.OAuthLogin
}

type authFlowStore struct {
	mu   sync.Mutex
	byID map[string]*pendingAuth
}

func newAuthFlowStore() *authFlowStore {
	return &authFlowStore{byID: map[string]*pendingAuth{}}
}

func (s *authFlowStore) put(p *pendingAuth) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byID == nil {
		s.byID = map[string]*pendingAuth{}
	}
	s.byID[p.ID] = p
}

func (s *authFlowStore) get(id string) *pendingAuth {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.byID[id]
}

// snapshot copies public fields under the store lock.
func (s *authFlowStore) snapshot(id string) (pendingAuth, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.byID[id]
	if p == nil {
		return pendingAuth{}, false
	}
	if p.Status == "pending" && time.Now().After(p.ExpiresAt) {
		p.Status = "expired"
		p.Message = "device code expired"
		if p.cancel != nil {
			p.cancel()
		}
	}
	// Shallow copy of scalar fields (no goroutine handles).
	return pendingAuth{
		ID: p.ID, Provider: p.Provider, Kind: p.Kind,
		UserCode: p.UserCode, VerificationURI: p.VerificationURI,
		AuthorizeURL: p.AuthorizeURL, ExpiresAt: p.ExpiresAt,
		Status: p.Status, Message: p.Message,
	}, true
}

func (s *authFlowStore) update(id string, fn func(*pendingAuth)) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.byID[id]
	if p == nil {
		return false
	}
	fn(p)
	return true
}

func (s *authFlowStore) delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.byID, id)
}

func newFlowID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// redactAuthErr scrubs credential-shaped substrings from auth errors for the browser.
func redactAuthErr(err error) string {
	if err == nil {
		return ""
	}
	msg := redact.String(err.Error())
	// Bound length so huge provider HTML never floods the UI.
	if len(msg) > 280 {
		msg = msg[:280] + "…"
	}
	return msg
}

func providerStatusDTO(p host.ProviderStatus) map[string]any {
	out := map[string]any{
		"name":    p.Name,
		"detail":  p.Detail,
		"authed":  p.Authed,
		"builtin": p.Builtin,
		"custom":  p.Custom,
		"oauth":   p.OAuth,
		"device":  p.Device,
		"apiKey":  p.APIKey,
	}
	if p.WireAPI != "" {
		out["wireAPI"] = p.WireAPI
	}
	if p.BaseURL != "" {
		out["baseURL"] = p.BaseURL
	}
	if !p.ExpiresAt.IsZero() {
		out["expiresAt"] = p.ExpiresAt.UTC().Format(time.RFC3339)
	}
	// Methods the UI may offer (never includes credential values).
	methods := []string{}
	if p.APIKey {
		methods = append(methods, "apiKey")
	}
	if p.Device {
		methods = append(methods, "device")
	}
	if p.OAuth {
		methods = append(methods, "oauth")
	}
	out["methods"] = methods
	return out
}

func (s *Server) authFlows() *authFlowStore {
	s.authFlowOnce.Do(func() {
		s.authFlowsStore = newAuthFlowStore()
	})
	return s.authFlowsStore
}

// handleProviders lists provider auth status (full DTO, no secrets).
func (s *Server) handleProviders(w http.ResponseWriter, r *http.Request) {
	if s.opts.Services == nil || s.opts.Services.Auth == nil {
		capabilityUnavailable(w, "auth")
		return
	}
	statuses := s.opts.Services.Auth.Statuses()
	out := make([]map[string]any, 0, len(statuses))
	for _, p := range statuses {
		out = append(out, providerStatusDTO(p))
	}
	// OAuth browser-callback safety: only advertise paste-assisted OAuth.
	// Full redirect callback is not a safe remote browser flow.
	writeJSON(w, http.StatusOK, map[string]any{
		"providers": out,
		"oauthMode": "paste", // browser opens authorize URL; complete via paste if supported
		"oauthNote": "Browser OAuth uses the authorize URL plus optional paste completion. Remote hosts without a safe callback should use device flow or API key.",
	})
}

func (s *Server) handleAuthKey(w http.ResponseWriter, r *http.Request) {
	if s.opts.Services == nil || s.opts.Services.Auth == nil {
		capabilityUnavailable(w, "auth")
		return
	}
	if !s.requireMutable(w) {
		return
	}
	var body struct {
		Provider string `json:"provider"`
		Key      string `json:"key"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	if strings.TrimSpace(body.Provider) == "" || strings.TrimSpace(body.Key) == "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "provider and key are required"})
		return
	}
	if err := s.opts.Services.Auth.SetAPIKey(body.Provider, body.Key); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: redactAuthErr(err)})
		return
	}
	// Return refreshed status (never the key).
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"provider": providerStatusByName(s.opts.Services.Auth, body.Provider),
	})
}

func (s *Server) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	if s.opts.Services == nil || s.opts.Services.Auth == nil {
		capabilityUnavailable(w, "auth")
		return
	}
	if !s.requireMutable(w) {
		return
	}
	provider := strings.TrimSpace(r.PathValue("provider"))
	if provider == "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "provider is required"})
		return
	}
	if err := s.opts.Services.Auth.Logout(provider); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: redactAuthErr(err)})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"provider": providerStatusByName(s.opts.Services.Auth, provider),
	})
}

func providerStatusByName(auth host.Auth, name string) map[string]any {
	name = strings.TrimSpace(name)
	for _, p := range auth.Statuses() {
		if strings.EqualFold(p.Name, name) {
			return providerStatusDTO(p)
		}
	}
	return map[string]any{"name": name, "detail": "unknown", "authed": false, "methods": []string{}}
}

// POST /v1/auth/device — start RFC 8628 device login.
func (s *Server) handleAuthDeviceStart(w http.ResponseWriter, r *http.Request) {
	if s.opts.Services == nil || s.opts.Services.Auth == nil {
		capabilityUnavailable(w, "auth")
		return
	}
	if !s.requireMutable(w) {
		return
	}
	var body struct {
		Provider string `json:"provider"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	provider := strings.TrimSpace(body.Provider)
	if provider == "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "provider is required"})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	login, err := s.opts.Services.Auth.BeginDevice(ctx, provider)
	if err != nil {
		cancel()
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: redactAuthErr(err)})
		return
	}
	id := newFlowID()
	p := &pendingAuth{
		ID:              id,
		Provider:        provider,
		Kind:            "device",
		UserCode:        login.UserCode,
		VerificationURI: login.VerificationURI,
		ExpiresAt:       time.Now().Add(15 * time.Minute),
		Status:          "pending",
		cancel:          cancel,
		device:          login,
	}
	s.authFlows().put(p)
	go s.pollDevice(p)
	writeJSON(w, http.StatusOK, map[string]any{
		"id":              id,
		"provider":        provider,
		"kind":            "device",
		"userCode":        login.UserCode,
		"verificationUri": login.VerificationURI,
		"expiresAt":       p.ExpiresAt.UTC().Format(time.RFC3339),
		"status":          "pending",
	})
}

func (s *Server) pollDevice(p *pendingAuth) {
	defer p.cancel()
	msg, err := p.device.Poll(context.Background())
	s.authFlows().mu.Lock()
	defer s.authFlows().mu.Unlock()
	cur := s.authFlows().byID[p.ID]
	if cur == nil || cur.Status != "pending" {
		return
	}
	if err != nil {
		cur.Status = "failed"
		if strings.Contains(strings.ToLower(err.Error()), "cancel") {
			cur.Status = "canceled"
		}
		if strings.Contains(strings.ToLower(err.Error()), "expir") {
			cur.Status = "expired"
		}
		cur.Message = redactAuthErr(err)
		return
	}
	cur.Status = "completed"
	cur.Message = msg
}

// GET /v1/auth/device/{id}
func (s *Server) handleAuthDeviceStatus(w http.ResponseWriter, r *http.Request) {
	if s.opts.Services == nil || s.opts.Services.Auth == nil {
		capabilityUnavailable(w, "auth")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	p, ok := s.authFlows().snapshot(id)
	if !ok || p.Kind != "device" {
		writeJSON(w, http.StatusNotFound, opErrorResponse{Error: "device login not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":              p.ID,
		"provider":        p.Provider,
		"kind":            p.Kind,
		"userCode":        p.UserCode,
		"verificationUri": p.VerificationURI,
		"expiresAt":       p.ExpiresAt.UTC().Format(time.RFC3339),
		"status":          p.Status,
		"message":         p.Message,
		"providerStatus":  providerStatusByName(s.opts.Services.Auth, p.Provider),
	})
}

// DELETE /v1/auth/device/{id} — cancel
func (s *Server) handleAuthDeviceCancel(w http.ResponseWriter, r *http.Request) {
	if s.opts.Services == nil || s.opts.Services.Auth == nil {
		capabilityUnavailable(w, "auth")
		return
	}
	if !s.requireMutable(w) {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	var kind string
	ok := s.authFlows().update(id, func(p *pendingAuth) {
		kind = p.Kind
		if p.Kind != "device" {
			return
		}
		if p.cancel != nil {
			p.cancel()
		}
		p.Status = "canceled"
		p.Message = "canceled by user"
	})
	if !ok || kind != "device" {
		writeJSON(w, http.StatusNotFound, opErrorResponse{Error: "device login not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": "canceled"})
}

// POST /v1/auth/oauth — start OAuth; returns authorize URL + flow id for paste completion.
// Does not invent a browser callback. Unsupported providers get a specific error.
func (s *Server) handleAuthOAuthStart(w http.ResponseWriter, r *http.Request) {
	if s.opts.Services == nil || s.opts.Services.Auth == nil {
		capabilityUnavailable(w, "auth")
		return
	}
	if !s.requireMutable(w) {
		return
	}
	var body struct {
		Provider string `json:"provider"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	provider := strings.TrimSpace(body.Provider)
	if provider == "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "provider is required"})
		return
	}
	// Check capability flags first for a clear unavailable message.
	var supports bool
	for _, st := range s.opts.Services.Auth.Statuses() {
		if strings.EqualFold(st.Name, provider) && st.OAuth {
			supports = true
			break
		}
	}
	if !supports {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{
			Error: "OAuth is unavailable for this provider in the browser. Use API key or device flow when supported, or run `strike auth login` in the TUI/CLI.",
		})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	login, err := s.opts.Services.Auth.BeginOAuth(ctx, provider)
	if err != nil {
		cancel()
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: redactAuthErr(err)})
		return
	}
	id := newFlowID()
	p := &pendingAuth{
		ID:           id,
		Provider:     provider,
		Kind:         "oauth",
		AuthorizeURL: login.URL,
		ExpiresAt:    time.Now().Add(15 * time.Minute),
		Status:       "pending",
		cancel:       cancel,
		oauth:        login,
	}
	s.authFlows().put(p)
	go func() {
		defer cancel()
		msg, err := login.Wait(ctx)
		s.authFlows().mu.Lock()
		defer s.authFlows().mu.Unlock()
		cur := s.authFlows().byID[id]
		if cur == nil || cur.Status != "pending" {
			return
		}
		if err != nil {
			cur.Status = "failed"
			cur.Message = redactAuthErr(err)
			return
		}
		cur.Status = "completed"
		cur.Message = msg
	}()
	writeJSON(w, http.StatusOK, map[string]any{
		"id":           id,
		"provider":     provider,
		"kind":         "oauth",
		"authorizeUrl": login.URL,
		"expiresAt":    p.ExpiresAt.UTC().Format(time.RFC3339),
		"status":       "pending",
		"mode":         "paste",
		"note":         "Open the authorize URL, then paste the redirect URL or code if the host callback cannot reach this browser. Device flow is preferred on remote hosts.",
	})
}

// POST /v1/auth/oauth/{id}/complete — paste redirect URL or code.
func (s *Server) handleAuthOAuthComplete(w http.ResponseWriter, r *http.Request) {
	if s.opts.Services == nil || s.opts.Services.Auth == nil {
		capabilityUnavailable(w, "auth")
		return
	}
	if !s.requireMutable(w) {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	p := s.authFlows().get(id)
	if p == nil || p.Kind != "oauth" || p.oauth == nil {
		writeJSON(w, http.StatusNotFound, opErrorResponse{Error: "oauth login not found"})
		return
	}
	var body struct {
		Paste string `json:"paste"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	if strings.TrimSpace(body.Paste) == "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "paste is required (redirect URL or authorization code)"})
		return
	}
	if err := p.oauth.CompleteWithPaste(body.Paste); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: redactAuthErr(err)})
		return
	}
	provider := p.Provider
	s.authFlows().update(id, func(cur *pendingAuth) {
		cur.Status = "completed"
		cur.Message = "oauth completed"
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":             true,
		"status":         "completed",
		"providerStatus": providerStatusByName(s.opts.Services.Auth, provider),
	})
}

// GET /v1/auth/oauth/{id}
func (s *Server) handleAuthOAuthStatus(w http.ResponseWriter, r *http.Request) {
	if s.opts.Services == nil || s.opts.Services.Auth == nil {
		capabilityUnavailable(w, "auth")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	p, ok := s.authFlows().snapshot(id)
	if !ok || p.Kind != "oauth" {
		writeJSON(w, http.StatusNotFound, opErrorResponse{Error: "oauth login not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":             p.ID,
		"provider":       p.Provider,
		"kind":           p.Kind,
		"authorizeUrl":   p.AuthorizeURL,
		"expiresAt":      p.ExpiresAt.UTC().Format(time.RFC3339),
		"status":         p.Status,
		"message":        p.Message,
		"providerStatus": providerStatusByName(s.opts.Services.Auth, p.Provider),
	})
}

// DELETE /v1/auth/oauth/{id}
func (s *Server) handleAuthOAuthCancel(w http.ResponseWriter, r *http.Request) {
	if s.opts.Services == nil || s.opts.Services.Auth == nil {
		capabilityUnavailable(w, "auth")
		return
	}
	if !s.requireMutable(w) {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	var kind string
	ok := s.authFlows().update(id, func(p *pendingAuth) {
		kind = p.Kind
		if p.Kind != "oauth" {
			return
		}
		if p.cancel != nil {
			p.cancel()
		}
		p.Status = "canceled"
		p.Message = "canceled by user"
	})
	if !ok || kind != "oauth" {
		writeJSON(w, http.StatusNotFound, opErrorResponse{Error: "oauth login not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": "canceled"})
}

// --- custom providers ---

func (s *Server) handleCustomProviders(w http.ResponseWriter, r *http.Request) {
	if s.opts.Services == nil || s.opts.Services.Providers == nil {
		capabilityUnavailable(w, "providers")
		return
	}
	switch r.Method {
	case http.MethodGet:
		list := s.opts.Services.Providers.List()
		// Never include credentials; CustomProvider has no key field by contract.
		writeJSON(w, http.StatusOK, map[string]any{"providers": list})
	case http.MethodPost:
		if !s.requireMutable(w) {
			return
		}
		var body host.CustomProvider
		if err := decodeBody(w, r, &body); err != nil {
			writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
			return
		}
		// Reject any accidental credential fields by only mapping known fields
		// (decode already DisallowUnknownFields).
		if strings.TrimSpace(body.Name) == "" || strings.TrimSpace(body.BaseURL) == "" {
			writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "name and baseURL are required"})
			return
		}
		if err := s.opts.Services.Providers.Upsert(body); err != nil {
			writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
			return
		}
		got, _ := s.opts.Services.Providers.Get(body.Name)
		writeJSON(w, http.StatusOK, got)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, opErrorResponse{Error: "method not allowed"})
	}
}

func (s *Server) handleCustomProviderDelete(w http.ResponseWriter, r *http.Request) {
	if s.opts.Services == nil || s.opts.Services.Providers == nil {
		capabilityUnavailable(w, "providers")
		return
	}
	if !s.requireMutable(w) {
		return
	}
	name := strings.TrimSpace(r.PathValue("name"))
	if name == "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "name is required"})
		return
	}
	if err := s.opts.Services.Providers.Remove(name); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- scheduler presets ---

func (s *Server) handleSchedulerPresets(w http.ResponseWriter, r *http.Request) {
	if s.opts.Services == nil || s.opts.Services.SchedulerPresets == nil {
		capabilityUnavailable(w, "scheduler")
		return
	}
	list := s.opts.Services.SchedulerPresets.List()
	global, err := s.opts.Services.SchedulerPresets.Global()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"presets": list,
		"global":  global,
	})
}

func (s *Server) handleSchedulerPresetsApply(w http.ResponseWriter, r *http.Request) {
	if s.opts.Services == nil || s.opts.Services.SchedulerPresets == nil {
		capabilityUnavailable(w, "scheduler")
		return
	}
	if !s.requireMutable(w) {
		return
	}
	var body struct {
		IDs []string `json:"ids"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	if body.IDs == nil {
		body.IDs = []string{}
	}
	if err := s.opts.Services.SchedulerPresets.ApplyGlobalPresets(body.IDs); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	global, _ := s.opts.Services.SchedulerPresets.Global()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "global": global})
}

// --- config sources (typed inspection, not raw file editor) ---

func (s *Server) handleConfigSources(w http.ResponseWriter, r *http.Request) {
	if s.opts.Services == nil || s.opts.Services.ConfigFiles == nil {
		capabilityUnavailable(w, "configFiles")
		return
	}
	workDir := s.currentCWD(r)
	refs := s.opts.Services.ConfigFiles.List(workDir)
	// Strip absolute Path from wire if it could leak host layout beyond Display.
	// Keep Display + Exists + Slot for provenance; Path is host-local for Ensure only.
	out := make([]map[string]any, 0, len(refs))
	for _, ref := range refs {
		out = append(out, map[string]any{
			"slot":      ref.Slot,
			"kind":      ref.Kind,
			"scope":     ref.Scope,
			"label":     ref.Label,
			"display":   ref.Display,
			"exists":    ref.Exists,
			"canCreate": ref.CanCreate,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"sources": out})
}
