// Package config loads strike configuration and user extensions. Layers:
// defaults, then global (~/.strike/config), then project (./.strike/config),
// then managed/MDM (system path; see managed.go) — JSON or JSONC (// and
// /* */ comments). Optional "$schema" is ignored (editor DX only). Scalar
// fields override; permission rules concatenate so later layers win under
// last-match-wins evaluation. Managed is highest for scalars and contributes
// a deny ceiling the permission service enforces after session grants.
// The same two .strike roots also hold agents/ and skills/ folders (see
// agents.go). Loaded by cmd/strike at startup and wrapped by
// internal/host/local (Settings, and the agent/skill listings); internal/tui
// never imports it directly. Programmatic saves (SetGlobal*,
// AppendProjectPermission) rewrite pure JSON and drop comments / $schema —
// see docs/config.md.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/jonathanung/strike-cli/internal/permission"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/sandbox"
	"github.com/jonathanung/strike-cli/internal/scheduler"
)

type Config struct {
	Provider     string          `json:"provider,omitempty"`
	Model        string          `json:"model,omitempty"`
	Effort       protocol.Effort `json:"effort,omitempty"`
	SystemPrompt string          `json:"systemPrompt,omitempty"`
	// LeanCode is agent-scoped lean-code guidance intensity: off|lite|full.
	// Empty means lite (default). Unknown values are ignored at load time.
	LeanCode string `json:"leanCode,omitempty"`
	// DeferTools controls toolsearch-backed schema deferral: on|off.
	// When on, non-core tools (optional built-ins + MCP) are omitted from the
	// provider tools[] until toolsearch discovers them (or they are called).
	// Empty means off (default). Unknown values are ignored at load time.
	DeferTools   string `json:"deferTools,omitempty"`
	DefaultAgent string `json:"defaultAgent,omitempty"`
	// Theme is the preferred TUI color theme id (bundled or JSON under
	// ~/.strike/themes or ./.strike/themes). Empty means the stock "strike"
	// palette.
	Theme string `json:"theme,omitempty"`
	// VimMode is how /vim presents the editor: "pane"/"embedded" (default,
	// right-pane PTY), "overlay"/"modal" (large scrim modal), or "takeover"
	// (full-screen tea.ExecProcess handoff). Unknown values are ignored at
	// load time. Aliases share vocabulary with mdReadMode and nanoMode.
	VimMode string `json:"vimMode,omitempty"`
	// NanoMode is how /nano presents nano: same values/aliases as VimMode
	// (pane|embedded, overlay|modal, takeover). Unknown values are ignored.
	NanoMode string `json:"nanoMode,omitempty"`
	// MdReadMode is how /md-read presents markdown: "embedded"/"pane" (default,
	// right-pane markdown window) or "modal"/"overlay" (large scrim modal).
	// Unknown values are ignored at load time.
	MdReadMode string `json:"mdReadMode,omitempty"`
	// Notify controls desktop notifications (OSC 9 + bell) for permission/
	// question asks and long turn completion: "on", "off", or
	// "unfocused-only" (default). Unknown values are ignored at load time.
	Notify string `json:"notify,omitempty"`
	// PermissionAutoApproveSeconds enables permission-modal auto-allow once
	// after N seconds (yolo-lite). Zero disables (default). Clamped to 1–60
	// when positive.
	PermissionAutoApproveSeconds int `json:"permissionAutoApproveSeconds,omitempty"`
	// PermissionAutoApproveExclude lists permission names (e.g. "bash") that
	// never auto-approve even when seconds > 0. Compared case-insensitively.
	PermissionAutoApproveExclude []string `json:"permissionAutoApproveExclude,omitempty"`
	// PermissionMode is the default tool-permission posture dial for new
	// sessions (default|plan|soft-approve|accept-edits|yolo). Empty means
	// default. Session changes via Shift+Tab or /mode persist in the JSONL
	// log, not here. Distinct from Sandbox (what OS isolation allows).
	PermissionMode protocol.PermissionMode `json:"permissionMode,omitempty"`
	// Sandbox is the OS process sandbox dial for bash: off|read-only|
	// workspace-write. Empty means workspace-write (default). This controls
	// what the OS isolation layer makes possible; permissionMode controls
	// when the agent is asked. See docs/config.md (two-dial model).
	Sandbox string `json:"sandbox,omitempty"`
	// Network holds application-layer egress allowlists (webfetch/websearch host/CIDR).
	// Distinct from Sandbox Network on/off (bash OS profile). See docs/config.md.
	Network NetworkConfig `json:"network,omitempty"`
	// WebSearch configures the websearch tool backend (provider + API key env).
	// Empty means auto-detect (brave when BRAVE_API_KEY is set). See docs/config.md.
	WebSearch   WebSearchConfig    `json:"webSearch,omitempty"`
	Permissions permission.Ruleset `json:"permissions,omitempty"`
	// PermissionPreset selects a shipped named ruleset (read-only|dev|
	// yolo-with-sandbox) inserted after defaults and before permissions[].
	// Empty means no preset layer. See permission.Presets / docs/config.md.
	PermissionPreset string `json:"permissionPreset,omitempty"`
	// Hooks mixes declarative rules (action) and shell commands (command).
	// Global then project layers concatenate. Invalid entries are dropped.
	Hooks []Hook `json:"hooks,omitempty"`
	// Providers are user-declared custom/self-hosted endpoints (name, base
	// URL, wire api). API keys are never stored here — only in auth.json.
	Providers []CustomProvider `json:"providers,omitempty"`
	// ModelOverlays are providers.jsonc model refinements for built-in /
	// catalog-backed providers (openai, anthropic, …). Not serialized on
	// config JSON — loaded from providers.jsonc only.
	ModelOverlays map[string][]ModelDef `json:"-"`
	// EndpointOverlays are providers.jsonc options (baseURL/apiKey/headers)
	// for built-in providers. Not serialized on config JSON.
	EndpointOverlays map[string]ProviderEndpoint `json:"-"`
	// DisableDefaultProviders hides all builtin catalog providers when true
	// (disable-default-providers in config or providers.jsonc). Per-provider
	// overrides live in DisableDefaultPer. Not round-tripped via standard
	// json tags alone — loaded via parseDisableDefaultFlags.
	DisableDefaultProviders bool `json:"-"`
	// disableDefaultProvidersSet is true when a layer explicitly set the bulk flag.
	disableDefaultProvidersSet bool `json:"-"`
	// DisableDefaultPer maps builtin name → disabled for disable-default-<name>.
	// Explicit false re-enables when DisableDefaultProviders is true.
	DisableDefaultPer map[string]bool `json:"-"`
	// CompactionStrategy is "trim" (default: drop older turns) or "summarize"
	// (replace dropped turns with a model-authored summary). Unknown values
	// are ignored at load time.
	CompactionStrategy string `json:"compactionStrategy,omitempty"`
	// CompactionModel optionally pins the model id used for summarize
	// compaction (same provider as the session). Empty uses the session model.
	CompactionModel string `json:"compactionModel,omitempty"`
	// CompactionThreshold is the occupancy fraction (0–1 exclusive of 0) that
	// triggers automatic compaction. Zero means engine default (0.70). Values
	// >=1 disable threshold compaction. Out-of-range negatives clamp to 0.
	CompactionThreshold float64 `json:"compactionThreshold,omitempty"`
	// CompactionBuffer is extra token headroom reserved with MaxTokens when
	// computing the threshold budget. Zero means engine default (4096).
	// Negatives clamp to 0.
	CompactionBuffer int `json:"compactionBuffer,omitempty"`
	// KeepUserTurns is how many trailing real user turns to preserve when
	// compacting. Zero means engine default (2). Negatives clamp to 0.
	KeepUserTurns int `json:"keepUserTurns,omitempty"`
	// PruneProtectTokens is how many recent tool-output tokens to keep intact
	// while walking history backward during continuous tool-result prune.
	// Zero means engine default (40000). Negatives clamp to 0.
	PruneProtectTokens int `json:"pruneProtectTokens,omitempty"`
	// PruneMinimumTokens is the minimum estimated tokens that must be freed
	// before prune mutates history (avoids thrash). Zero means engine default
	// (20000). Negatives clamp to 0.
	PruneMinimumTokens int `json:"pruneMinimumTokens,omitempty"`
	// PruneKeepUserTurns skips tool results inside the most recent N real user
	// turns during prune. Zero means engine default (2). Negatives clamp to 0.
	PruneKeepUserTurns int `json:"pruneKeepUserTurns,omitempty"`
	// PruneProtectTools names additional tools whose results stay available
	// after prune (merged with the built-in "skill" protect). Empty means no
	// extras. Names are lowercased and deduped at load.
	PruneProtectTools []string `json:"pruneProtectTools,omitempty"`
	// MaxChildDepth bounds nested task tool spawns (root depth 0). Zero means
	// engine default (1: children cannot spawn further tasks).
	MaxChildDepth int `json:"maxChildDepth,omitempty"`
	// Keybinds remaps app-level binding ids to key sequence(s). Ids match the
	// TUI keybind catalog (e.g. "nav.jump-bottom"). Merged last-wins per id
	// across global/project config and keybinds.jsonc layers. Unknown ids fail
	// Load. Prefer ~/.strike/keybinds.jsonc (or ./.strike/keybinds.jsonc).
	Keybinds map[string]KeybindChords `json:"keybinds,omitempty"`
	// Session holds per-session runtime preferences (worktree isolation).
	Session SessionConfig `json:"session,omitempty"`
	// MCP configures external Model Context Protocol servers (stdio or HTTP).
	// Prefer mcp.jsonc (see Load). When a layer sets servers (including {}),
	// it replaces the previous layer's server map.
	MCP MCPConfig `json:"mcp,omitempty"`
	// LSP configures external language servers (JSON-RPC over stdio). When a
	// layer sets servers (including {}), it replaces the previous layer's map.
	// Registry is keyed by file extension via each server's extensions list.
	LSP LSPConfig `json:"lsp,omitempty"`
	// Harnesses configures named external function harnesses. Project
	// definitions replace global definitions with the same name.
	Harnesses map[string]HarnessConfig `json:"harnesses,omitempty"`
	// Scheduler holds in-process resource pool limits, optional shipped
	// build-system presets, and command classification rules. Project limits
	// override global per pool (omitted keys preserve lower layers /
	// unlimited). Preset IDs and command rules concatenate (global then
	// project; presets expand before user rules). Last match wins. See
	// docs/config.md.
	Scheduler SchedulerConfig `json:"scheduler,omitempty"`
	// ToolRetry is the harness error-recovery / retry policy for tool
	// dispatch (error code × idempotency). See docs/config.md.
	ToolRetry ToolRetryConfig `json:"toolRetry,omitempty"`
	// Managed is provenance for the enterprise/MDM layer (not serialized).
	// Populated by Load when system managed-config is present.
	Managed ManagedInfo `json:"-"`
}

