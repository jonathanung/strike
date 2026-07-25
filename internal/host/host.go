// Package host defines the services a strike frontend needs from its host
// process, beyond the engine protocol: credentials, model catalog, saved
// defaults, prompt history, project memory/issues, and static agent/skill listings. Contract only:
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

// ModelInfo is picker-facing metadata for one catalog model. Zero fields mean
// unknown or unsupported; frontends must omit them from display.
type ModelInfo struct {
	ID         string
	Context    int     // context window tokens; 0 = unknown
	InputCost  float64 // USD per million input tokens
	OutputCost float64 // USD per million output tokens
	HasCost    bool
	ToolCall   bool
	Reasoning  bool
	Attachment bool // multimodal / file attachments
}

// Catalog lists model ids and limits for a provider (may hit network/cache;
// ctx-aware).
type Catalog interface {
	// ModelIDs returns the provider's available model ids, or an error when
	// the catalog is unreachable or lists no models for the provider.
	ModelIDs(ctx context.Context, provider string) ([]string, error)
	// Models returns the provider's models with catalog metadata (context,
	// cost, capabilities), sorted by id. Same empty/error contract as ModelIDs.
	Models(ctx context.Context, provider string) ([]ModelInfo, error)
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
	// SaveTheme persists the preferred TUI theme id (JSON theme file stem).
	// Empty id is rejected.
	SaveTheme(id string) error
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

// DirEntry is one name from Files.ListDir.
type DirEntry struct {
	Name  string
	IsDir bool
}

// Files reads workspace files for frontend features (markdown reader, file
// explorer). Nil means the capability is absent; frontends must degrade
// gracefully.
type Files interface {
	// ReadFile resolves path (relative to the host work directory, or absolute),
	// then reads the file. Implementations enforce a size cap. Empty path,
	// missing files, directories, oversize content, and I/O failures return errors.
	ReadFile(path string) ([]byte, error)
	// ListDir lists the directory at path (relative to the work directory, or
	// absolute). Empty path lists the work directory root. Missing paths and
	// non-directories return errors. Results are sorted directories-first,
	// then by name (case-insensitive).
	ListDir(path string) ([]DirEntry, error)
}

// MemoryEntry is one project-local key/value memory record.
type MemoryEntry struct {
	Key   string
	Value string
	Tags  []string
}

// Memory is project-scoped durable key/value memory for /memory and tools.
// Nil means the capability is absent; frontends must degrade gracefully.
type Memory interface {
	// List returns entries sorted by key. Non-empty tag filters to that tag.
	List(tag string) ([]MemoryEntry, error)
	// Get returns one entry by key.
	Get(key string) (MemoryEntry, bool, error)
	// Put inserts or replaces key with value and optional tags.
	Put(key, value string, tags []string) error
	// Delete removes key. Missing keys return an error.
	Delete(key string) error
}

// Issue is one project-local tracked issue.
type Issue struct {
	ID     int
	Title  string
	Body   string
	Status string // "open" or "closed"
}

// Issues is project-scoped durable issue tracking for /issues and tools.
// Nil means the capability is absent; frontends must degrade gracefully.
type Issues interface {
	// List returns issues sorted by id. Non-empty status filters to open|closed.
	List(status string) ([]Issue, error)
	// Get returns one issue by id.
	Get(id int) (Issue, bool, error)
	// Create inserts a new open issue.
	Create(title, body string) (Issue, error)
	// Update patches title, body, and/or status. Nil pointers leave fields unchanged.
	Update(id int, title, body, status *string) (Issue, error)
	// Close sets status to closed.
	Close(id int) (Issue, error)
}

// Session is one durable agent session the frontend can list or open.
type Session struct {
	ID       string
	ParentID string // empty for root sessions
	Title    string
	Open     bool
}

// Sessions reads durable session logs for transcript navigation (subagents).
// Nil means the capability is absent; frontends must degrade gracefully.
// Event payloads are JSONL envelopes (protocol codec) so this contract stays
// stdlib-only.
type Sessions interface {
	// Get returns one session by id.
	Get(id string) (Session, bool, error)
	// Children returns direct child sessions of parentID (newest first).
	Children(parentID string) ([]Session, error)
	// ReplayJSONL returns the raw JSONL event log for id (one envelope per line).
	ReplayJSONL(id string) ([]byte, error)
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
	Memory    Memory
	Issues    Issues
	Sessions  Sessions  // durable session list/replay; nil when unsupported
	Providers Providers // custom/self-hosted provider CRUD; nil when unsupported
	Agents    []string  // selectable agent names, default first
	Skills    []Skill
}
