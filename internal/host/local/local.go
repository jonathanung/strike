// Package local implements the host.Services contract against strike's real
// backend: the auth credential store, the models.dev catalog, global config
// defaults, and project prompt history. It is the composition seam wired up
// by cmd/strike; frontends depend on the host contract, never on this
// package.
package local

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/jonathanung/strike-cli/internal/auth"
	"github.com/jonathanung/strike-cli/internal/config"
	"github.com/jonathanung/strike-cli/internal/history"
	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/issue"
	"github.com/jonathanung/strike-cli/internal/memory"
	"github.com/jonathanung/strike-cli/internal/models"
	"github.com/jonathanung/strike-cli/internal/protocol"
)

// New builds the local host services backed by the real auth store, prompt
// history, project memory/issues, and the models.dev catalog. A nil hist yields a
// nil Services.History (history is optional). A nil mem yields a nil
// Services.Memory. A nil issues yields a nil Services.Issues. Agents pass
// through unchanged; skills whose names fail config.ValidateSkillName are
// dropped here, since they cannot be invoked as slash commands (the frontend
// no longer filters). customs may be nil (no custom provider support).
// workDir scopes relative memory/issue export-import paths (empty allows only
// absolute paths, or relative paths resolved via filepath.Abs without a root).
func New(store *auth.Store, hist *history.Store, mem *memory.Store, issues *issue.Store, agents []string, skills []config.Skill, customs *config.CustomStore, workDir string) host.Services {
	if customs == nil {
		customs = config.NewCustomStore(nil)
	}
	services := host.Services{
		Auth:      authAdapter{store: store, customs: customs},
		Catalog:   catalogAdapter{customs: customs},
		Settings:  settingsAdapter{},
		Providers: providersAdapter{store: customs},
		Agents:    agents,
	}
	// A typed nil *history.Store would satisfy the interface but panic on
	// use, so only wire history when it is really present.
	if hist != nil {
		services.History = hist
	}
	if mem != nil {
		services.Memory = memoryAdapter{store: mem, workDir: workDir}
	}
	if issues != nil {
		services.Issues = issuesAdapter{store: issues, workDir: workDir}
	}
	for _, s := range skills {
		if err := config.ValidateSkillName(s.Name); err != nil {
			continue
		}
		services.Skills = append(services.Skills, host.NewSkill(
			s.Name,
			s.Description,
			strings.Contains(s.Template, "$ARGUMENTS"),
			s.Render,
		))
	}
	return services
}

// credentialProviders are the built-in providers backed by the auth store, in
// display order, each carrying the login methods it supports. Custom providers
// and echo are appended by Statuses.
var credentialProviders = []host.ProviderStatus{
	{Name: "anthropic", APIKey: true},
	{Name: "openai", OAuth: true, APIKey: true},
	{Name: "xai", OAuth: true, Device: true, APIKey: true},
}

// authAdapter adapts *auth.Store to host.Auth.
type authAdapter struct {
	store   *auth.Store
	customs *config.CustomStore
}

func (a authAdapter) Statuses() []host.ProviderStatus {
	customs := a.customs.List()
	out := make([]host.ProviderStatus, 0, len(credentialProviders)+len(customs)+1)
	for _, p := range credentialProviders {
		p.Detail = auth.Describe(p.Name, a.store)
		p.Authed = p.Detail != "none"
		if cred, ok := a.store.Get(p.Name); ok && cred.Type == auth.TypeOAuth && !cred.ExpiresAt.IsZero() {
			p.ExpiresAt = cred.ExpiresAt
		}
		out = append(out, p)
	}
	for _, cp := range customs {
		key, hasKey := auth.APIKeyEnv(cp.Name, a.store, cp.APIKeyEnv)
		credDetail := "none"
		if hasKey {
			if d := auth.Describe(cp.Name, a.store); d != "none" {
				credDetail = d
			} else if key != "" {
				credDetail = "env"
			}
		}
		hostName := ""
		if u, err := url.Parse(cp.BaseURL); err == nil {
			hostName = u.Host
		}
		meta := string(cp.API)
		if hostName != "" {
			meta = string(cp.API) + " · " + hostName
		}
		detail := meta
		if credDetail != "none" {
			detail = credDetail + " · " + meta
		}
		// Customs are always selectable once defined; local gateways (ollama)
		// often need no key. Missing keys still surface in Detail as "none".
		out = append(out, host.ProviderStatus{
			Name:    cp.Name,
			Detail:  detail,
			Authed:  true,
			Custom:  true,
			APIKey:  true,
			WireAPI: string(cp.API),
			BaseURL: cp.BaseURL,
		})
	}
	out = append(out, host.ProviderStatus{
		Name:    "echo",
		Detail:  "offline dev provider",
		Authed:  true,
		Builtin: true,
	})
	return out
}