// ToolRetryConfig is the JSON "toolRetry" object — auto-retry and loop
// detection knobs for tool dispatch. Provider stream retries stay under
// engine MaxStreamAttempts (not configured here).
type ToolRetryConfig struct {
	// MaxAttempts bounds auto-retries for one tool call including the first
	// try. Zero means engine default (3). Set to 1 to disable tool auto-retry.
	// Only safe-retry tools retry on transient/timeout; mutative/unsafe never
	// auto-retry.
	MaxAttempts int `json:"maxAttempts,omitempty"`
	// BaseDelayMs is the first backoff step in milliseconds before attempt 2.
	// Zero means engine default (200). Negatives clamp to 0 (instant).
	BaseDelayMs int `json:"baseDelayMs,omitempty"`
	// MaxDelayMs caps exponential backoff. Zero means engine default (2000).
	MaxDelayMs int `json:"maxDelayMs,omitempty"`
	// LoopThreshold is how many identical consecutive failing tool+args stop
	// the turn. Zero means engine default (3).
	LoopThreshold int `json:"loopThreshold,omitempty"`
}

// NetworkConfig is the JSON "network" object — shared allowlist shape for
// application-layer web egress (webfetch, websearch API hosts). Bash OS
// networking stays all-or-nothing via sandbox.Policy.NetworkEnabled; container
// net can reuse this shape later.
type NetworkConfig struct {
	// Allow lists hostnames (example.com), single-label wildcards
	// (*.example.com), IP literals, and CIDRs permitted for webfetch/websearch.
	// Empty/omitted means unrestricted public hosts (SSRF private blocks
	// still apply). When a layer sets allow (including []), it replaces the
	// previous layer's list (project can tighten or clear global).
	Allow []string `json:"allow,omitempty"`
}

// WebSearchConfig is the JSON "webSearch" object — backend for the websearch
// tool. Provider-neutral: add providers without changing the tool contract.
// Empty provider auto-selects brave when its API key env is set; otherwise
// websearch returns structured setup guidance (no silent scrape fallback).
type WebSearchConfig struct {
	// Provider is the search backend id. Currently "brave". Empty = auto.
	Provider string `json:"provider,omitempty"`
	// APIKeyEnv is the environment variable holding the API key.
	// Empty defaults per provider (brave → BRAVE_API_KEY).
	APIKeyEnv string `json:"apiKeyEnv,omitempty"`
	// BaseURL overrides the provider API base (tests / enterprise proxies).
	// Empty uses the provider default (brave → https://api.search.brave.com).
	BaseURL string `json:"baseURL,omitempty"`
}

// SchedulerConfig is the JSON "scheduler" object.
//
// Limits, presets, and command rules are process-local only — separate Strike
// OS processes do not coordinate. Compile via SchedulerEffective /
// scheduler.CompileWithPresets (presets expand into ordinary limits/rules).
type SchedulerConfig struct {
	// Presets lists shipped build-system preset IDs (cmake, ninja, gradle,
	// bazel, maven, cargo, npm). Expanded at compile time into suggested
	// limits and command rules before user Limits/Commands apply.
	Presets []string `json:"presets,omitempty"`
	// Limits maps pool name (process|build|test|model|container) → positive
	// capacity. Omitted pools stay unlimited. Explicit 0 or negative fails load.
	// User limits overlay preset-suggested capacities per pool.
	Limits scheduler.Limits `json:"limits,omitempty"`
	// Commands are ordered classification rules (pattern glob → class).
	// Appended after expanded preset rules; last match wins. Empty list with
	// no presets means every command is general.
	Commands []scheduler.CommandRule `json:"commands,omitempty"`
}

