// Package host defines the services a strike frontend needs from its host
// process, beyond the engine protocol: credentials, model catalog, saved
// defaults, prompt history, project memory/issues/plans, workflow catalog,
// plugin lifecycle, local telemetry, and static agent/skill listings. Contract only:
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
	Provider   string  // owning provider id (set by Catalog.Models / ModelsForProviders)
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
	// cost, capabilities), sorted by id. Each ModelInfo.Provider is set.
	// Same empty/error contract as ModelIDs.
	Models(ctx context.Context, provider string) ([]ModelInfo, error)
	// ModelsForProviders returns models across providers in the given order
	// (provider order, then model id within each). Each ModelInfo.Provider is
	// set. Empty names are skipped. Individual provider failures are omitted
	// (partial success). Returns an error only when every non-empty provider
	// fails and none yielded models.
	ModelsForProviders(ctx context.Context, providers []string) ([]ModelInfo, error)
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

// UserDefaults is a snapshot of persisted global defaults (empty = unset).
// Field vocabulary matches ~/.strike/config JSON keys.
type UserDefaults struct {
	Provider       string
	Model          string
	Agent          string
	Effort         string
	PermissionMode string
	// Sandbox is the OS process sandbox dial (off|read-only|workspace-write).
	// Distinct from PermissionMode (when asked). Empty means unset → product
	// default workspace-write at load.
	Sandbox string
	// Notify is desktop notification gating: on|off|unfocused-only.
	Notify string
	// Autoupdate is startup release check: off|notify|auto.
	Autoupdate string
	// LeanCode is lean-code guidance intensity: off|lite|full.
	LeanCode string
	// DeferTools is toolsearch-backed schema deferral: on|off.
	DeferTools string
	// SessionWorktree is session.worktree: off|auto|always.
	SessionWorktree string
	// PermissionAutoApproveSeconds is the permission-modal countdown (0 = off).
	PermissionAutoApproveSeconds int
	// PermissionAutoApproveExclude lists permission names that never auto-allow.
	PermissionAutoApproveExclude []string
	// MaxChildDepth bounds nested task spawns (0 = engine default).
	MaxChildDepth int
	Theme         string
	VimMode       string
	NanoMode      string
	MdReadMode    string
	// Compaction / prune dials (history compaction). Zero/empty means unset
	// → engine defaults at load (see docs/config.md).
	CompactionStrategy  string
	CompactionModel     string
	CompactionThreshold float64
	CompactionBuffer    int
	KeepUserTurns       int
	PruneProtectTokens  int
	PruneMinimumTokens  int
	PruneKeepUserTurns  int
	PruneProtectTools   []string
}

// CompactionDials is a partial update for history compaction and continuous
// prune knobs. Empty string fields leave the corresponding stored value
// unchanged. Vocabulary:
//
//	Strategy           — trim|summarize
//	Model              — model id; "-" clears (use session model)
//	Threshold          — occupancy fraction string (e.g. "0.70"); "default"/"0"
//	                     resets to engine default; ">=1" disables threshold
//	Buffer / KeepUserTurns / Prune*Tokens / PruneKeepUserTurns —
//	                     non-negative integer strings; "default"/"0" resets
//	PruneProtectTools  — comma-separated tool names; "-" clears extras
type CompactionDials struct {
	Strategy           string
	Model              string
	Threshold          string
	Buffer             string
	KeepUserTurns      string
	PruneProtectTokens string
	PruneMinimumTokens string
	PruneKeepUserTurns string
	PruneProtectTools  string
}