func (a authAdapter) Describe(provider string) string {
	if cp, ok := a.customs.Get(provider); ok {
		if key, ok := auth.APIKeyEnv(provider, a.store, cp.APIKeyEnv); ok && key != "" {
			if d := auth.Describe(provider, a.store); d != "none" {
				return d
			}
			return "env"
		}
		return "none"
	}
	return auth.Describe(provider, a.store)
}

// SetAPIKey trims surrounding whitespace (matching the former modal), then
// rejects an empty key or a provider that does not accept API keys.
func (a authAdapter) SetAPIKey(provider, key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("api key is empty")
	}
	if !a.acceptsAPIKey(provider) {
		return fmt.Errorf("provider %q does not accept an API key", provider)
	}
	return a.store.Set(provider, auth.Credential{Type: auth.TypeAPIKey, APIKey: key})
}

func (a authAdapter) Logout(provider string) error {
	return a.store.Delete(provider)
}

func (a authAdapter) BeginOAuth(ctx context.Context, provider string) (*host.OAuthLogin, error) {
	var flow auth.FlowConfig
	switch provider {
	case "openai":
		flow = auth.OpenAIFlow()
	case "xai":
		flow = auth.XAIFlow()
	default:
		return nil, fmt.Errorf("provider %q does not support OAuth login", provider)
	}
	pending, err := flow.Begin()
	if err != nil {
		return nil, err
	}
	store := a.store
	wait := func(ctx context.Context) (string, error) {
		tokens, err := pending.Wait(ctx)
		if err != nil {
			return "", err
		}
		// Background on purpose: persisting a completed login must not be
		// lost to UI cancellation of the wait ctx.
		return auth.CompleteLogin(context.Background(), store, provider, tokens)
	}
	return host.NewOAuthLogin(pending.URL, wait).WithPaste(func(raw string) error {
		return pending.CompleteWithPaste(raw)
	}), nil
}

func (a authAdapter) BeginDevice(ctx context.Context, provider string) (*host.DeviceLogin, error) {
	if provider != "xai" {
		return nil, fmt.Errorf("provider %q does not support device login", provider)
	}
	flow := auth.XAIDeviceFlow()
	code, err := flow.RequestCode(ctx)
	if err != nil {
		return nil, err
	}
	store := a.store
	poll := func(ctx context.Context) (string, error) {
		tokens, err := flow.Poll(ctx, code)
		if err != nil {
			return "", err
		}
		// Background on purpose: see BeginOAuth.
		return auth.CompleteLogin(context.Background(), store, provider, tokens)
	}
	return host.NewDeviceLogin(code.UserCode, code.VerificationURI, poll), nil
}

// acceptsAPIKey reports whether a provider stores a pasted API key. echo does
// not; customs and credential builtins do.
func (a authAdapter) acceptsAPIKey(provider string) bool {
	for _, p := range credentialProviders {
		if p.Name == provider {
			return p.APIKey
		}
	}
	if _, ok := a.customs.Get(provider); ok {
		return true
	}
	return false
}

// catalogAdapter adapts the models.dev catalog to host.Catalog, with custom
// provider model lists taking precedence over the remote catalog.
type catalogAdapter struct {
	customs *config.CustomStore
}

func (c catalogAdapter) ModelIDs(ctx context.Context, provider string) ([]string, error) {
	infos, err := c.Models(ctx, provider)
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(infos))
	for i, info := range infos {
		ids[i] = info.ID
	}
	return ids, nil
}