// HarnessConfig is one named external subprocess harness command. Embedded Go
// harnesses are compile-time registrations and cannot be declared in config.
type HarnessConfig struct {
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// MCPConfig is the JSON "mcp" object.
type MCPConfig struct {
	// Servers maps a short name to a server entry. Names become the
	// mcp_<name>_* tool namespace.
	Servers map[string]MCPServer `json:"servers,omitempty"`
}

// MCPServer is one MCP server entry (stdio command or streamable HTTP URL).
type MCPServer struct {
	// Type is "stdio" (default) or "http". Empty with url set implies http.
	Type string `json:"type,omitempty"`
	// Stdio
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	// HTTP (streamable HTTP endpoint)
	URL string `json:"url,omitempty"`
	// Headers are sent on every HTTP request; never logged (e.g. Authorization).
	Headers map[string]string `json:"headers,omitempty"`
}

// LSPConfig is the JSON "lsp" object.
type LSPConfig struct {
	// Servers maps a short name to a language server entry. Extensions on each
	// entry build the runtime registry (first server claiming an extension wins).
	Servers map[string]LSPServer `json:"servers,omitempty"`
	// DiagnosticsSeverity is the minimum severity injected into file-tool
	// results: error (default), warning, info, or hint. Errors-only by default;
	// set warning (or lower importance) to opt in to warnings.
	DiagnosticsSeverity string `json:"diagnosticsSeverity,omitempty"`
	// DiagnosticsMaxChars caps injected diagnostic text in tool results
	// (default 4000). Prevents a broken project from blowing the context window.
	DiagnosticsMaxChars int `json:"diagnosticsMaxChars,omitempty"`
	// DiagnosticsWaitMs is how long file tools wait for publishDiagnostics
	// after a mutation before snapshotting (default 400). Multi-file patches
	// share one wait window.
	DiagnosticsWaitMs int `json:"diagnosticsWaitMs,omitempty"`
}

// LSPServer is one language server entry (stdio command + file extensions).
type LSPServer struct {
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	// Extensions lists file extensions this server handles (e.g. ".go", "ts").
	// Required — servers without extensions are ignored at start.
	Extensions []string `json:"extensions,omitempty"`
}

// SessionConfig is the JSON "session" object in config.
type SessionConfig struct {
	// Worktree is off (default), auto (second+ concurrent root), or always.
	Worktree string `json:"worktree,omitempty"`
	// WorktreeCleanup is keep (default) or delete on session close.
	WorktreeCleanup string `json:"worktreeCleanup,omitempty"`
	// OverlapPolicy is off|warn|block for multi-agent path conflicts
	// (default warn). warn surfaces tool warnings + path.overlap events;
	// block refuses conflicting writes; off tracks without signaling.
	OverlapPolicy string `json:"overlapPolicy,omitempty"`
	// RetentionMaxSessions caps closed durable sessions under
	// ~/.strike/sessions (0 = unlimited). Applied via session.ApplyRetention
	// / RetentionFromConfig — not automatic on every launch.
	RetentionMaxSessions int `json:"retentionMaxSessions,omitempty"`
	// RetentionMaxAgeDays deletes closed sessions older than N days (0 = off).
	RetentionMaxAgeDays int `json:"retentionMaxAgeDays,omitempty"`
	// RetentionMaxBytes caps total closed session log+meta bytes (0 = off).
	RetentionMaxBytes int64 `json:"retentionMaxBytes,omitempty"`
	// TimelineMaxEntries caps in-memory structured run timeline entries
	// (0 = pkg/timeline default 10000; negative in API disables — JSON 0 keeps default).
	// Oldest terminal entries are pruned first (#810).
	TimelineMaxEntries int `json:"timelineMaxEntries,omitempty"`
	// TimelineArgsPreviewMax / TimelineOutputPreviewMax override inline payload
	// preview rune caps (0 = library defaults 512 / 2048). Oversized payloads
	// truncate; with TimelineBlobSpill they also spill to content-addressed blobs.
	TimelineArgsPreviewMax   int `json:"timelineArgsPreviewMax,omitempty"`
	TimelineOutputPreviewMax int `json:"timelineOutputPreviewMax,omitempty"`
	// TimelineBlobSpill enables spilling oversized redacted tool payloads under
	// ~/.strike/traces/<sessionId>/blobs/ (blob:sha256: refs on timeline entries).
	// Spill writes do not fsync — session JSONL remains the durability boundary.
	TimelineBlobSpill bool `json:"timelineBlobSpill,omitempty"`
	// TraceRetentionMaxFiles/AgeDays/Bytes bound ~/.strike/traces and
	// ~/.strike/runs session trees (0 = unlimited). Applied via
	// session.ApplyTraceRetention / ApplyRetentionWithSidecars — coordinates
	// with session.retention* (#803); not automatic on every launch.
	TraceRetentionMaxFiles   int   `json:"traceRetentionMaxFiles,omitempty"`
	TraceRetentionMaxAgeDays int   `json:"traceRetentionMaxAgeDays,omitempty"`
	TraceRetentionMaxBytes   int64 `json:"traceRetentionMaxBytes,omitempty"`
	// AgentBudget is the default per-child resource limit for task spawns
	// (#774). Spawn-time task.budget fields overlay non-zero values. Zero
	// means unlimited for that dimension (soft stall/loop signals still
	// apply). Distinct from future session maxSessionCostUSD (#577), which
	// remains the outer cost envelope when configured.
	AgentBudget AgentBudgetConfig `json:"agentBudget,omitempty"`
}

// AgentBudgetConfig is JSON for session.agentBudget (camelCase).
type AgentBudgetConfig struct {
	MaxWallClockS     int     `json:"maxWallClockS,omitempty"`
	MaxTokens         int     `json:"maxTokens,omitempty"`
	MaxCostUSD        float64 `json:"maxCostUsd,omitempty"`
	MaxToolCalls      int     `json:"maxToolCalls,omitempty"`
	MaxDangerousTools int     `json:"maxDangerousTools,omitempty"`
	StallAfterS       int     `json:"stallAfterS,omitempty"`
	LoopDetectN       int     `json:"loopDetectN,omitempty"`
}

// Hook is one lifecycle hook entry. Exactly one of Action or Command should
// be set:
//   - Action (log|block|notify): declarative rule evaluated in-process
//   - Command: shell hook (event JSON on stdin; exit allow/block; stdout inject)
type Hook struct {
	// Event is pre_tool_use, post_tool_use, turn_start, or turn_end.
	// Shell hooks only run for pre_tool_use / post_tool_use.
	Event string `json:"event"`
	// Matcher is a doublestar glob over the tool name; empty matches all.
	Matcher string `json:"matcher,omitempty"`
	// Action is log, block, or notify (declarative). Mutually exclusive with Command.
	Action string `json:"action,omitempty"`
	// Message is optional text for block/notify declarative rules.
	Message string `json:"message,omitempty"`
	// Command runs via bash -c with the event payload on stdin (shell hook).
	Command string `json:"command,omitempty"`
	// TimeoutMs bounds shell execution (default 30000, max 120000).
	TimeoutMs int `json:"timeoutMs,omitempty"`
}

// IsShell reports a shell-command hook (has command, no action).
func (h Hook) IsShell() bool {
	return strings.TrimSpace(h.Command) != "" && strings.TrimSpace(h.Action) == ""
}

// IsRule reports a declarative action hook (has action, no command).
func (h Hook) IsRule() bool {
	return strings.TrimSpace(h.Action) != "" && strings.TrimSpace(h.Command) == ""
}

// ShellHooks returns shell-command entries for the engine.
func (c Config) ShellHooks() []Hook {
	out := make([]Hook, 0, len(c.Hooks))
	for _, h := range c.Hooks {
		if h.IsShell() {
			out = append(out, h)
		}
	}
	return out
}

// HookRules returns declarative rules for the permission/engine evaluator.
func (c Config) HookRules() permission.HookRuleset {
	out := make(permission.HookRuleset, 0, len(c.Hooks))
	for _, h := range c.Hooks {
		if !h.IsRule() {
			continue
		}
		out = append(out, permission.HookRule{
			Event:   h.Event,
			Matcher: h.Matcher,
			Action:  h.Action,
			Message: h.Message,
		})
	}
	return out
}

func Default() Config {
	return Config{
		Provider: "anthropic",
		// Default language servers (E2.3). Missing binaries degrade to
		// per-server error status; clear with "lsp": {"servers": {}}.
		LSP: LSPConfig{Servers: DefaultLSPServers()},
	}
}

// DefaultLSPServers is the shipped language-server map: Go, TypeScript,
// Python, and Rust. First server claiming an extension wins at runtime.
func DefaultLSPServers() map[string]LSPServer {
	return map[string]LSPServer{
		"go": {
			Command:    "gopls",
			Extensions: []string{".go"},
		},
		"typescript": {
			Command:    "typescript-language-server",
			Args:       []string{"--stdio"},
			Extensions: []string{".ts", ".tsx", ".js", ".jsx"},
		},
		"python": {
			Command:    "pylsp",
			Extensions: []string{".py"},
		},
		"rust": {
			Command:    "rust-analyzer",
			Extensions: []string{".rs"},
		},
	}
}

// DefaultModel is used when neither config nor flags set a model.
//
// Precedence for the session model id (deterministic):
//  1. config/flag model when set
//  2. DefaultModelCustom first configured key (custom providers)
//  3. DefaultModel(provider) built-in catalog default
//  4. empty (caller may require freeform /model)
//
// Built-in defaults below are strike's catalog pins, not models.dev order.
// Provider ids are canonicalized (gemini → google). Model ids keep vendor names
// (e.g. gemini-2.5-pro for the google provider).
func DefaultModel(provider string) string {
	switch CanonicalProviderID(provider) {
	case "openai":
		return "gpt-5.5"
	case "xai":
		return "grok-4.5"
	case "google":
		return "gemini-2.5-pro"
	case "kimi":
		return "moonshot-v1"
	case "deepseek":
		return "deepseek-chat"
	case "echo":
		return "echo"
	default:
		return "claude-sonnet-5"
	}
}

// DefaultModelCustom returns the first configured model id for a custom
// provider, or empty when none are listed (caller may leave model unset).
// Nested providers.jsonc object key order is sorted alphabetically at parse
// time; legacy []string keeps file order.
func DefaultModelCustom(p CustomProvider) string {
	if len(p.Models) > 0 {
		return p.Models[0]
	}
	if len(p.ModelDefs) > 0 {
		return p.ModelDefs[0].ID
	}
	return ""
}

// GlobalRoot is ~/.strike — strike's home for all user-level state. Existing
// directory symlinks are resolved so the state directory can live elsewhere
// (history/memory/issue open with O_NOFOLLOW require a real directory).
func GlobalRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return resolveExisting(filepath.Join(home, ".strike"))
}

