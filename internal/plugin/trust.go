package plugin

import (
	"fmt"
	"sort"
	"strings"
)

// TrustRecord is a durable grant binding plugin id (lockfile key) to source
// identity, content digest, and capability set (docs/plugins.md §5.3).
// Credentials must never appear here.
type TrustRecord struct {
	Digest       string          `json:"digest"`
	Source       *SourceIdentity `json:"source,omitempty"`
	Capabilities []string        `json:"capabilities,omitempty"`
	TrustedAt    string          `json:"trustedAt,omitempty"` // RFC3339
}

// Capability tags inferred from executable contributions / declared list.
const (
	CapMCPStdio         = "mcp.stdio"
	CapMCPHTTP          = "mcp.http"
	CapHarnesses        = "harnesses"
	CapHooksCommand     = "hooks.command"
	CapHooksDeclarative = "hooks.declarative"
)

// InferCapabilities returns the sorted capability set for trust binding.
// Declared manifest.capabilities are included when non-empty; executable kinds
// present in contributions are always inferred so grants stay fail-closed.
func InferCapabilities(m Manifest) []string {
	set := map[string]struct{}{}
	for _, c := range m.Capabilities {
		c = strings.TrimSpace(c)
		if c != "" {
			set[c] = struct{}{}
		}
	}
	for _, raw := range m.Contributions.MCP {
		e, err := parseMCPEntry(raw)
		if err != nil {
			continue
		}
		switch normalizeMCPTransport(e.Transport, e.URL) {
		case "http":
			set[CapMCPHTTP] = struct{}{}
		default:
			set[CapMCPStdio] = struct{}{}
		}
	}
	if len(m.Contributions.Harnesses) > 0 {
		set[CapHarnesses] = struct{}{}
	}
	for _, raw := range m.Contributions.Hooks {
		h, err := parseHookEntry(raw)
		if err != nil {
			continue
		}
		if h.IsShell() {
			set[CapHooksCommand] = struct{}{}
		} else if h.IsRule() {
			set[CapHooksDeclarative] = struct{}{}
		}
	}
	if len(m.Contributions.Panes) > 0 {
		set[CapPanes] = struct{}{}
	}
	return sortedKeys(set)
}

// InferCapabilitiesAt is InferCapabilities plus process-pane tags that require
// reading definition files under pluginRoot.
func InferCapabilitiesAt(m Manifest, pluginRoot string) []string {
	set := map[string]struct{}{}
	for _, c := range InferCapabilities(m) {
		set[c] = struct{}{}
	}
	if pluginRoot != "" && HasProcessPanes(m, pluginRoot) {
		set[CapPanes] = struct{}{}
		set[CapPanesProcess] = struct{}{}
	}
	if m.Format == FormatAPS && pluginRoot != "" {
		inferAPSMCPCaps(pluginRoot, set)
	}
	return sortedKeys(set)
}

// HasExecutableContributions reports MCP, harness, or shell-hook entries.
// Process panes need the plugin root — use HasExecutableContributionsAt.
func HasExecutableContributions(m Manifest) bool {
	if len(m.Contributions.MCP) > 0 || len(m.Contributions.Harnesses) > 0 {
		return true
	}
	for _, raw := range m.Contributions.Hooks {
		h, err := parseHookEntry(raw)
		if err != nil {
			continue
		}
		if h.IsShell() {
			return true
		}
	}
	return false
}

// HasExecutableContributionsAt includes process panes under pluginRoot.
func HasExecutableContributionsAt(m Manifest, pluginRoot string) bool {
	if HasExecutableContributions(m) {
		return true
	}
	if pluginRoot != "" && HasProcessPanes(m, pluginRoot) {
		return true
	}
	if m.Format == FormatAPS && pluginRoot != "" {
		return apsHasExecutableMCP(pluginRoot)
	}
	return false
}

// TrustMatch explains whether a trust record still authorizes execution.
type TrustMatch struct {
	OK     bool
	Reason string // empty when OK; otherwise why trust is missing/stale
	State  string // none | trusted | stale | n/a-passive-only
}