func (c catalogAdapter) Models(ctx context.Context, provider string) ([]host.ModelInfo, error) {
	if cp, ok := c.customs.Get(provider); ok {
		if len(cp.Models) == 0 {
			return nil, fmt.Errorf("no models configured for %s — add model ids in /settings or use /model <id>", provider)
		}
		out := make([]host.ModelInfo, len(cp.Models))
		for i, id := range cp.Models {
			out[i] = host.ModelInfo{ID: id}
		}
		return out, nil
	}
	catalog, err := models.Load(ctx)
	if err != nil {
		return nil, err
	}
	infos := catalog.Infos(provider)
	if len(infos) == 0 {
		return nil, fmt.Errorf("no models listed for %s on models.dev", provider)
	}
	out := make([]host.ModelInfo, len(infos))
	for i, info := range infos {
		out[i] = host.ModelInfo{
			ID:         info.ID,
			Context:    info.Context,
			InputCost:  info.InputCost,
			OutputCost: info.OutputCost,
			HasCost:    info.HasCost,
			ToolCall:   info.ToolCall,
			Reasoning:  info.Reasoning,
			Attachment: info.Attachment,
		}
	}
	return out, nil
}

func (c catalogAdapter) ContextWindow(ctx context.Context, provider, model string) (int, bool, error) {
	if _, ok := c.customs.Get(provider); ok {
		return 0, false, nil
	}
	catalog, err := models.Load(ctx)
	if err != nil {
		return 0, false, err
	}
	tokens, ok := catalog.ContextWindow(provider, model)
	return tokens, ok, nil
}

func (c catalogAdapter) OutputLimit(ctx context.Context, provider, model string) (int, bool, error) {
	if _, ok := c.customs.Get(provider); ok {
		return 0, false, nil
	}
	catalog, err := models.Load(ctx)
	if err != nil {
		return 0, false, err
	}
	tokens, ok := catalog.OutputLimit(provider, model)
	return tokens, ok, nil
}

// settingsAdapter adapts global config persistence to host.Settings.
type settingsAdapter struct{}

func (settingsAdapter) SaveDefaults(provider, model, agent, effort string) error {
	level, ok := protocol.ParseEffort(effort)
	if !ok {
		return fmt.Errorf("unknown effort %q", effort)
	}
	return config.SetGlobalDefaults(provider, model, agent, level)
}

func (settingsAdapter) SaveTheme(id string) error {
	return config.SetGlobalTheme(id)
}

// memoryAdapter adapts *memory.Store to host.Memory.
type memoryAdapter struct {
	store   *memory.Store
	workDir string
}

func (m memoryAdapter) List(tag string) ([]host.MemoryEntry, error) {
	entries, err := m.store.List(tag)
	if err != nil {
		return nil, err
	}
	out := make([]host.MemoryEntry, len(entries))
	for i, e := range entries {
		out[i] = host.MemoryEntry{Key: e.Key, Value: e.Value, Tags: append([]string(nil), e.Tags...)}
	}
	return out, nil
}

func (m memoryAdapter) Get(key string) (host.MemoryEntry, bool, error) {
	e, ok, err := m.store.Get(key)
	if err != nil || !ok {
		return host.MemoryEntry{}, ok, err
	}
	return host.MemoryEntry{Key: e.Key, Value: e.Value, Tags: append([]string(nil), e.Tags...)}, true, nil
}

func (m memoryAdapter) Put(key, value string, tags []string) error {
	return m.store.Put(key, value, tags)
}

func (m memoryAdapter) Delete(key string) error {
	return m.store.Delete(key)
}

func (m memoryAdapter) Export(path string) error {
	resolved, err := resolveDataPath(m.workDir, path)
	if err != nil {
		return err
	}
	return m.store.Export(resolved)
}

func (m memoryAdapter) Import(path string, replace bool) (int, error) {
	resolved, err := resolveDataPath(m.workDir, path)
	if err != nil {
		return 0, err
	}
	return m.store.Import(resolved, replace)
}

// issuesAdapter adapts *issue.Store to host.Issues.
type issuesAdapter struct {
	store   *issue.Store
	workDir string
}

func (a issuesAdapter) List(status string) ([]host.Issue, error) {
	items, err := a.store.List(status)
	if err != nil {
		return nil, err
	}
	out := make([]host.Issue, len(items))
	for i, iss := range items {
		out[i] = toHostIssue(iss)
	}
	return out, nil
}