// GlobalPath is the global config file, ~/.strike/config (JSON or JSONC).
func GlobalPath() string {
	root := GlobalRoot()
	if root == "" {
		return ""
	}
	return filepath.Join(root, "config")
}

// projectRoot is the per-project .strike directory. Existing directory
// symlinks are resolved so project state can live outside the work tree.
func projectRoot(workDir string) string {
	if workDir == "" {
		return ""
	}
	return resolveExisting(filepath.Join(workDir, ".strike"))
}

// resolveExisting returns path with symlinks resolved when the path exists.
// Missing paths (first-run) are returned unchanged so callers can create them.
func resolveExisting(path string) string {
	if path == "" {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}

// Load merges:
//
//	default → ~/.strike/config → ~/.strike/mcp.jsonc → ~/.strike/providers.jsonc
//	→ ~/.strike/keybinds.jsonc → ./.strike/config → ./.strike/mcp.jsonc
//	→ ./.strike/providers.jsonc → ./.strike/keybinds.jsonc
//	→ managed/MDM (system managed-config + managed-config.d; highest)
//
// mcp.jsonc/json is preferred for MCP servers (see ReadMCPFile); the legacy
// mcp object in config still works. providers.jsonc is OpenCode-compatible
// (see ReadProvidersFile); the legacy providers array in config still works.
// Dedicated keybinds.jsonc/json overrides the config keybinds object in the
// same root (last-wins per id). Managed scalars and permission rules override
// user/project; see ManagedInfo and docs/config.md (enterprise).
func Load(workDir string) (Config, error) {
	cfg := Default()
	// Global config JSON (optional).
	if path := GlobalPath(); path != "" {
		layer, err := read(path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			// ok
		case err != nil:
			return cfg, err
		default:
			cfg = merge(cfg, layer)
		}
	}
	// Global mcp.jsonc/json (optional; loads even when config is absent).
	if mc, err := loadMCPFileLayer(GlobalRoot()); err != nil {
		return cfg, err
	} else {
		cfg.MCP = mergeMCP(cfg.MCP, mc)
	}
	// Global providers.jsonc/json (optional; loads even when config is absent).
	if pf, err := loadProvidersFileLayer(GlobalRoot()); err != nil {
		return cfg, err
	} else {
		cfg = applyProvidersFile(cfg, pf)
	}
	// Global keybinds.jsonc/json (optional; overrides config keybinds).
	if kb, err := loadKeybindsFileLayer(GlobalRoot()); err != nil {
		return cfg, err
	} else if len(kb) > 0 {
		cfg.Keybinds = MergeKeybinds(cfg.Keybinds, kb)
	}
	// Project config JSON (optional).
	if workDir != "" {
		path := filepath.Join(projectRoot(workDir), "config")
		layer, err := read(path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			// ok
		case err != nil:
			return cfg, err
		default:
			cfg = merge(cfg, layer)
		}
		if mc, err := loadMCPFileLayer(projectRoot(workDir)); err != nil {
			return cfg, err
		} else {
			cfg.MCP = mergeMCP(cfg.MCP, mc)
		}
		if pf, err := loadProvidersFileLayer(projectRoot(workDir)); err != nil {
			return cfg, err
		} else {
			cfg = applyProvidersFile(cfg, pf)
		}
		if kb, err := loadKeybindsFileLayer(projectRoot(workDir)); err != nil {
			return cfg, err
		} else if len(kb) > 0 {
			cfg.Keybinds = MergeKeybinds(cfg.Keybinds, kb)
		}
	}
	// Managed/MDM last: system policy overrides global and project.
	managed, info, err := LoadManaged()
	if err != nil {
		return cfg, err
	}
	if info.Active() {
		cfg = merge(cfg, managed)
		cfg.Managed = info
	}
	cfg.Provider = CanonicalProviderID(cfg.Provider)
	return cfg, nil
}

// applyProvidersFile merges customs/overlays/endpoints and disable-default flags.
func applyProvidersFile(cfg Config, pf ProvidersFile) Config {
	if len(pf.Customs) > 0 {
		cfg.Providers = mergeProviders(cfg.Providers, pf.Customs)
	}
	if len(pf.Overlays) > 0 {
		cfg.ModelOverlays = mergeOverlayMaps(cfg.ModelOverlays, pf.Overlays)
	}
	if len(pf.Endpoints) > 0 {
		cfg.EndpointOverlays = mergeEndpointMaps(cfg.EndpointOverlays, pf.Endpoints)
	}
	return mergeDisableDefaultFromFile(cfg, pf)
}

// IsBuiltinProviderDisabled reports whether a builtin catalog provider is
// hidden by disable-default-providers / disable-default-<name>. Customs are
// never disabled. Per-provider false overrides a bulk true.
// Alias ids (gemini) are canonicalized to google before lookup.
func (c Config) IsBuiltinProviderDisabled(name string) bool {
	name = CanonicalProviderID(name)
	if name == "" {
		return false
	}
	if _, ok := BuiltinProviderNames[name]; !ok {
		return false
	}
	if v, ok := c.DisableDefaultPer[name]; ok {
		return v
	}
	return c.DisableDefaultProviders
}

func mergeDisableDefaultFromFile(cfg Config, pf ProvidersFile) Config {
	if pf.DisableDefaultAll != nil {
		cfg.DisableDefaultProviders = *pf.DisableDefaultAll
		cfg.disableDefaultProvidersSet = true
	}
	if len(pf.DisableDefaultPer) > 0 {
		cfg.DisableDefaultPer = mergeDisableDefaultPer(cfg.DisableDefaultPer, pf.DisableDefaultPer)
	}
	return cfg
}

func mergeDisableDefaultPer(base, layer map[string]bool) map[string]bool {
	if len(layer) == 0 {
		return cloneBoolMap(base)
	}
	out := cloneBoolMap(base)
	if out == nil {
		out = make(map[string]bool, len(layer))
	}
	for k, v := range layer {
		out[CanonicalProviderID(k)] = v
	}
	return out
}

func read(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	stripped, err := stripJSONC(data)
	if err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	var c Config
	if err := json.Unmarshal(stripped, &c); err != nil {
		return Config{}, err
	}
	c.Provider = CanonicalProviderID(c.Provider)
	// disable-default-* top-level keys (same names as providers.jsonc).
	// "$schema" and other unknown keys are ignored by encoding/json.
	if all, per, err := parseDisableDefaultFlags(stripped); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	} else {
		if all != nil {
			c.DisableDefaultProviders = *all
			c.disableDefaultProvidersSet = true
		}
		if len(per) > 0 {
			c.DisableDefaultPer = per
		}
	}
	// Drop invalid custom providers at load so a bad row does not brick startup.
	if len(c.Providers) > 0 {
		valid := make([]CustomProvider, 0, len(c.Providers))
		for _, p := range c.Providers {
			p = NormalizeCustomProvider(p)
			if err := p.Validate(); err != nil {
				continue
			}
			valid = append(valid, p)
		}
		c.Providers = valid
	}
	// Drop invalid hooks (neither shell nor rule, or bad declarative fields).
	if len(c.Hooks) > 0 {
		valid := make([]Hook, 0, len(c.Hooks))
		for _, h := range c.Hooks {
			switch {
			case h.IsShell():
				// Shell hooks only fire on tool events; keep any event string
				// and let the engine matcher filter.
				if strings.TrimSpace(h.Event) == "" {
					continue
				}
				valid = append(valid, h)
			case h.IsRule():
				rule := permission.HookRule{
					Event:   h.Event,
					Matcher: h.Matcher,
					Action:  h.Action,
					Message: h.Message,
				}
				if err := permission.ValidateHookRule(rule); err != nil {
					continue
				}
				valid = append(valid, h)
			default:
				// Both action+command or neither: drop.
			}
		}
		c.Hooks = valid
	}
	c.PermissionAutoApproveSeconds = ClampPermissionAutoApproveSeconds(c.PermissionAutoApproveSeconds)
	c.PermissionAutoApproveExclude = normalizePermissionAutoApproveExclude(c.PermissionAutoApproveExclude)
	c.MaxChildDepth = ClampMaxChildDepth(c.MaxChildDepth)
	c.ToolRetry = normalizeToolRetry(c.ToolRetry)
	if c.PermissionMode != "" {
		mode, ok := protocol.ParsePermissionMode(string(c.PermissionMode))
		if !ok {
			return Config{}, fmt.Errorf("%s: unknown permissionMode %q (want default|plan|soft-approve|accept-edits|yolo)", path, c.PermissionMode)
		}
		c.PermissionMode = mode
	}
	if c.PermissionPreset != "" {
		c.PermissionPreset = strings.ToLower(strings.TrimSpace(c.PermissionPreset))
		if !permission.ValidPresetID(c.PermissionPreset) {
			return Config{}, fmt.Errorf("%s: unknown permissionPreset %q (want read-only|dev|yolo-with-sandbox)", path, c.PermissionPreset)
		}
	}
	if strings.TrimSpace(c.Sandbox) != "" {
		mode, ok := sandbox.ParseMode(c.Sandbox)
		if !ok {
			return Config{}, fmt.Errorf("%s: unknown sandbox %q (want %s)", path, c.Sandbox, sandbox.ModeNames())
		}
		c.Sandbox = mode.String()
	}
	if c.Network.Allow != nil {
		norm, err := sandbox.NormalizeNetworkAllow(c.Network.Allow)
		if err != nil {
			return Config{}, fmt.Errorf("%s: %w", path, err)
		}
		c.Network.Allow = norm
	}
	c.WebSearch = NormalizeWebSearch(c.WebSearch)
	if err := ValidateWebSearch(c.WebSearch); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	c.CompactionStrategy = NormalizeCompactionStrategy(c.CompactionStrategy)
	c.CompactionModel = strings.TrimSpace(c.CompactionModel)
	c.CompactionThreshold = ClampCompactionThreshold(c.CompactionThreshold)
	c.CompactionBuffer = ClampCompactionBuffer(c.CompactionBuffer)
	c.KeepUserTurns = ClampKeepUserTurns(c.KeepUserTurns)
	c.PruneProtectTokens = ClampPruneProtectTokens(c.PruneProtectTokens)
	c.PruneMinimumTokens = ClampPruneMinimumTokens(c.PruneMinimumTokens)
	c.PruneKeepUserTurns = ClampPruneKeepUserTurns(c.PruneKeepUserTurns)
	c.PruneProtectTools = NormalizePruneProtectTools(c.PruneProtectTools)
	c.Notify = NormalizeNotify(c.Notify)
	c.LeanCode = NormalizeLeanCode(c.LeanCode)
	c.DeferTools = NormalizeDeferTools(c.DeferTools)
	// Keybinds: unknown ids / invalid chords fail the layer (and thus Load).
	if err := ValidateKeybinds(c.Keybinds); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	for name, harness := range c.Harnesses {
		if err := validateConfigIdentifier(name, "harness"); err != nil {
			return Config{}, fmt.Errorf("%s: %w", path, err)
		}
		if strings.TrimSpace(name) != name {
			return Config{}, fmt.Errorf("%s: harness name %q has leading or trailing whitespace", path, name)
		}
		if strings.TrimSpace(harness.Command) == "" {
			return Config{}, fmt.Errorf("%s: harness %q command is empty", path, name)
		}
	}
	// Scheduler: validate limits/rules and stamp command provenance with path.
	if err := normalizeSchedulerLayer(&c.Scheduler, path); err != nil {
		return Config{}, err
	}
	return c, nil
}

