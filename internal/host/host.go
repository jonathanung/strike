// Package host defines the services a strike frontend needs from its host
// process, beyond the engine protocol: credentials, model catalog, saved
// defaults, prompt history, and static agent/skill listings. Contract only:
// this package imports nothing outside the standard library so frontends
// can be developed and tested against fakes. Implementations live in
// internal/host/local.
//
// The boundary rule is the point of this package. internal/tui talks to the
// host through these interfaces alone, never to internal/auth, config,
// models, or history directly. The backend can therefore add a service or a
// protocol event (staging a non-exposed feature) without touching the
// frontend, and the frontend can be exercised against fakes with no network,
// credentials, or disk.
package host

import (
	"context"
	"time"
)

// ProviderStatus describes one selectable provider and its credential
// state, with capability flags so frontends stay data-driven (adding a
// provider must not require frontend edits).
type ProviderStatus struct {
	Name      string    // e.g. "anthropic"
	Detail    string    // human-readable credential state: "none", "oauth+key", "offline dev provider"
	Authed    bool      // usable right now
	Builtin   bool      // no credentials needed (echo)
	Custom    bool      // user-declared self-hosted / gateway provider
	OAuth     bool      // supports browser OAuth login
	Device    bool      // supports RFC 8628 device flow
	APIKey    bool      // supports pasted API key
	WireAPI   string    // custom only: "openai" | "anthropic"
	BaseURL   string    // custom only: endpoint origin (no secrets)
	ExpiresAt time.Time // OAuth token expiry; zero = unknown/N/A
}

// CustomProvider is a user-declared LLM endpoint. API keys are never included;
// set them through Auth.SetAPIKey using Name as the provider id.
type CustomProvider struct {
	Name      string
	BaseURL   string
	API       string // wire dialect: "openai" | "anthropic"
	Headers   map[string]string
	APIKeyEnv string
	Models    []string
}

// Providers manages custom/self-hosted provider definitions (config only).
// Credentials stay in Auth.
type Providers interface {
	// List returns every custom provider in stable order.
	List() []CustomProvider
	// Get returns one custom provider by name.
	Get(name string) (CustomProvider, bool)
	// Upsert validates and inserts or replaces a custom provider.
	Upsert(p CustomProvider) error
	// Remove deletes a custom provider definition (does not delete keys;
	// call Auth.Logout separately when forgetting credentials).
	Remove(name string) error
}

// Auth manages provider credentials. All methods are safe to call from
// frontend goroutines (tea.Cmd funcs).
type Auth interface {
	// Statuses lists every selectable provider in a stable order,
	// builtin providers included.
	Statuses() []ProviderStatus
	// Describe returns the credential state for one provider (the same
	// string used in ProviderStatus.Detail).
	Describe(provider string) string
	// SetAPIKey stores a pasted API key for a provider. It reports an error
	// for an empty key or a provider that does not accept API keys.
	SetAPIKey(provider, key string) error
	// Logout discards any stored credentials for a provider.
	Logout(provider string) error
	// BeginOAuth starts a browser OAuth login and returns the in-flight
	// handle. It errors for a provider that does not support OAuth.
	BeginOAuth(ctx context.Context, provider string) (*OAuthLogin, error)
	// BeginDevice starts an RFC 8628 device login and returns the in-flight
	// handle. It errors for a provider that does not support the device flow.
	BeginDevice(ctx context.Context, provider string) (*DeviceLogin, error)
}

// Catalog lists model ids and limits for a provider (may hit network/cache;
// ctx-aware).
type Catalog interface {
	// ModelIDs returns the provider's available model ids, or an error when
	// the catalog is unreachable or lists no models for the provider.
	ModelIDs(ctx context.Context, provider string) ([]string, error)
	// ContextWindow returns the model's context window in tokens.
	// ok is false when unknown (not the same as zero).
	ContextWindow(ctx context.Context, provider, model string) (tokens int, ok bool, err error)
	// OutputLimit returns the model's max output tokens.
	// ok is false when unknown (not the same as zero).
	OutputLimit(ctx context.Context, provider, model string) (tokens int, ok bool, err error)
}

// Settings persists user-chosen defaults. Empty fields mean "leave as is".
type Settings interface {
	// SaveDefaults persists the chosen provider, model, agent, and reasoning
	// effort; each empty string leaves the corresponding stored value
	// unchanged. Effort is a plain string so this contract stays
	// stdlib-only; an unrecognized level is rejected with an error.
	SaveDefaults(provider, model, agent, effort string) error
}

// History is project-scoped prompt history. Enqueue is async; the channel
// yields the persistence result exactly once.
type History interface {
	// Entries returns the recalled prompts, oldest to newest.
	Entries() []string
	// Enqueue durably appends a prompt; the returned channel delivers the
	// persistence result exactly once.
	Enqueue(prompt string) <-chan error
}

// Files reads workspace files for frontend features (e.g. markdown reader).
// Nil means the capability is absent; frontends must degrade gracefully.
type Files interface {
	// ReadFile resolves path (relative to the host work directory, or absolute),
	// then reads the file. Implementations enforce a size cap. Empty path,
	// missing files, directories, oversize content, and I/O failures return errors.
	ReadFile(path string) ([]byte, error)
}

// Services bundles everything a frontend receives from its host. Any field
// may be nil/empty when a capability is absent (tests, future frontends);
// frontends must degrade gracefully.
type Services struct {
	Auth      Auth
	Catalog   Catalog
	Settings  Settings
	History   History
	Files     Files
	Providers Providers // custom/self-hosted provider CRUD; nil when unsupported
	Agents    []string  // selectable agent names, default first
	Skills    []Skill
}
