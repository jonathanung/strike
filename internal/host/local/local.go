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
	"strings"

	"github.com/jonathanung/strike-cli/internal/auth"
	"github.com/jonathanung/strike-cli/internal/config"
	"github.com/jonathanung/strike-cli/internal/history"
	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/models"
	"github.com/jonathanung/strike-cli/internal/protocol"
)

// New builds the local host services backed by the real auth store, prompt
// history, and the models.dev catalog. A nil hist yields a nil
// Services.History (history is optional). Agents pass through unchanged;
// skills whose names fail config.ValidateSkillName are dropped here, since
// they cannot be invoked as slash commands (the frontend no longer filters).
func New(store *auth.Store, hist *history.Store, agents []string, skills []config.Skill) host.Services {
	services := host.Services{
		Auth:     authAdapter{store: store},
		Catalog:  catalogAdapter{},
		Settings: settingsAdapter{},
		Agents:   agents,
	}
	// A typed nil *history.Store would satisfy the interface but panic on
	// use, so only wire history when it is really present.
	if hist != nil {
		services.History = hist
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

// credentialProviders are the providers backed by the auth store, in display
// order, each carrying the login methods it supports. echo is appended by
// Statuses as a builtin needing no credentials.
var credentialProviders = []host.ProviderStatus{
	{Name: "anthropic", APIKey: true},
	{Name: "openai", OAuth: true, APIKey: true},
	{Name: "xai", OAuth: true, Device: true, APIKey: true},
}

// authAdapter adapts *auth.Store to host.Auth.
type authAdapter struct {
	store *auth.Store
}

func (a authAdapter) Statuses() []host.ProviderStatus {
	out := make([]host.ProviderStatus, 0, len(credentialProviders)+1)
	for _, p := range credentialProviders {
		p.Detail = auth.Describe(p.Name, a.store)
		p.Authed = p.Detail != "none"
		if cred, ok := a.store.Get(p.Name); ok && cred.Type == auth.TypeOAuth && !cred.ExpiresAt.IsZero() {
			p.ExpiresAt = cred.ExpiresAt
		}
		out = append(out, p)
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
	return auth.Describe(provider, a.store)
}

// SetAPIKey trims surrounding whitespace (matching the former modal), then
// rejects an empty key or a provider that does not accept API keys.
func (a authAdapter) SetAPIKey(provider, key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("api key is empty")
	}
	if !acceptsAPIKey(provider) {
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

// acceptsAPIKey reports whether a provider stores a pasted API key. echo and
// unknown names do not.
func acceptsAPIKey(provider string) bool {
	for _, p := range credentialProviders {
		if p.Name == provider {
			return p.APIKey
		}
	}
	return false
}

// catalogAdapter adapts the models.dev catalog to host.Catalog.
type catalogAdapter struct{}

func (catalogAdapter) ModelIDs(ctx context.Context, provider string) ([]string, error) {
	catalog, err := models.Load(ctx)
	if err != nil {
		return nil, err
	}
	ids := catalog.ModelIDs(provider)
	if len(ids) == 0 {
		return nil, fmt.Errorf("no models listed for %s on models.dev", provider)
	}
	return ids, nil
}

func (catalogAdapter) ContextWindow(ctx context.Context, provider, model string) (int, bool, error) {
	catalog, err := models.Load(ctx)
	if err != nil {
		return 0, false, err
	}
	tokens, ok := catalog.ContextWindow(provider, model)
	return tokens, ok, nil
}

func (catalogAdapter) OutputLimit(ctx context.Context, provider, model string) (int, bool, error) {
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