// normalizeSchedulerLayer validates scheduler presets, limits, and command
// rules for one config file and stamps each rule's Source with path for
// startup reports.
func normalizeSchedulerLayer(sc *SchedulerConfig, path string) error {
	if sc == nil {
		return nil
	}
	if err := scheduler.ValidatePresetIDs(sc.Presets, path); err != nil {
		return err
	}
	// Normalize preset id whitespace; drop empties already rejected above.
	if len(sc.Presets) > 0 {
		out := make([]string, 0, len(sc.Presets))
		for _, id := range sc.Presets {
			out = append(out, strings.TrimSpace(id))
		}
		sc.Presets = out
	}
	if err := scheduler.ValidateLimits(sc.Limits, path); err != nil {
		return err
	}
	if len(sc.Commands) == 0 {
		return nil
	}
	out := make([]scheduler.CommandRule, 0, len(sc.Commands))
	for i, r := range sc.Commands {
		r.Source = path
		if err := scheduler.ValidateCommandRule(r, i, path); err != nil {
			return err
		}
		// Normalize class whitespace for stable effective output.
		r.Class = scheduler.Class(strings.TrimSpace(string(r.Class)))
		out = append(out, r)
	}
	sc.Commands = out
	return nil
}

// MaxChildDepthCeiling is the hard upper bound for nested task spawns
// (matches engine absoluteMaxChildDepth).
const MaxChildDepthCeiling = 8

