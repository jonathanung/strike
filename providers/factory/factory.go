// Package factory routes a provider name onto a concrete adapter.
//
// OpenAI platform-key vs ChatGPT OAuth billing is decided here. Config JSONC
// loading stays in the product; callers pass already-resolved endpoint and
// custom-provider structs.
package factory

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/jonathanung/strike-cli/provider"
	"github.com/jonathanung/strike-cli/provider/echo"
	"github.com/jonathanung/strike-cli/providers/anthropic"
	"github.com/jonathanung/strike-cli/providers/auth"
	"github.com/jonathanung/strike-cli/providers/chatgpt"
	"github.com/jonathanung/strike-cli/providers/google"
	"github.com/jonathanung/strike-cli/providers/openaicompat"
)

// Options is the already-parsed construction input. Store is required for
// every name except "echo".
type Options struct {
	Store auth.Store
	// LookupCustom returns an already-resolved custom provider (env expanded).
	LookupCustom func(name string) (Custom, bool)
	// LookupEndpoint returns an already-resolved builtin endpoint overlay.
	LookupEndpoint func(name string) (Endpoint, bool)
	// Disabled reports whether a builtin catalog provider is hidden.
	Disabled func(name string) bool
	// DefaultModel returns the catalog pin for a builtin name.
	DefaultModel func(name string) string
}

// Custom is a user-declared endpoint after config resolution.
type Custom struct {
	Name         string
	BaseURL      string
	API          string
	Headers      map[string]string
	APIKeyEnv    string
	DefaultModel string
}

// Endpoint customizes a built-in provider's HTTP origin and/or API key env.
type Endpoint struct {
	BaseURL   string
	APIKeyEnv string
	Headers   map[string]string
}

// Active reports whether any endpoint field is set.
func (e Endpoint) Active() bool {
	return strings.TrimSpace(e.BaseURL) != "" || strings.TrimSpace(e.APIKeyEnv) != "" || len(e.Headers) > 0
}

// CanonicalID normalizes a provider id (trim, lowercase, gemini → google).
func CanonicalID(id string) string {
	return auth.CanonicalProvider(id)
}

// Select constructs a provider by name, probing credentials so a bad
// selection fails at select time with a clear message.
func Select(name string, opts Options) (provider.Provider, string, error) {
	name = CanonicalID(name)
	if opts.Disabled != nil && opts.Disabled(name) {
		return nil, "", fmt.Errorf("provider %q is disabled (set disable-default-%s to false, or disable-default-providers to false)", name, name)
	}
	if name != "echo" && opts.LookupEndpoint != nil {
		if ep, ok := opts.LookupEndpoint(name); ok {
			if p, model, err, handled := BuiltinEndpoint(name, ep, opts); handled {
				return p, model, err
			}
		}
	}
	switch name {
	case "echo":
		return echo.New(), modelOr(opts, name, "echo"), nil
	case "anthropic":
		if opts.Store == nil {
			return nil, "", fmt.Errorf("no Anthropic credentials: set ANTHROPIC_API_KEY or run `strike auth login anthropic`")
		}
		key, _ := auth.APIKey("anthropic", opts.Store)
		p, err := anthropic.New(key)
		if err != nil {
			return nil, "", err
		}
		return p, modelOr(opts, name, "claude-sonnet-5"), nil
	case "openai":
		return selectOpenAI(opts)
	case "xai":
		if err := requireBearer(name, opts.Store); err != nil {
			return nil, "", err
		}
		return openaicompat.NewXAI(auth.BearerSource(name, opts.Store)), modelOr(opts, name, "grok-4.5"), nil
	case "google":
		if err := requireBearer(name, opts.Store); err != nil {
			return nil, "", err
		}
		return google.New(auth.BearerSource(name, opts.Store)), modelOr(opts, name, "gemini-2.5-pro"), nil
	case "kimi":
		if err := requireBearer(name, opts.Store); err != nil {
			return nil, "", err
		}
		return openaicompat.New("kimi", "https://api.moonshot.cn/v1", auth.BearerSource(name, opts.Store)), modelOr(opts, name, "moonshot-v1"), nil
	case "deepseek":
		if err := requireBearer(name, opts.Store); err != nil {
			return nil, "", err
		}
		return openaicompat.NewTextOnly("deepseek", "https://api.deepseek.com/v1", auth.BearerSource(name, opts.Store)), modelOr(opts, name, "deepseek-chat"), nil
	default:
		if opts.LookupCustom != nil {
			if cp, ok := opts.LookupCustom(name); ok {
				return BuildCustom(cp, opts.Store)
			}
		}
		return nil, "", fmt.Errorf("unknown provider %q (want anthropic, openai, xai, google, kimi, deepseek, echo, or a custom name from /settings; gemini is accepted as an alias of google)", name)
	}
}