// MatchTrust validates a lockfile trust record against the live install.
// id is the plugin id; digest/source/caps are current values.
func MatchTrust(trust *TrustRecord, digest string, source *SourceIdentity, caps []string) TrustMatch {
	if trust == nil {
		return TrustMatch{OK: false, Reason: "no trust record", State: "none"}
	}
	if strings.TrimSpace(trust.Digest) == "" {
		return TrustMatch{OK: false, Reason: "trust record missing digest", State: "stale"}
	}
	if trust.Digest != strings.TrimSpace(digest) {
		return TrustMatch{OK: false, Reason: "content digest changed; re-trust required", State: "stale"}
	}
	if !sourceIdentityEqual(trust.Source, source) {
		return TrustMatch{OK: false, Reason: "source identity changed; re-trust required", State: "stale"}
	}
	// Current capability set must be a subset of the granted set (fail closed on growth).
	granted := map[string]struct{}{}
	for _, c := range trust.Capabilities {
		c = strings.TrimSpace(c)
		if c != "" {
			granted[c] = struct{}{}
		}
	}
	for _, c := range caps {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if _, ok := granted[c]; !ok {
			return TrustMatch{
				OK:     false,
				Reason: fmt.Sprintf("capability %q not in trust grant; re-trust required", c),
				State:  "stale",
			}
		}
	}
	return TrustMatch{OK: true, State: "trusted"}
}

// TrustOptions controls Trust / Untrust.
type TrustOptions struct {
	ID          string
	Scope       Scope // empty: prefer project install when both
	WorkDir     string
	GlobalRoot  string
	ProjectRoot string
	// StrikeVersion for load validation.
	StrikeVersion string
}

// TrustResult is the outcome of a successful Trust grant.
type TrustResult struct {
	ID           string
	Scope        Scope
	Digest       string
	Capabilities []string
	Source       *SourceIdentity
}

// Trust records an explicit user grant for executable contributions of an
// installed plugin. The grant binds current digest, source identity, and
// inferred capability set. Passive contributions do not require this grant.
func Trust(opts TrustOptions) (TrustResult, error) {
	id := strings.TrimSpace(opts.ID)
	if err := ValidatePluginKey(id); err != nil {
		return TrustResult{}, err
	}
	ip, roots, err := resolveInstalledForTrust(opts)
	if err != nil {
		return TrustResult{}, err
	}
	if ip.LoadError != "" || ip.Manifest == nil {
		msg := ip.LoadError
		if msg == "" {
			msg = "manifest unavailable"
		}
		return TrustResult{}, fmt.Errorf("plugin %q: %s", id, msg)
	}
	if !HasExecutableContributionsAt(*ip.Manifest, ip.Root) {
		return TrustResult{}, fmt.Errorf("plugin %q has no executable contributions to trust", id)
	}

	// Always bind trust to the live tree so grants cannot target a stale lock digest.
	digest, err := ComputeDigest(ip.Root)
	if err != nil {
		return TrustResult{}, fmt.Errorf("compute digest: %w", err)
	}
	// Prefer live lockfile source; fall back to install identity on disk entry.
	var source *SourceIdentity
	if ip.Source != nil {
		cp := *ip.Source
		source = &cp
	}
	caps := InferCapabilitiesAt(*ip.Manifest, ip.Root)

	var result TrustResult
	err = WithLockfileLock(roots.LockPath, func(lf Lockfile) (Lockfile, bool, error) {
		e := lf.Plugins[id]
		// Preserve enablement / install provenance; refresh digest to current.
		if e.Version == "" {
			e.Version = ip.Version
		}
		e.Digest = digest
		if source != nil {
			e.Source = source
		} else if e.Source == nil {
			// Local path identity from install root when lock lacked source.
			abs := ip.Root
			e.Source = &SourceIdentity{Type: SourceLocal, Path: abs}
			source = e.Source
		}
		if err := e.Source.Validate(); err != nil {
			return lf, true, fmt.Errorf("source identity: %w", err)
		}
		tr := &TrustRecord{
			Digest:       digest,
			Source:       cloneSource(e.Source),
			Capabilities: append([]string(nil), caps...),
			TrustedAt:    nowRFC3339(),
		}
		e.Trust = tr
		lf = setLockEntry(lf, id, e)
		result = TrustResult{
			ID:           id,
			Scope:        roots.Scope,
			Digest:       digest,
			Capabilities: append([]string(nil), caps...),
			Source:       cloneSource(e.Source),
		}
		return lf, false, nil
	})
	if err != nil {
		return TrustResult{}, err
	}
	return result, nil
}