// ClampPermissionAutoApproveSeconds maps config values: ≤0 → 0 (off), >60 → 60.
func ClampPermissionAutoApproveSeconds(n int) int {
	if n <= 0 {
		return 0
	}
	if n > 60 {
		return 60
	}
	return n
}

func normalizeToolRetry(tr ToolRetryConfig) ToolRetryConfig {
	if tr.MaxAttempts < 0 {
		tr.MaxAttempts = 0
	}
	if tr.BaseDelayMs < 0 {
		tr.BaseDelayMs = 0
	}
	if tr.MaxDelayMs < 0 {
		tr.MaxDelayMs = 0
	}
	if tr.LoopThreshold < 0 {
		tr.LoopThreshold = 0
	}
	return tr
}

// mergeAgentBudgetConfig overlays non-zero layer fields onto base.
func mergeAgentBudgetConfig(base, layer AgentBudgetConfig) AgentBudgetConfig {
	if layer.MaxWallClockS != 0 {
		base.MaxWallClockS = layer.MaxWallClockS
	}
	if layer.MaxTokens != 0 {
		base.MaxTokens = layer.MaxTokens
	}
	if layer.MaxCostUSD != 0 {
		base.MaxCostUSD = layer.MaxCostUSD
	}
	if layer.MaxToolCalls != 0 {
		base.MaxToolCalls = layer.MaxToolCalls
	}
	if layer.MaxDangerousTools != 0 {
		base.MaxDangerousTools = layer.MaxDangerousTools
	}
	if layer.StallAfterS != 0 {
		base.StallAfterS = layer.StallAfterS
	}
	if layer.LoopDetectN != 0 {
		base.LoopDetectN = layer.LoopDetectN
	}
	return base
}

// ClampMaxChildDepth maps config values: <0 → 0 (engine default),
// >MaxChildDepthCeiling → MaxChildDepthCeiling.
func ClampMaxChildDepth(n int) int {
	if n < 0 {
		return 0
	}
	if n > MaxChildDepthCeiling {
		return MaxChildDepthCeiling
	}
	return n
}