// Settings persists user-chosen defaults. Empty fields mean "leave as is".
type Settings interface {
	// Defaults returns the current global defaults snapshot. Missing config
	// yields a zero value; implementations should not error on absence.
	Defaults() UserDefaults
	// SaveDefaults persists the chosen provider, model, agent, reasoning
	// effort, and permission mode; each empty string leaves the corresponding
	// stored value unchanged. Effort and mode are plain strings so this
	// contract stays stdlib-only; an unrecognized value is rejected with an
	// error. mode is default|plan|soft-approve|accept-edits|yolo.
	SaveDefaults(provider, model, agent, effort, mode string) error
	// SaveTheme persists the preferred TUI theme id (JSON theme file stem).
	// Empty id is rejected.
	SaveTheme(id string) error
	// SavePresentation persists non-empty vimMode, nanoMode, and mdReadMode
	// (pane|embedded|overlay|modal|takeover vocabulary). Empty leaves the
	// stored value unchanged; unknown values are rejected.
	SavePresentation(vimMode, nanoMode, mdReadMode string) error
	// SaveConfigDials persists non-empty peer-ported behavior dials into
	// ~/.strike/config: sandbox (off|read-only|workspace-write), notify
	// (on|off|unfocused-only), leanCode (off|lite|full), deferTools (on|off),
	// sessionWorktree (off|auto|always), and autoupdate (off|notify|auto).
	// Empty leaves the stored value unchanged; unknown values are rejected.
	SaveConfigDials(sandbox, notify, leanCode, deferTools, sessionWorktree, autoupdate string) error
	// SaveAutoApproveDials persists permissionAutoApproveSeconds, optional
	// exclude list, and maxChildDepth. Empty scalar strings leave the
	// corresponding field unchanged. exclude nil leaves the list unchanged; a
	// non-nil pointer (including to an empty slice) replaces it.
	// seconds: off|0|1-60; maxChildDepth: default|0|1-8. Unknown values error.
	SaveAutoApproveDials(seconds string, exclude *[]string, maxChildDepth string) error
	// SaveCompactionDials persists non-empty compaction/prune dials into
	// ~/.strike/config. See CompactionDials for field vocabulary. Unknown or
	// unparseable values are rejected without writing.
	SaveCompactionDials(d CompactionDials) error
	// SaveKeybinds persists binding-id overrides to ~/.strike/keybinds.jsonc.
	// Unknown ids are silently dropped; callers should pre-filter. A nil
	// map deletes the file (reset to defaults).
	SaveKeybinds(overrides map[string][]string) error
}

// ConfigFileScope is global (~/.strike) or project (./.strike).
type ConfigFileScope string

const (
	ConfigScopeGlobal  ConfigFileScope = "global"
	ConfigScopeProject ConfigFileScope = "project"
)

// ConfigFileRef is one /config picker row (primary slot or existing extra file).
type ConfigFileRef struct {
	// Slot is a primary id: config|mcp|providers|keybinds. Empty for extras.
	Slot string
	// Kind groups extras: agents|skills|themes|workflows. Empty for primary.
	Kind string
	// Scope is global or project.
	Scope ConfigFileScope
	// Label is the short display name (e.g. "Main config", "agents/foo.md").
	Label string
	// Path is the absolute path to open or create.
	Path string
	// Display is a user-facing path (~/.strike/config or ./.strike/…).
	Display string
	// Exists is true when Path is present on disk.
	Exists bool
	// CanCreate is true for primary slots that may be stub-created on select.
	CanCreate bool
}

// ConfigFiles enumerates and prepares user-editable .strike config surfaces
// for the /config picker. Never includes auth.json, sessions, managed/MDM,
// or other non-user-edit roots.
type ConfigFiles interface {
	// List returns picker rows for global + project (workDir) scopes.
	// workDir may be empty (skips project rows that need a root).
	List(workDir string) []ConfigFileRef
	// Ensure creates a missing primary-slot stub when CanCreate is true.
	// Returns the absolute path to open. created is true when a new file was
	// written. Rejects paths outside global/project .strike roots.
	Ensure(ref ConfigFileRef) (path string, created bool, err error)
	// LoadKeybinds returns merged keybind overrides from disk (global then
	// project) for live TUI apply. Invalid JSON yields an error; missing
	// files yield an empty map.
	LoadKeybinds(workDir string) (map[string][]string, error)
}

