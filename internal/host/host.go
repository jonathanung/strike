// Package host defines the services a strike frontend needs from its host
// process, beyond the engine protocol: credentials, model catalog, saved
// defaults, prompt history, project memory/issues, local telemetry, and static
// agent/skill listings. Contract only:
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
	"errors"
	"time"
)

// ErrInitExists is returned by ProjectInit.Write when AGENTS.md already exists
// and force is false.
var ErrInitExists = errors.New("AGENTS.md already exists")

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

// Model source labels for ModelInfo.Source.
const (
	ModelSourceCatalog = "catalog" // models.dev (or equivalent) only
	ModelSourceConfig  = "config"  // providers.jsonc / custom list only
	ModelSourceMerge   = "merge"   // catalog entry refined by config
)

// ModelInfo is picker-facing metadata for one catalog model. Zero fields mean
// unknown or unsupported; frontends must omit them from display.
type ModelInfo struct {
	ID         string
	Name       string  // display label; empty means use ID
	Context    int     // context window tokens; 0 = unknown
	Output     int     // max output tokens; 0 = unknown
	InputCost  float64 // USD per million input tokens
	OutputCost float64 // USD per million output tokens
	HasCost    bool
	ToolCall   bool
	Reasoning  bool
	Attachment bool     // multimodal / file attachments
	VariantIDs []string // config effort/reasoning variant ids
	Source     string   // ModelSourceCatalog | ModelSourceConfig | ModelSourceMerge
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
	// ResolveVariant maps a config model variant id to a reasoning effort
	// level (protocol effort string). ok is false when the variant is unknown
	// or carries no effort field.
	ResolveVariant(ctx context.Context, provider, model, variant string) (effort string, ok bool, err error)
}