func normalizePermissionAutoApproveExclude(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, raw := range in {
		name := strings.ToLower(strings.TrimSpace(raw))
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// PermissionAutoApproveExcluded reports whether permission is on the exclude list.
func PermissionAutoApproveExcluded(permission string, exclude []string) bool {
	if len(exclude) == 0 {
		return false
	}
	want := strings.ToLower(strings.TrimSpace(permission))
	for _, name := range exclude {
		if name == want {
			return true
		}
	}
	return false
}

// NormalizeCompactionStrategy maps config aliases to trim|summarize.
// Empty and unknown values become "" (engine default = trim).
func NormalizeCompactionStrategy(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "trim":
		return "trim"
	case "summarize", "summary":
		return "summarize"
	default:
		return ""
	}
}

// ClampCompactionThreshold maps config values: <0 → 0 (engine default),
// values in (0,1) kept, >=1 kept as-is (engine treats >=1 as disabled).
func ClampCompactionThreshold(v float64) float64 {
	if v < 0 {
		return 0
	}
	return v
}

// ClampCompactionBuffer maps config values: <0 → 0 (engine default).
func ClampCompactionBuffer(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

// ClampKeepUserTurns maps config values: <0 → 0 (engine default).
func ClampKeepUserTurns(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

// ClampPruneProtectTokens maps config values: <0 → 0 (engine default).
func ClampPruneProtectTokens(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

// ClampPruneMinimumTokens maps config values: <0 → 0 (engine default).
func ClampPruneMinimumTokens(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

// ClampPruneKeepUserTurns maps config values: <0 → 0 (engine default).
func ClampPruneKeepUserTurns(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

// NormalizePruneProtectTools lowercases, trims, and dedupes tool names.
// Empty / all-blank input becomes nil.
func NormalizePruneProtectTools(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, raw := range in {
		name := strings.ToLower(strings.TrimSpace(raw))
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Notify mode values for Config.Notify / desktop notifications.
const (
	NotifyOn            = "on"
	NotifyOff           = "off"
	NotifyUnfocusedOnly = "unfocused-only"
)

// NormalizeNotify maps config aliases to on|off|unfocused-only.
// Empty and unknown values become "" (TUI default = unfocused-only).
func NormalizeNotify(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "on", "true", "1", "yes", "always":
		return NotifyOn
	case "off", "false", "0", "no", "never":
		return NotifyOff
	case "unfocused-only", "unfocused", "blur":
		return NotifyUnfocusedOnly
	default:
		return ""
	}
}

// LeanCode intensity values for Config.LeanCode / engine lean-code overlays.
const (
	LeanCodeOff  = "off"
	LeanCodeLite = "lite"
	LeanCodeFull = "full"
)

// NormalizeLeanCode maps config aliases to off|lite|full.
// Empty and unknown values become "" (engine default = lite).
func NormalizeLeanCode(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "off", "false", "0", "no", "never", "none":
		return LeanCodeOff
	case "lite", "light", "default":
		return LeanCodeLite
	case "full", "on", "true", "1", "yes":
		return LeanCodeFull
	default:
		return ""
	}
}

// DeferTools values for Config.DeferTools (toolsearch-backed schema deferral).
const (
	DeferToolsOn  = "on"
	DeferToolsOff = "off"
)

// NormalizeDeferTools maps config aliases to on|off.
// Empty and unknown values become "" (default off).
func NormalizeDeferTools(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "on", "true", "1", "yes", "enable", "enabled":
		return DeferToolsOn
	case "off", "false", "0", "no", "disable", "disabled", "never":
		return DeferToolsOff
	default:
		return ""
	}
}

// DeferToolsEnabled reports whether deferred tool schemas are active.
func DeferToolsEnabled(s string) bool {
	return NormalizeDeferTools(s) == DeferToolsOn
}

// NormalizeWebSearch trims webSearch fields.
func NormalizeWebSearch(c WebSearchConfig) WebSearchConfig {
	c.Provider = strings.ToLower(strings.TrimSpace(c.Provider))
	c.APIKeyEnv = strings.TrimSpace(c.APIKeyEnv)
	c.BaseURL = strings.TrimSpace(c.BaseURL)
	return c
}

// ValidateWebSearch rejects unknown providers. Empty provider is valid (auto).
func ValidateWebSearch(c WebSearchConfig) error {
	switch c.Provider {
	case "", "brave":
		return nil
	default:
		return fmt.Errorf("unknown webSearch.provider %q (want brave or empty for auto)", c.Provider)
	}
}

func merge(base, layer Config) Config {
	if layer.Provider != "" {
		base.Provider = CanonicalProviderID(layer.Provider)
	}
	if layer.Model != "" {
		base.Model = layer.Model
	}
	if layer.Effort != "" {
		base.Effort = layer.Effort
	}
	if layer.SystemPrompt != "" {
		base.SystemPrompt = layer.SystemPrompt
	}
	if layer.LeanCode != "" {
		base.LeanCode = layer.LeanCode
	}
	if layer.DeferTools != "" {
		base.DeferTools = layer.DeferTools
	}
	if layer.DefaultAgent != "" {
		base.DefaultAgent = layer.DefaultAgent
	}
	if layer.Theme != "" {
		base.Theme = layer.Theme
	}
	if layer.VimMode != "" {
		base.VimMode = layer.VimMode
	}
	if layer.NanoMode != "" {
		base.NanoMode = layer.NanoMode
	}
	if layer.MdReadMode != "" {
		base.MdReadMode = layer.MdReadMode
	}
	if layer.Notify != "" {
		base.Notify = layer.Notify
	}
	if layer.PermissionAutoApproveSeconds != 0 {
		base.PermissionAutoApproveSeconds = layer.PermissionAutoApproveSeconds
	}
	if layer.PermissionAutoApproveExclude != nil {
		base.PermissionAutoApproveExclude = layer.PermissionAutoApproveExclude
	}
	if layer.PermissionMode != "" {
		base.PermissionMode = layer.PermissionMode
	}
	if layer.PermissionPreset != "" {
		base.PermissionPreset = layer.PermissionPreset
	}
	if layer.Sandbox != "" {
		base.Sandbox = layer.Sandbox
	}
	// network.allow: non-nil layer slice replaces (including explicit []).
	// make+copy so len-0 stays non-nil (append to nil yields nil).
	if layer.Network.Allow != nil {
		out := make([]string, len(layer.Network.Allow))
		copy(out, layer.Network.Allow)
		base.Network.Allow = out
	}
	// webSearch: any non-empty field on the layer replaces the whole object
	// (project can override provider/key/baseURL as a unit).
	if layer.WebSearch.Provider != "" || layer.WebSearch.APIKeyEnv != "" || layer.WebSearch.BaseURL != "" {
		base.WebSearch = layer.WebSearch
	}
	if layer.CompactionStrategy != "" {
		base.CompactionStrategy = layer.CompactionStrategy
	}
	if layer.CompactionModel != "" {
		base.CompactionModel = layer.CompactionModel
	}
	if layer.CompactionThreshold != 0 {
		base.CompactionThreshold = layer.CompactionThreshold
	}
	if layer.CompactionBuffer != 0 {
		base.CompactionBuffer = layer.CompactionBuffer
	}
	if layer.KeepUserTurns != 0 {
		base.KeepUserTurns = layer.KeepUserTurns
	}
	if layer.PruneProtectTokens != 0 {
		base.PruneProtectTokens = layer.PruneProtectTokens
	}
	if layer.PruneMinimumTokens != 0 {
		base.PruneMinimumTokens = layer.PruneMinimumTokens
	}
	if layer.PruneKeepUserTurns != 0 {
		base.PruneKeepUserTurns = layer.PruneKeepUserTurns
	}
	if layer.PruneProtectTools != nil {
		base.PruneProtectTools = layer.PruneProtectTools
	}
	if layer.MaxChildDepth != 0 {
		base.MaxChildDepth = layer.MaxChildDepth
	}
	if layer.Session.Worktree != "" {
		base.Session.Worktree = layer.Session.Worktree
	}
	if layer.Session.WorktreeCleanup != "" {
		base.Session.WorktreeCleanup = layer.Session.WorktreeCleanup
	}
	if layer.Session.OverlapPolicy != "" {
		base.Session.OverlapPolicy = layer.Session.OverlapPolicy
	}
	if layer.Session.RetentionMaxSessions != 0 {
		base.Session.RetentionMaxSessions = layer.Session.RetentionMaxSessions
	}
	if layer.Session.RetentionMaxAgeDays != 0 {
		base.Session.RetentionMaxAgeDays = layer.Session.RetentionMaxAgeDays
	}
	if layer.Session.RetentionMaxBytes != 0 {
		base.Session.RetentionMaxBytes = layer.Session.RetentionMaxBytes
	}
	if layer.Session.TimelineMaxEntries != 0 {
		base.Session.TimelineMaxEntries = layer.Session.TimelineMaxEntries
	}
	if layer.Session.TimelineArgsPreviewMax != 0 {
		base.Session.TimelineArgsPreviewMax = layer.Session.TimelineArgsPreviewMax
	}
	if layer.Session.TimelineOutputPreviewMax != 0 {
		base.Session.TimelineOutputPreviewMax = layer.Session.TimelineOutputPreviewMax
	}
	if layer.Session.TimelineBlobSpill {
		base.Session.TimelineBlobSpill = true
	}
	if layer.Session.TraceRetentionMaxFiles != 0 {
		base.Session.TraceRetentionMaxFiles = layer.Session.TraceRetentionMaxFiles
	}
	if layer.Session.TraceRetentionMaxAgeDays != 0 {
		base.Session.TraceRetentionMaxAgeDays = layer.Session.TraceRetentionMaxAgeDays
	}
	if layer.Session.TraceRetentionMaxBytes != 0 {
		base.Session.TraceRetentionMaxBytes = layer.Session.TraceRetentionMaxBytes
	}
	base.Session.AgentBudget = mergeAgentBudgetConfig(base.Session.AgentBudget, layer.Session.AgentBudget)
	base.Permissions = append(base.Permissions, layer.Permissions...)
	base.Hooks = append(base.Hooks, layer.Hooks...)
	base.Providers = mergeProviders(base.Providers, layer.Providers)
	base.Keybinds = MergeKeybinds(base.Keybinds, layer.Keybinds)
	base.MCP = mergeMCP(base.MCP, layer.MCP)
	base.LSP = mergeLSP(base.LSP, layer.LSP)
	base.Harnesses = mergeHarnesses(base.Harnesses, layer.Harnesses)
	base.Scheduler = mergeScheduler(base.Scheduler, layer.Scheduler)
	base.ToolRetry = mergeToolRetry(base.ToolRetry, layer.ToolRetry)
	if layer.disableDefaultProvidersSet {
		base.DisableDefaultProviders = layer.DisableDefaultProviders
		base.disableDefaultProvidersSet = true
	}
	if len(layer.DisableDefaultPer) > 0 {
		base.DisableDefaultPer = mergeDisableDefaultPer(base.DisableDefaultPer, layer.DisableDefaultPer)
	}
	return base
}

// mergeToolRetry overlays non-zero tool-retry knobs from layer onto base.
func mergeToolRetry(base, layer ToolRetryConfig) ToolRetryConfig {
	if layer.MaxAttempts != 0 {
		base.MaxAttempts = layer.MaxAttempts
	}
	if layer.BaseDelayMs != 0 {
		base.BaseDelayMs = layer.BaseDelayMs
	}
	if layer.MaxDelayMs != 0 {
		base.MaxDelayMs = layer.MaxDelayMs
	}
	if layer.LoopThreshold != 0 {
		base.LoopThreshold = layer.LoopThreshold
	}
	return base
}

// mergeScheduler merges preset IDs (deduped), overlays limits per pool, and
// appends command rules (later layers win under last-match-wins classification).
func mergeScheduler(base, layer SchedulerConfig) SchedulerConfig {
	return SchedulerConfig{
		Presets: scheduler.MergePresetIDs(base.Presets, layer.Presets),
		Limits:  scheduler.MergeLimits(base.Limits, layer.Limits),
		Commands: append(
			scheduler.CloneCommandRules(base.Commands),
			scheduler.CloneCommandRules(layer.Commands)...,
		),
	}
}

// SchedulerEffective compiles the loaded scheduler policy for admission wiring.
// Shipped presets expand into ordinary limits and command rules first; user
// limits overlay suggested capacities and user commands append after preset
// rules. Safe when Scheduler is empty (unlimited defaults, no command rules).
func (c Config) SchedulerEffective() (*scheduler.Effective, error) {
	return scheduler.CompileWithPresets(c.Scheduler.Presets, c.Scheduler.Limits, c.Scheduler.Commands, "")
}

func mergeHarnesses(base, layer map[string]HarnessConfig) map[string]HarnessConfig {
	out := cloneHarnesses(base)
	if out == nil && layer != nil {
		out = make(map[string]HarnessConfig, len(layer))
	}
	for name, harness := range layer {
		out[name] = cloneHarnessConfig(harness)
	}
	return out
}

func cloneHarnesses(in map[string]HarnessConfig) map[string]HarnessConfig {
	if in == nil {
		return nil
	}
	out := make(map[string]HarnessConfig, len(in))
	for name, harness := range in {
		out[name] = cloneHarnessConfig(harness)
	}
	return out
}

func cloneHarnessConfig(in HarnessConfig) HarnessConfig {
	out := HarnessConfig{Command: in.Command, Args: append([]string(nil), in.Args...)}
	if in.Env != nil {
		out.Env = make(map[string]string, len(in.Env))
		for name, value := range in.Env {
			out.Env[name] = value
		}
	}
	return out
}

// parseDisableDefaultFlags extracts disable-default-providers and
// disable-default-<name> booleans from a JSON object (config or providers map).
func parseDisableDefaultFlags(data []byte) (all *bool, per map[string]bool, err error) {
	stripped := bytesTrimSpace(data)
	if len(stripped) == 0 || stripped[0] != '{' {
		return nil, nil, nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(stripped, &raw); err != nil {
		return nil, nil, err
	}
	for k, v := range raw {
		handled, herr := applyDisableDefaultKey(&all, &per, k, v)
		if herr != nil {
			return nil, nil, herr
		}
		_ = handled
	}
	return all, per, nil
}

// mergeMCP: when layer sets Servers (including empty map), it replaces base.
// Omitted mcp / nil servers leaves base unchanged.
func mergeMCP(base, layer MCPConfig) MCPConfig {
	if layer.Servers != nil {
		return MCPConfig{Servers: cloneMCPServers(layer.Servers)}
	}
	if base.Servers != nil {
		return MCPConfig{Servers: cloneMCPServers(base.Servers)}
	}
	return MCPConfig{}
}

func cloneMCPServers(in map[string]MCPServer) map[string]MCPServer {
	if in == nil {
		return nil
	}
	out := make(map[string]MCPServer, len(in))
	for k, v := range in {
		s := MCPServer{
			Type:    strings.TrimSpace(v.Type),
			Command: strings.TrimSpace(v.Command),
			Args:    append([]string(nil), v.Args...),
			URL:     strings.TrimSpace(v.URL),
		}
		if len(v.Env) > 0 {
			s.Env = make(map[string]string, len(v.Env))
			for ek, ev := range v.Env {
				s.Env[ek] = ev
			}
		}
		if len(v.Headers) > 0 {
			s.Headers = make(map[string]string, len(v.Headers))
			for hk, hv := range v.Headers {
				s.Headers[hk] = hv
			}
		}
		out[k] = s
	}
	return out
}

// mergeLSP: when layer sets Servers (including empty map), it replaces base
// servers. Omitted servers leaves base servers. Scalar diagnostics knobs
// overlay last-wins when the layer sets a non-zero/non-empty value.
func mergeLSP(base, layer LSPConfig) LSPConfig {
	out := LSPConfig{}
	if layer.Servers != nil {
		out.Servers = cloneLSPServers(layer.Servers)
	} else if base.Servers != nil {
		out.Servers = cloneLSPServers(base.Servers)
	}
	out.DiagnosticsSeverity = base.DiagnosticsSeverity
	if strings.TrimSpace(layer.DiagnosticsSeverity) != "" {
		out.DiagnosticsSeverity = strings.TrimSpace(layer.DiagnosticsSeverity)
	}
	out.DiagnosticsMaxChars = base.DiagnosticsMaxChars
	if layer.DiagnosticsMaxChars != 0 {
		out.DiagnosticsMaxChars = layer.DiagnosticsMaxChars
	}
	out.DiagnosticsWaitMs = base.DiagnosticsWaitMs
	if layer.DiagnosticsWaitMs != 0 {
		out.DiagnosticsWaitMs = layer.DiagnosticsWaitMs
	}
	return out
}

func cloneLSPServers(in map[string]LSPServer) map[string]LSPServer {
	if in == nil {
		return nil
	}
	out := make(map[string]LSPServer, len(in))
	for k, v := range in {
		s := LSPServer{
			Command: strings.TrimSpace(v.Command),
			Args:    append([]string(nil), v.Args...),
		}
		if len(v.Env) > 0 {
			s.Env = make(map[string]string, len(v.Env))
			for ek, ev := range v.Env {
				s.Env[ek] = ev
			}
		}
		if len(v.Extensions) > 0 {
			s.Extensions = append([]string(nil), v.Extensions...)
		}
		out[k] = s
	}
	return out
}