// Onboarding is global first-time setup state (installation-scoped, not
// per-project). Interactive TUI may auto-open /ftue while unacknowledged;
// exec/auth/serve/version/upgrade must not call this interface.
type Onboarding interface {
	// ShouldAutoOpen reports whether an interactive TUI launch should open
	// the setup wizard once. Established installs (sessions or real
	// credentials) migrate to acknowledged without returning true. Safe to
	// call repeatedly; may persist migration on first call.
	ShouldAutoOpen() bool
	// Acknowledge marks onboarding complete (finish or dismiss). Idempotent
	// and safe under concurrent in-process callers.
	Acknowledge() error
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
	// ForkAt copies the first keepEvents of id's log into a new root session.
	// keepEvents < 0 means the full log (same as Fork). Parent stays intact.
	ForkAt(id string, keepEvents int) (Session, error)
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
	// LiveIDs lists in-process root session ids in stable spawn/open order.
	// Activate must not reorder the slice (agents pane / numbered shortcuts).
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

// MCPServerStatus is one configured external MCP server for /mcp and web.
type MCPServerStatus struct {
	Name      string   `json:"name"`
	Command   string   `json:"command,omitempty"`   // non-secret endpoint label (command or URL)
	Transport string   `json:"transport,omitempty"` // stdio|http
	State     string   `json:"state"`               // "up", "down", "error", "disabled"
	ToolCount int      `json:"toolCount"`
	Error     string   `json:"error,omitempty"`
	Tools     []string `json:"tools,omitempty"`
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

// LSPServerStatus is one configured language server for /lsp.
type LSPServerStatus struct {
	Name       string
	Command    string
	State      string // "up", "down", "error", "disabled"
	Extensions []string
	Error      string
	OpenDocs   int
}

// Diagnostic is one language-server finding for the diagnostics right pane.
// Line and Character are 1-based for display (LSP wire is 0-based).
type Diagnostic struct {
	Path      string
	Line      int
	Character int
	Severity  string // error|warning|info|hint
	Source    string
	Code      string
	Message   string
}

// LSP reports language server status, control, and collected diagnostics.
// Nil means the capability is absent; frontends must degrade gracefully.
type LSP interface {
	// Statuses returns configured servers in stable order.
	Statuses() []LSPServerStatus
	// Retry reconnects name (or every non-up server when name is empty).
	Retry(name string) error
	// Disable stops name.
	Disable(name string) error
	// Diagnostics returns a stable-ordered snapshot of live-server findings.
	Diagnostics() []Diagnostic
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
	// MemCachedBytes is reclaimable OS page/file cache excluded from MemUsedBytes.
	MemCachedBytes uint64
	MemCachedOK    bool

	SwapUsedBytes  uint64
	SwapTotalBytes uint64
	SwapOK         bool

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
	// disk (empty skips disk). Must respect ctx cancellation and must not block
	// the caller past ctx (slow disk probes should be cached/off-thread).
	Sample(ctx context.Context, root string) (TelemetrySample, error)
}

// GoalCriterion is one falsifiable success condition on a host.Goal.
type GoalCriterion struct {
	Description string
	Check       string // "cmd: …" | "predicate: …" | "judge: …"
	Satisfied   bool
}

// Goal is a project-local loop-harness goal for /goal.
type Goal struct {
	ID            string
	Description   string
	Criteria      []GoalCriterion
	Status        string // pending|active|paused|done|failed|aborted
	MaxIterations int
	MaxCostUSD    float64
	AllowedTools  []string
	CostUSD       float64
	LastIteration int
	FailReason    string
	CreatedAt     time.Time
}

// GoalSetOptions configures /goal set constraints.
type GoalSetOptions struct {
	MaxIterations      int
	MaxCostUSD         float64
	MaxWallClockS      int
	MaxNoProgressIters int
	AllowedTools       []string
}

// GoalIteration is one committed loop pass for /goal log.
type GoalIteration struct {
	N         int
	Plan      string
	StateHash string
	CostUSD   float64
	// Summary is a short human-readable line (criteria matrix + action count).
	Summary string
}

// PlanSection is one ordered block inside a host.Plan.
type PlanSection struct {
	ID    string
	Title string
	Body  string
	// Delegate* surface section→child correlation for plan progress UI.
	DelegateStatus    string
	DelegateChildID   string
	DelegateChildName string
	DelegateDetail    string
}

// Plan is a root-session-owned structured planning artifact.
type Plan struct {
	ID        string
	OwnerRoot string // owning root session id
	Title     string
	Status    string // draft|approved|closed
	Sections  []PlanSection
	Version   int // CAS token; increments on every accepted mutation
	CreatedAt time.Time
	UpdatedAt time.Time
}

// PlanMeta is project-index metadata (no section bodies) visible to every root.
type PlanMeta struct {
	ID           string
	OwnerRoot    string
	Title        string
	Status       string
	Version      int
	SectionCount int
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Plans is project-scoped durable structured plans for the plan window and tools.
// Nil means the capability is absent; frontends must degrade gracefully.
// Mutations require the owning root session id and an expected Version for CAS.
// List returns index metadata for the whole project; Get returns full bodies.
type Plans interface {
	// List returns project-wide index metadata newest-UpdatedAt first.
	List() ([]PlanMeta, error)
	// Get returns one full plan by id (deep copy).
	Get(id string) (Plan, bool, error)
	// Create inserts a draft plan owned by ownerRoot. Section IDs are assigned.
	Create(ownerRoot, title string, sections []PlanSection) (Plan, error)
	// UpdateTitle CAS-updates the title. Only the owning root may mutate.
	UpdateTitle(id, ownerRoot, title string, expectedVersion int) (Plan, error)
	// UpdateSection CAS-updates one section by stable id. Nil pointers leave
	// fields unchanged. Only the owning root may mutate.
	UpdateSection(id, ownerRoot, sectionID string, title, body *string, expectedVersion int) (Plan, error)
	// AddSection appends a section with a new stable id. Owner + CAS.
	AddSection(id, ownerRoot, title, body string, expectedVersion int) (Plan, error)
	// SetStatus CAS-transitions lifecycle (draft↔approved, either→closed).
	// Closed plans reopen only via Reopen.
	SetStatus(id, ownerRoot, status string, expectedVersion int) (Plan, error)
	// Reopen CAS-moves a closed plan back to draft. Owner-only.
	Reopen(id, ownerRoot string, expectedVersion int) (Plan, error)
}

// Goals is project-scoped loop-harness control for /goal.
// Nil means the capability is absent; frontends must degrade gracefully.
type Goals interface {
	// Set validates and stores a pending goal (does not run).
	// criteria entries are CheckSpec strings (cmd:/predicate:/judge:).
	Set(description string, criteria []string, opts GoalSetOptions) (Goal, error)
	// List returns goals newest-first.
	List() ([]Goal, error)
	// Get returns one goal by id.
	Get(id string) (Goal, bool, error)
	// Run starts or resumes the loop until terminal, paused, or ctx cancel.
	Run(ctx context.Context, id string) (Goal, error)
	// Pause requests pause of an active goal.
	Pause(id string) (Goal, error)
	// Resume marks a paused goal active without running iterations.
	Resume(id string) (Goal, error)
	// Abort terminates a non-terminal goal.
	Abort(id string) (Goal, error)
	// Log returns committed iterations (optional single iter when iter>0).
	Log(id string, iter int) ([]GoalIteration, error)
}

// ShellResult is the outcome of a user-initiated local shell run (composer !).
type ShellResult struct {
	Command  string
	Output   string
	ExitCode int
}

// Shell runs local bash commands for frontend bang-escape (!cmd). Nil means
// the capability is absent; frontends must degrade gracefully. Implementations
// must apply the same workspace destructive-path guard as the bash tool.
// Permission prompts are omitted — the user typed the command — but the
// destructive-path guard still runs (best-effort; not a security boundary).
type Shell interface {
	// Run executes command with bash in the session work directory. Empty
	// command returns an error. Respects ctx cancellation/timeout.
	Run(ctx context.Context, command string) (ShellResult, error)
}

// SchedulerPresetRule is one inspectable command classification rule from a
// shipped scheduler preset (pattern glob → class).
type SchedulerPresetRule struct {
	Pattern string
	Class   string // general | build | test
}

// SchedulerPreset is FTUE/settings metadata for one shipped build-system
// preset. IDs are stable config keys; Rules/Limits are the values expansion
// would inject (ordinary scheduler fields — not a second runtime path).
type SchedulerPreset struct {
	ID           string
	Version      int
	Name         string
	Rationale    string
	DefaultClass string // general | build | test
	Limits       map[string]int
	Rules        []SchedulerPresetRule
}

// SchedulerCommandRule is one user-authored (or otherwise stored) command
// classification rule from the global scheduler config layer.
type SchedulerCommandRule struct {
	Pattern string
	Class   string // general | build | test
	Source  string // optional provenance stamp; empty for plain user rules
}

// SchedulerGlobalState is the global config layer's scheduler section for
// FTUE/settings. Custom Limits/Commands are distinct from preset expansion.
type SchedulerGlobalState struct {
	Presets  []string // enabled shipped preset IDs
	Limits   map[string]int
	Commands []SchedulerCommandRule
}

// SchedulerPresets is the shipped build-system preset catalog plus global
// apply for onboarding and future settings UIs. Nil means the capability is
// absent.
type SchedulerPresets interface {
	// List returns every shipped preset in stable display order.
	List() []SchedulerPreset
	// Get returns one preset by stable ID.
	Get(id string) (SchedulerPreset, bool)
	// Global returns the current global-layer scheduler snapshot. Missing
	// config yields zero values and a nil error.
	Global() (SchedulerGlobalState, error)
	// ApplyGlobalPresets validates ids and atomically replaces the global
	// presets list. Custom limits and commands are preserved unchanged.
	// Unknown or duplicate ids error without writing. An empty slice clears
	// global presets only.
	ApplyGlobalPresets(ids []string) error
}

// PermissionMatch is one rule that matched during permission explain.
type PermissionMatch struct {
	Layer      string
	Permission string
	Pattern    string
	Action     string
}

// PermissionExplanation is the host-facing explain result for a sample tool call.
type PermissionExplanation struct {
	Permission string
	Pattern    string
	Action     string
	Layer      string
	Matched    PermissionMatch
	Trail      []PermissionMatch
	Summary    string // multi-line human text for notices
}

// PermissionPresetInfo is one shipped permission preset (read-only, dev, …).
type PermissionPresetInfo struct {
	ID          string
	Name        string
	Description string
}

// Permissions is the explain/preset surface for /permission. Nil when unsupported.
type Permissions interface {
	// Explain returns last-match-wins detail for permission + pattern.
	// Empty pattern means "*".
	Explain(permission, pattern string) PermissionExplanation
	// Presets lists shipped named rulesets.
	Presets() []PermissionPresetInfo
}

// Services bundles everything a frontend receives from its host. Any field
// may be nil/empty when a capability is absent (tests, future frontends);
// frontends must degrade gracefully.
type Services struct {
	Auth        Auth
	Catalog     Catalog
	Settings    Settings
	ConfigFiles ConfigFiles // /config picker paths; nil when unsupported
	Onboarding  Onboarding  // global FTUE state; nil when unsupported
	History     History
	Files       Files
	Shell       Shell // composer ! bang; nil when unsupported
	Memory      Memory
	Issues      Issues
	Plans       Plans     // structured root-owned plans; nil when unsupported
	Goals       Goals     // loop harness; nil when unsupported
	Sessions    Sessions  // durable session list/replay; nil when unsupported
	Roots       Roots     // concurrent parent sessions; nil when single-root only
	Providers   Providers // custom/self-hosted provider CRUD; nil when unsupported
	Init        ProjectInit
	MCP         MCP       // external MCP server status; nil when unsupported
	LSP         LSP       // language server status + diagnostics; nil when unsupported
	Plugins     Plugins   // plugin lifecycle manager; nil when unsupported
	Panes       Panes     // enabled pane contributions; nil when unsupported
	Telemetry   Telemetry // local CPU/RAM/disk; nil when unsupported
	// SchedulerPresets is the shipped build-system preset catalog and global
	// apply surface (FTUE #705).
	SchedulerPresets SchedulerPresets
	// Permissions is explain + shipped presets for /permission (#798).
	Permissions Permissions
	// Workflows is the loaded workflow catalog (builtin/global/project/plugin).
	// Nil when unsupported; frontends must degrade without panic.
	Workflows Workflows
	// WorkflowDrafts reviews/saves in-memory model or editor drafts without
	// activation. Nil when unsupported; frontends must degrade without panic.
	WorkflowDrafts WorkflowDrafts
	Agents         []string // selectable agent names, default first
	Skills         []Skill
}