// Settings persists user-chosen defaults. Empty fields mean "leave as is".
type Settings interface {
	// SaveDefaults persists the chosen provider, model, agent, reasoning
	// effort, and permission mode; each empty string leaves the corresponding
	// stored value unchanged. Effort and mode are plain strings so this
	// contract stays stdlib-only; an unrecognized value is rejected with an
	// error. mode is default|plan|soft-approve|accept-edits|yolo.
	SaveDefaults(provider, model, agent, effort, mode string) error
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

// FileContent is a project-scoped read for composer @file mentions.
// Path is workspace-relative (slash-separated). When Skip is true, Content is
// empty and Notice explains why (binary, oversize, missing, escape, …).
type FileContent struct {
	Path    string
	Content string
	Notice  string
	Skip    bool
}

// EditApply is one exact-string replacement for Files.ApplyEdit (diff viewer).
type EditApply struct {
	Path       string
	OldString  string
	NewString  string
	ReplaceAll bool
}

// EditApplyResult is returned by Files.ApplyEdit after a successful apply.
type EditApplyResult struct {
	Path    string // project-relative path written
	Count   int    // replacements performed (0 when Already)
	Already bool   // true when the file already reflected NewString (no write)
}

// Files reads workspace files for frontend features (markdown reader, file
// explorer, @file mentions) and applies user-initiated edit/patch writes from
// the diff viewer. Nil means the capability is absent; frontends must degrade
// gracefully.
type Files interface {
	// ReadFile resolves path (relative to the host work directory, or absolute),
	// then reads the file. Implementations enforce a size cap. Empty path,
	// missing files, directories, oversize content, and I/O failures return errors.
	// Unlike ReadScoped, absolute paths and ".." may leave the work directory
	// (user-initiated reads such as /md-read).
	ReadFile(path string) ([]byte, error)
	// ListDir lists the directory at path (relative to the work directory, or
	// absolute). Empty path lists the work directory root. Missing paths and
	// non-directories return errors. Results are sorted directories-first,
	// then by name (case-insensitive).
	ListDir(path string) ([]DirEntry, error)
	// SearchFiles returns workspace-relative paths under the work directory
	// matching query (basename + full path; exact, prefix, contains, then
	// ordered subsequence). Directories end with a trailing "/". Empty query
	// returns a stable non-noise prefix of the index. An exact existing path
	// is always included even when outside the fuzzy top-k. Results never
	// escape the work root via ".." or directory symlinks. limit caps the
	// result count (implementations enforce a maximum).
	SearchFiles(query string, limit int) ([]string, error)
	// ReadScoped reads path only when the final resolved path (after cleaning
	// and symlink evaluation) stays under the work directory. Binary files and
	// oversize content are skipped or truncated with Notice set; missing paths
	// and escapes set Skip. Directories expand to an immediate-child listing
	// only (not recursive file contents); Path is returned with a trailing "/".
	ReadScoped(path string) (FileContent, error)
	// ApplyEdit performs an exact string replacement under the work root
	// (symlink-safe). Failures leave the file unchanged. When OldString is
	// absent but NewString is already present (single-match case), returns
	// Already without writing. ReplaceAll replaces every occurrence; otherwise
	// OldString must match exactly once.
	ApplyEdit(req EditApply) (EditApplyResult, error)
	// ApplyPatch applies a multi-file apply_patch envelope under the work root.
	// Validates fully before writing and rolls back on commit failure so partial
	// state is avoided when possible. Returns a short human summary on success.
	ApplyPatch(patch string) (summary string, err error)
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
	// Export writes a versioned portable JSON snapshot to path.
	// Relative paths resolve under the project work directory and must not
	// escape it; absolute paths are allowed as intentional targets.
	Export(path string) error
	// Import loads a portable snapshot. When replace is true the store is
	// cleared first; otherwise entries merge by key (imported wins). Returns
	// the number of entries applied from the file.
	Import(path string, replace bool) (int, error)
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
	// Export writes a versioned portable JSON snapshot to path.
	// Relative paths resolve under the project work directory and must not
	// escape it; absolute paths are allowed as intentional targets.
	Export(path string) error
	// Import loads a portable snapshot. When replace is true the store is
	// cleared first; otherwise issues merge by id (imported wins). IDs are
	// preserved. Returns the number of issues applied from the file.
	Import(path string, replace bool) (int, error)
}

// Session is one durable agent session the frontend can list or open.
type Session struct {
	ID        string
	ParentID  string // empty for root sessions
	Title     string
	Open      bool
	UpdatedAt time.Time // zero when unknown
	// ProjectKey is the launch project identity (history/memory key). Empty on
	// legacy sessions created before project scoping.
	ProjectKey string
	// Optional PR linkage from shipping (sidecar / session.meta). Empty when none.
	PRURL    string
	PRNumber int
	PRState  string // open|merged|closed when known
}

// Sessions reads durable session logs for transcript navigation and resume.
// Nil means the capability is absent; frontends must degrade gracefully.
// Event payloads are JSONL envelopes (protocol codec) so this contract stays
// stdlib-only.
type Sessions interface {
	// Get returns one session by id.
	Get(id string) (Session, bool, error)
	// List returns durable sessions newest-UpdatedAt first. When rootsOnly is
	// true, only sessions without a parent are included (picker / resume).
	// Implementations scope to the current launch project by default; legacy
	// sessions without a project key are omitted from the default list.
	List(rootsOnly bool) ([]Session, error)
	// Children returns direct child sessions of parentID (newest first).
	Children(parentID string) ([]Session, error)
	// ReplayJSONL returns the raw JSONL event log for id (one envelope per line).
	ReplayJSONL(id string) ([]byte, error)
	// Fork copies id's event log into a new root session ("fork of …") and
	// returns the child. Parent stays intact. Implementations may reject
	// subagent (parented) transcripts.
	Fork(id string) (Session, error)
	// Rename sets the durable display title for id. Empty title clears it.
	// Survives restart via session metadata.
	Rename(id, title string) (Session, error)
	// Delete removes id's durable log and metadata. When the session is open
	// (or is the active session), force must be true; otherwise Delete fails
	// and leaves files intact.
	Delete(id string, force bool) error
}

// AllProjectsSessions is an optional Sessions capability: list transcripts
// across every project (power-user picker toggle).
type AllProjectsSessions interface {
	ListAllProjects(rootsOnly bool) ([]Session, error)
}

// PRStateRefresher is an optional Sessions capability: best-effort remote
// refresh of PR open/merged/closed, caching results on disk. Implementations
// must leave last-known metadata unchanged on network/tool failure and must
// never surface forge tokens in errors returned to the UI.
type PRStateRefresher interface {
	RefreshPRStates(sessions []Session) []Session
}

// ProjectInit bootstraps project agent instructions (AGENTS.md) from a light
// local scan. Nil means the capability is absent; frontends must degrade.
type ProjectInit interface {
	// Exists reports whether a non-empty AGENTS.md is already present under
	// the work root. path is the absolute target path when known.
	Exists() (exists bool, path string, err error)
	// Write creates or replaces AGENTS.md. When force is false and the file
	// already exists, returns ErrInitExists without writing. created is true
	// when the file did not previously exist (or was empty).
	Write(force bool) (path string, created bool, err error)
}

// Roots controls concurrent in-process parent (root) sessions. Nil means the
// host is single-root: switching sessions uses the composition-root resume
// loop (engine restart, no OS process exit). When non-nil, Spawn/Activate keep
// multiple root engines live so ≥2 parents can run without tearing down the
// TUI program.
type Roots interface {
	// ActiveID is the root currently receiving composer ops.
	ActiveID() string
	// LiveIDs lists in-process root session ids (stable order: active first,
	// then remaining by id).
	LiveIDs() []string
	// Activate switches the ops target to an already-live root id.
	Activate(id string) error
	// Spawn creates a new empty root session+engine, activates it, and returns
	// its id. The prior active root keeps running in the background.
	Spawn() (string, error)
	// Open starts (or activates) a durable root session in-process. Already-live
	// ids only Activate. Unknown or subagent ids return an error.
	Open(id string) error
	// Interrupt cancels the turn on id; empty id targets the active root.
	Interrupt(id string) error
	// WorkDir returns the tool CWD bound to a live root (worktree or launch).
	WorkDir(id string) string
}

// MCPServerStatus is one configured external MCP server for /mcp.
type MCPServerStatus struct {
	Name      string
	Command   string // non-secret endpoint label (command or URL)
	Transport string // stdio|http
	State     string // "up", "down", "error", "disabled"
	ToolCount int
	Error     string
	Tools     []string
}

// MCP reports external Model Context Protocol server status and control.
// Nil means the capability is absent; frontends must degrade gracefully.
type MCP interface {
	// Statuses returns configured servers in stable order.
	Statuses() []MCPServerStatus
	// Retry reconnects name (or every non-up server when name is empty).
	Retry(name string) error
	// Disable stops name and unregisters its tools.
	Disable(name string) error
}

// TelemetrySample is one local host resource snapshot for the system pane.
// OK flags distinguish measured zeros from unavailable values — frontends
// must never render missing metrics as zero.
type TelemetrySample struct {
	CPUHostPct float64 // host-wide CPU utilization 0–100
	CPUHostOK  bool
	CPUProcPct float64 // this process CPU 0–100+
	CPUProcOK  bool

	MemUsedBytes  uint64
	MemTotalBytes uint64
	MemOK         bool

	DiskUsedBytes  uint64
	DiskTotalBytes uint64
	DiskFreeBytes  uint64
	DiskOK         bool
	DiskRoot       string // path whose filesystem was measured

	At time.Time
}

// Telemetry samples local CPU/RAM/disk. Nil means the capability is absent;
// frontends must degrade gracefully. Implementations are local-only and must
// never upload samples or attach them to provider requests.
type Telemetry interface {
	// Sample collects one snapshot. root is the project/worktree path used for
	// disk (empty skips disk). Respects ctx cancellation.
	Sample(ctx context.Context, root string) (TelemetrySample, error)
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
	Roots     Roots     // concurrent parent sessions; nil when single-root only
	Providers Providers // custom/self-hosted provider CRUD; nil when unsupported
	Init      ProjectInit
	MCP       MCP       // external MCP server status; nil when unsupported
	Telemetry Telemetry // local CPU/RAM/disk; nil when unsupported
	Agents    []string  // selectable agent names, default first
	Skills    []Skill
}