func (a issuesAdapter) Get(id int) (host.Issue, bool, error) {
	iss, ok, err := a.store.Get(id)
	if err != nil || !ok {
		return host.Issue{}, ok, err
	}
	return toHostIssue(iss), true, nil
}

func (a issuesAdapter) Create(title, body string) (host.Issue, error) {
	iss, err := a.store.Create(title, body)
	if err != nil {
		return host.Issue{}, err
	}
	return toHostIssue(iss), nil
}

func (a issuesAdapter) Update(id int, title, body, status *string) (host.Issue, error) {
	iss, err := a.store.Update(id, title, body, status)
	if err != nil {
		return host.Issue{}, err
	}
	return toHostIssue(iss), nil
}

func (a issuesAdapter) Close(id int) (host.Issue, error) {
	iss, err := a.store.CloseIssue(id)
	if err != nil {
		return host.Issue{}, err
	}
	return toHostIssue(iss), nil
}

func (a issuesAdapter) Export(path string) error {
	resolved, err := resolveDataPath(a.workDir, path)
	if err != nil {
		return err
	}
	return a.store.Export(resolved)
}

func (a issuesAdapter) Import(path string, replace bool) (int, error) {
	resolved, err := resolveDataPath(a.workDir, path)
	if err != nil {
		return 0, err
	}
	return a.store.Import(resolved, replace)
}

// resolveDataPath resolves export/import paths. Relative paths must stay under
// workDir when set (no ".." or symlink escape). Absolute paths are intentional
// targets and are only cleaned.
func resolveDataPath(workDir, path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("path is empty")
	}
	if strings.IndexByte(path, 0) >= 0 {
		return "", fmt.Errorf("path contains invalid character")
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	workDir = strings.TrimSpace(workDir)
	if workDir == "" {
		abs, err := filepath.Abs(path)
		if err != nil {
			return "", fmt.Errorf("resolve path: %w", err)
		}
		return abs, nil
	}
	resolved, _, err := resolveUnderRoot(workDir, path)
	if err != nil {
		return "", err
	}
	return resolved, nil
}

func toHostIssue(iss issue.Issue) host.Issue {
	return host.Issue{
		ID:     iss.ID,
		Title:  iss.Title,
		Body:   iss.Body,
		Status: iss.Status,
	}
}

// providersAdapter exposes custom provider CRUD through the host contract.
type providersAdapter struct {
	store *config.CustomStore
}

func (p providersAdapter) List() []host.CustomProvider {
	items := p.store.List()
	out := make([]host.CustomProvider, len(items))
	for i, cp := range items {
		out[i] = toHostCustom(cp)
	}
	return out
}

func (p providersAdapter) Get(name string) (host.CustomProvider, bool) {
	cp, ok := p.store.Get(name)
	if !ok {
		return host.CustomProvider{}, false
	}
	return toHostCustom(cp), true
}

func (p providersAdapter) Upsert(hp host.CustomProvider) error {
	return p.store.Upsert(fromHostCustom(hp))
}

func (p providersAdapter) Remove(name string) error {
	return p.store.Remove(name)
}

func toHostCustom(cp config.CustomProvider) host.CustomProvider {
	h := host.CustomProvider{
		Name:      cp.Name,
		BaseURL:   cp.BaseURL,
		API:       string(cp.API),
		APIKeyEnv: cp.APIKeyEnv,
	}
	if len(cp.Headers) > 0 {
		h.Headers = make(map[string]string, len(cp.Headers))
		for k, v := range cp.Headers {
			h.Headers[k] = v
		}
	}
	if len(cp.Models) > 0 {
		h.Models = append([]string(nil), cp.Models...)
	}
	return h
}

func fromHostCustom(hp host.CustomProvider) config.CustomProvider {
	cp := config.CustomProvider{
		Name:      hp.Name,
		BaseURL:   hp.BaseURL,
		API:       config.WireAPI(hp.API),
		APIKeyEnv: hp.APIKeyEnv,
	}
	if len(hp.Headers) > 0 {
		cp.Headers = make(map[string]string, len(hp.Headers))
		for k, v := range hp.Headers {
			cp.Headers[k] = v
		}
	}
	if len(hp.Models) > 0 {
		cp.Models = append([]string(nil), hp.Models...)
	}
	return cp
}
