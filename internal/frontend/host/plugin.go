package host

import "context"

// Plugin scope labels for PluginInfo.Scope.
const (
	PluginScopeGlobal  = "global"
	PluginScopeProject = "project"
)

// Plugin trust state labels for PluginInfo.TrustState.
const (
	PluginTrustNone        = "none"
	PluginTrustTrusted     = "trusted"
	PluginTrustStale       = "stale"
	PluginTrustPassiveOnly = "n/a-passive-only"
)

// PluginInfo is one installed plugin for the TUI manager (/plugin).
// Fields are display-safe: no secret or executable environment values.
type PluginInfo struct {
	ID      string
	Version string
	Name    string
	Scope   string // PluginScopeGlobal | PluginScopeProject
	Enabled bool
	// Status is a short lifecycle label: enabled | disabled | invalid.
	Status string
	Digest string
	// Format is agent-plugins | legacy (empty when unknown).
	Format string
	// Schema is plugin.json $schema (APS URI); empty on legacy packages.
	Schema string
	// DisplayName is extensions.com.strike.cli.displayName when set.
	DisplayName string
	// SourceType is local | git | catalog (empty when unknown).
	SourceType string
	// SourceLabel is a scrubbed provenance line (no credentials).
	SourceLabel string
	// TrustState is none | trusted | stale | n/a-passive-only.
	TrustState string
	LoadError  string

	// Contribution counts (passive + executable).
	Agents    int
	Skills    int
	Workflows int
	Themes    int
	Providers int
	Hooks     int
	Panes     int

	// Executable summaries (commands/paths only; env/header values omitted).
	MCP           []PluginMCP
	Harnesses     []PluginHarness
	Capabilities  []string // inferred capability tags for trust binding
	HasExecutable bool
	// Findings are actionable validation/collision/trust messages (redacted).
	Findings []string
	// UpdateAvailable is a newer catalog version when known; empty otherwise.
	UpdateAvailable string
}

// PluginMCP is MCP contribution metadata without env/header values.
type PluginMCP struct {
	Name       string
	Transport  string
	Command    string
	Args       []string
	EnvKeys    []string
	URL        string // redacted if credential-shaped
	HeaderKeys []string
}

// PluginHarness is harness metadata (command path only).
type PluginHarness struct {
	Name    string
	Command string
	Args    []string
}

// PluginCatalogHit is one remote catalog search result.
type PluginCatalogHit struct {
	ID           string
	Name         string
	Description  string
	Version      string
	Registry     string
	Capabilities []string
}

// PluginUpdateReview summarizes old → new changes before an update confirm.
// Summary is a multi-line human review with no secrets.
type PluginUpdateReview struct {
	ID                string
	OldVersion        string
	NewVersion        string
	OldDigest         string
	NewDigest         string
	SourceLabel       string
	CapabilityAdded   []string
	CapabilityRemoved []string
	ContribAdded      []string
	ContribRemoved    []string
	ExecutableChanged bool
	ExecutableDiffs   []string
	TrustInvalidated  bool
	HadTrust          bool
	Summary           string
}

// PluginTrustPreview is the capability review shown before granting trust.
// ReviewLines name commands and affected contribution types (no env values).
type PluginTrustPreview struct {
	ID           string
	Scope        string
	Digest       string
	Capabilities []string
	MCP          []PluginMCP
	Harnesses    []PluginHarness
	Hooks        int
	ReviewLines  []string
}

// PluginInstallResult is the outcome of a successful install or update.
type PluginInstallResult struct {
	ID      string
	Version string
	Scope   string
	Digest  string
	Enabled bool
}

// Plugins is the lifecycle surface for the TUI plugin manager.
// Nil means the capability is absent; frontends must degrade gracefully.
// Destructive and trust-changing mutations require confirm=true (or a prior
// explicit preview + confirm path for updates). Implementations must not
// render or return secret/env values.
type Plugins interface {
	// List returns installed plugins (enabled and disabled) with doctor-style
	// detail. Order is stable (global then project, id ascending).
	List() ([]PluginInfo, error)
	// Inspect returns one installed plugin (project preferred when scope empty).
	Inspect(id, scope string) (PluginInfo, error)
	// Enable sets enabled=true in the lockfile for the install scope.
	Enable(id, scope string) error
	// Disable sets enabled=false; source files are preserved.
	Disable(id, scope string) error
	// Remove deletes the plugin directory and lockfile entry. Requires confirm.
	Remove(id, scope string, confirm bool) error
	// TrustPreview builds the executable capability review without granting trust.
	TrustPreview(id, scope string) (PluginTrustPreview, error)
	// Trust records an explicit grant for executable contributions.
	Trust(id, scope string) error
	// Untrust revokes the executable trust grant.
	Untrust(id, scope string) error
	// Search queries a remote catalog. registry is a catalog base or index URL.
	Search(ctx context.Context, registry, query string) ([]PluginCatalogHit, error)
	// Install installs from a local path, git URL, or catalog:pkg[@ver].
	// registry is required for catalog: sources. scope is global|project (default global).
	Install(ctx context.Context, source, scope, registry string) (PluginInstallResult, error)
	// CheckOutdated lists catalog-sourced installs with a newer published version.
	// registry is used when an install lacks one.
	CheckOutdated(ctx context.Context, registry string) ([]PluginInfo, error)
	// PreviewUpdate builds an update review (may download the candidate artifact).
	// Does not mutate the install.
	PreviewUpdate(ctx context.Context, id, scope, registry string) (PluginUpdateReview, error)
	// Update applies a catalog update after review. Requires confirm.
	Update(ctx context.Context, id, scope, registry string, confirm bool) (PluginInstallResult, error)
}