// Untrust removes the executable trust grant. Passive load is unaffected;
// executables stay inactive until Trust is granted again.
func Untrust(opts TrustOptions) error {
	id := strings.TrimSpace(opts.ID)
	if err := ValidatePluginKey(id); err != nil {
		return err
	}
	_, roots, err := resolveInstalledForTrust(opts)
	if err != nil {
		return err
	}
	return WithLockfileLock(roots.LockPath, func(lf Lockfile) (Lockfile, bool, error) {
		e, ok := lf.Plugins[id]
		if !ok {
			// Still allow clearing when only on-disk without lock entry: write empty trust.
			e = LockfileEntry{}
		}
		if e.Trust == nil {
			return lf, true, nil
		}
		e.Trust = nil
		lf = setLockEntry(lf, id, e)
		return lf, false, nil
	})
}

func resolveInstalledForTrust(opts TrustOptions) (InstalledPlugin, Roots, error) {
	ip, err := Inspect(EnableOptions{
		ID:          opts.ID,
		Scope:       opts.Scope,
		WorkDir:     opts.WorkDir,
		GlobalRoot:  opts.GlobalRoot,
		ProjectRoot: opts.ProjectRoot,
	})
	if err != nil {
		return InstalledPlugin{}, Roots{}, err
	}
	roots, _, err := resolveManageScope(EnableOptions{
		ID:          opts.ID,
		Scope:       ip.Scope,
		WorkDir:     opts.WorkDir,
		GlobalRoot:  opts.GlobalRoot,
		ProjectRoot: opts.ProjectRoot,
	})
	if err != nil {
		return InstalledPlugin{}, Roots{}, err
	}
	return ip, roots, nil
}

func sourceIdentityEqual(a, b *SourceIdentity) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	if a.Type != b.Type {
		return false
	}
	switch a.Type {
	case SourceLocal:
		return filepathCleanEqual(a.Path, b.Path)
	case SourceGit:
		return strings.TrimSpace(a.URL) == strings.TrimSpace(b.URL) &&
			strings.EqualFold(strings.TrimSpace(a.Commit), strings.TrimSpace(b.Commit)) &&
			strings.TrimSpace(a.Subdir) == strings.TrimSpace(b.Subdir)
		// Ref is resolve-time only; commit is the pin.
	case SourceCatalog:
		return strings.TrimSpace(a.Registry) == strings.TrimSpace(b.Registry) &&
			strings.TrimSpace(a.Package) == strings.TrimSpace(b.Package) &&
			strings.TrimSpace(a.Version) == strings.TrimSpace(b.Version) &&
			strings.TrimSpace(a.URL) == strings.TrimSpace(b.URL) &&
			digestsEqual(a.Digest, b.Digest)
	default:
		return false
	}
}

func filepathCleanEqual(a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == b {
		return true
	}
	// Best-effort clean compare without requiring the path to exist.
	return strings.TrimRight(strings.ReplaceAll(a, "\\", "/"), "/") ==
		strings.TrimRight(strings.ReplaceAll(b, "\\", "/"), "/")
}

func cloneSource(s *SourceIdentity) *SourceIdentity {
	if s == nil {
		return nil
	}
	cp := *s
	return &cp
}

func sortedKeys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