func selectOpenAI(opts Options) (provider.Provider, string, error) {
	// Routing decides who gets billed: an explicit API key (env or
	// pasted) targets the platform API; a ChatGPT OAuth login
	// targets the subscription-billed ChatGPT backend.
	model := modelOr(opts, "openai", "gpt-5.5")
	if os.Getenv("OPENAI_API_KEY") != "" {
		return openaicompat.NewOpenAI(auth.BearerSource("openai", opts.Store)), model, nil
	}
	if opts.Store == nil {
		return nil, "", fmt.Errorf("no OpenAI credentials: set OPENAI_API_KEY or run `strike auth login openai` (or /auth openai)")
	}
	cred, ok := opts.Store.Get("openai")
	switch {
	case ok && cred.Type == auth.TypeOAuth:
		source := auth.ChatGPTSource(opts.Store)
		if _, _, err := source(context.Background()); err != nil {
			return nil, "", err
		}
		return chatgpt.New(source), model, nil
	case ok && cred.APIKey != "":
		return openaicompat.NewOpenAI(auth.BearerSource("openai", opts.Store)), model, nil
	default:
		return nil, "", fmt.Errorf("no OpenAI credentials: set OPENAI_API_KEY or run `strike auth login openai` (or /auth openai)")
	}
}

func requireBearer(name string, store auth.Store) error {
	if store == nil {
		return fmt.Errorf("no %s credentials: set %s or run `strike auth login %s`", name, builtinKeyHint(name, ""), name)
	}
	source := auth.BearerSource(name, store)
	if _, err := source(context.Background()); err != nil {
		return err
	}
	return nil
}

func modelOr(opts Options, name, fallback string) string {
	if opts.DefaultModel != nil {
		if m := opts.DefaultModel(name); m != "" {
			return m
		}
	}
	return fallback
}

// BuildCustom maps a resolved custom provider onto the openaicompat or
// anthropic adapter. When apiKeyEnv is set, a missing key fails clearly.
func BuildCustom(cp Custom, store auth.Store) (provider.Provider, string, error) {
	if err := ValidateBaseURL(cp.BaseURL); err != nil {
		return nil, "", fmt.Errorf("custom provider %s: %w", cp.Name, err)
	}
	defaultModel := cp.DefaultModel
	switch strings.ToLower(strings.TrimSpace(cp.API)) {
	case "openai":
		if cp.APIKeyEnv != "" {
			if _, ok := auth.APIKeyEnv(cp.Name, store, cp.APIKeyEnv); !ok {
				return nil, "", fmt.Errorf("custom provider %s: set %s (or paste a key via /auth %s)", cp.Name, cp.APIKeyEnv, cp.Name)
			}
		}
		source := OptionalBearer(cp.Name, store, cp.APIKeyEnv)
		return openaicompat.NewWithHeaders(cp.Name, cp.BaseURL, source, cp.Headers), defaultModel, nil
	case "responses":
		if cp.APIKeyEnv != "" {
			if _, ok := auth.APIKeyEnv(cp.Name, store, cp.APIKeyEnv); !ok {
				return nil, "", fmt.Errorf("custom provider %s: set %s (or paste a key via /auth %s)", cp.Name, cp.APIKeyEnv, cp.Name)
			}
		}
		source := OptionalBearer(cp.Name, store, cp.APIKeyEnv)
		return openaicompat.NewResponses(cp.Name, cp.BaseURL, source, cp.Headers), defaultModel, nil
	case "anthropic":
		key, ok := auth.APIKeyEnv(cp.Name, store, cp.APIKeyEnv)
		if cp.APIKeyEnv != "" && !ok {
			return nil, "", fmt.Errorf("custom provider %s: set %s (or paste a key via /auth %s)", cp.Name, cp.APIKeyEnv, cp.Name)
		}
		p, err := anthropic.NewCustom(cp.Name, cp.BaseURL, key, cp.Headers)
		if err != nil {
			return nil, "", err
		}
		return p, defaultModel, nil
	default:
		return nil, "", fmt.Errorf("custom provider %s: unknown api %q", cp.Name, cp.API)
	}
}

// BuiltinEndpoint applies a resolved providers.jsonc endpoint overlay onto a
// built-in provider. handled is false when the overlay has nothing actionable.
func BuiltinEndpoint(name string, ep Endpoint, opts Options) (provider.Provider, string, error, bool) {
	if !ep.Active() {
		return nil, "", nil, false
	}
	defaultModel := modelOr(opts, name, "")
	envName := ep.APIKeyEnv
	hint := builtinKeyHint(name, envName)
	store := opts.Store

	switch name {
	case "anthropic":
		baseURL := ep.BaseURL
		if baseURL == "" {
			baseURL = builtinDefaultBaseURL(name)
		} else if err := ValidateBaseURL(baseURL); err != nil {
			return nil, "", fmt.Errorf("anthropic endpoint: %w", err), true
		}
		var (
			key string
			ok  bool
		)
		if envName != "" {
			key, ok = auth.APIKeyEnv(name, store, envName)
		} else {
			key, ok = auth.APIKey(name, store)
		}
		if !ok || key == "" {
			return nil, "", fmt.Errorf("no Anthropic credentials: set %s or run `strike auth login anthropic`", hint), true
		}
		p, err := anthropic.NewCustom("anthropic", baseURL, key, ep.Headers)
		if err != nil {
			return nil, "", err, true
		}
		return p, defaultModel, nil, true

	case "openai", "xai", "kimi", "deepseek":
		baseURL := ep.BaseURL
		if baseURL == "" {
			baseURL = builtinDefaultBaseURL(name)
		} else if err := ValidateBaseURL(baseURL); err != nil {
			return nil, "", fmt.Errorf("%s endpoint: %w", name, err), true
		}
		// Overlay forces chat-completions (not ChatGPT OAuth backend).
		var source openaicompat.BearerSource
		if envName != "" {
			if _, ok := auth.APIKeyEnv(name, store, envName); !ok {
				return nil, "", fmt.Errorf("no %s credentials: set %s or paste a key via /auth %s", name, envName, name), true
			}
			source = OptionalBearer(name, store, envName)
		} else {
			source = auth.BearerSource(name, store)
			if _, err := source(context.Background()); err != nil {
				return nil, "", fmt.Errorf("no %s credentials: set %s or run `strike auth login %s`", name, hint, name), true
			}
		}
		return openaicompat.NewWithHeaders(name, baseURL, source, ep.Headers), defaultModel, nil, true

	case "google":
		if ep.BaseURL != "" {
			return nil, "", fmt.Errorf("google endpoint baseURL overlay is not supported yet"), true
		}
		if envName == "" {
			return nil, "", nil, false
		}
		if _, ok := auth.APIKeyEnv(name, store, envName); !ok {
			return nil, "", fmt.Errorf("no google credentials: set %s", envName), true
		}
		return google.New(OptionalBearer(name, store, envName)), defaultModel, nil, true

	default:
		return nil, "", nil, false
	}
}

// OptionalBearer resolves an API key from env/store and returns empty (not
// an error) when neither is set — used by local gateways.
func OptionalBearer(name string, store auth.Store, envName string) openaicompat.BearerSource {
	return func(ctx context.Context) (string, error) {
		if key, ok := auth.APIKeyEnv(name, store, envName); ok {
			return key, nil
		}
		return "", nil
	}
}

// ValidateBaseURL requires an absolute http(s) URL with a host.
func ValidateBaseURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("baseURL is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("baseURL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("baseURL must be http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("baseURL must include a host")
	}
	return nil
}

func builtinDefaultBaseURL(name string) string {
	switch name {
	case "anthropic":
		return "https://api.anthropic.com"
	case "openai":
		return "https://api.openai.com/v1"
	case "xai":
		return "https://api.x.ai/v1"
	case "kimi":
		return "https://api.moonshot.cn/v1"
	case "deepseek":
		return "https://api.deepseek.com/v1"
	default:
		return ""
	}
}

func builtinKeyHint(name, envName string) string {
	if envName != "" {
		return envName
	}
	switch name {
	case "anthropic":
		return "ANTHROPIC_API_KEY"
	case "openai":
		return "OPENAI_API_KEY"
	case "xai":
		return "XAI_API_KEY"
	case "google":
		return "GEMINI_API_KEY"
	case "kimi":
		return "KIMI_API_KEY"
	case "deepseek":
		return "DEEPSEEK_API_KEY"
	default:
		return "API key"
	}
}
