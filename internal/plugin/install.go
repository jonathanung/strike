package plugin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jonathanung/strike-cli/internal/version"
)

// InstallOptions controls local, git, or catalog plugin installation.
type InstallOptions struct {
	// Scope is global or project (required).
	Scope Scope
	// WorkDir is required for project scope.
	WorkDir string
	// GlobalRoot / ProjectRoot override discovery roots (tests).
	GlobalRoot  string
	ProjectRoot string
	// StrikeVersion overrides version.Version for compatibility checks.
	StrikeVersion string

	// LocalPath is a filesystem path to a plugin root (local install).
	LocalPath string
	// GitURL triggers a git install when set (and LocalPath empty).
	GitURL string
	// GitRef is an optional branch/tag name used only to resolve the pin.
	GitRef string
	// GitCommit is an optional commit SHA to checkout (pinned).
	GitCommit string
	// GitSubdir is an optional subdirectory inside the repo containing plugin.json.
	GitSubdir string

	// CatalogRegistry is the catalog base URL or index URL (catalog install).
	CatalogRegistry string
	// CatalogPackage is the package id/slug in the catalog.
	CatalogPackage string
	// CatalogVersion pins an immutable published version; empty selects latest.
	CatalogVersion string
	// HTTPClient optional for tests (catalog download).
	HTTPClient httpDoer

	// Force replaces an existing install of the same id.
	Force bool
	// PreserveEnabled keeps prior lockfile enablement when Force-replacing (updates).
	PreserveEnabled bool
}

// InstallResult is the outcome of a successful install.
type InstallResult struct {
	ID      string
	Version string
	Digest  string
	Scope   Scope
	Root    string
	Source  SourceIdentity
	Enabled bool
}

// Install validates and atomically installs a plugin from a local path, git, or catalog.
// Failed validation/download/verification leaves no partially enabled plugin and
// preserves any prior working version (staging cleaned; lockfile unchanged on failure).
func Install(ctx context.Context, opts InstallOptions) (InstallResult, error) {
	if opts.Scope == "" {
		opts.Scope = ScopeGlobal
	}
	if opts.Scope != ScopeGlobal && opts.Scope != ScopeProject {
		return InstallResult{}, fmt.Errorf("scope must be global or project")
	}

	roots, err := ResolveRoots(opts.Scope, Options{
		WorkDir:     opts.WorkDir,
		GlobalRoot:  opts.GlobalRoot,
		ProjectRoot: opts.ProjectRoot,
	})
	if err != nil {
		return InstallResult{}, err
	}

	strikeVer := opts.StrikeVersion
	if strikeVer == "" {
		strikeVer = version.Version
	}

	// Materialize payload into a staging directory under the plugins root.
	stagingParent := roots.PluginsDir
	if err := os.MkdirAll(stagingParent, 0o755); err != nil {
		return InstallResult{}, err
	}
	staging, err := os.MkdirTemp(stagingParent, ".staging-install-*")
	if err != nil {
		return InstallResult{}, err
	}
	stagingOK := false
	defer func() {
		if !stagingOK {
			_ = roots.removeAllUnderPlugins(staging)
		}
	}()

	var source SourceIdentity
	var catalogContentDigest string // optional expected tree digest from catalog
	switch {
	case strings.TrimSpace(opts.LocalPath) != "":
		srcPath, err := filepath.Abs(opts.LocalPath)
		if err != nil {
			return InstallResult{}, err
		}
		st, err := os.Stat(srcPath)
		if err != nil {
			return InstallResult{}, fmt.Errorf("local path: %w", err)
		}
		if !st.IsDir() {
			return InstallResult{}, fmt.Errorf("local path must be a directory")
		}
		if err := copyTree(srcPath, staging); err != nil {
			return InstallResult{}, fmt.Errorf("copy local plugin: %w", err)
		}
		source = SourceIdentity{Type: SourceLocal, Path: srcPath}
	case strings.TrimSpace(opts.GitURL) != "":
		commit, err := gitMaterialize(ctx, staging, gitInstallOptions{
			URL:    opts.GitURL,
			Ref:    opts.GitRef,
			Commit: opts.GitCommit,
			Subdir: opts.GitSubdir,
		})
		if err != nil {
			return InstallResult{}, err
		}
		source = SourceIdentity{
			Type:   SourceGit,
			URL:    strings.TrimSpace(opts.GitURL),
			Ref:    strings.TrimSpace(opts.GitRef),
			Commit: commit,
			Subdir: strings.TrimSpace(opts.GitSubdir),
		}
	case strings.TrimSpace(opts.CatalogRegistry) != "" || strings.TrimSpace(opts.CatalogPackage) != "":
		src, contentDig, err := catalogMaterialize(ctx, staging, opts)
		if err != nil {
			return InstallResult{}, err
		}
		source = src
		catalogContentDigest = contentDig
	default:
		return InstallResult{}, fmt.Errorf("install requires a local path, git URL, or catalog package")
	}

	if err := source.Validate(); err != nil {
		return InstallResult{}, err
	}

	// Validate staged bundle fully before enabling.
	m, _, err := ReadManifest(staging)
	if err != nil {
		return InstallResult{}, fmt.Errorf("validate manifest: %w", err)
	}
	// Catalog package id must match manifest id (reproducible identity).
	if source.Type == SourceCatalog && source.Package != "" && m.ID != source.Package {
		return InstallResult{}, fmt.Errorf("catalog package %q does not match manifest id %q", source.Package, m.ID)
	}
	if source.Type == SourceCatalog && source.Version != "" && m.Version != source.Version {
		return InstallResult{}, fmt.Errorf("catalog version %q does not match manifest version %q", source.Version, m.Version)
	}

	p, diags := loadOne(staging, opts.Scope, strikeVer)
	if p == nil {
		return InstallResult{}, fmt.Errorf("validation failed: %s", formatDiagSummary(diags))
	}
	if m.ID != p.ID {
		return InstallResult{}, fmt.Errorf("internal: manifest id mismatch")
	}

	digest, err := ComputeDigest(staging)
	if err != nil {
		return InstallResult{}, fmt.Errorf("compute digest: %w", err)
	}
	if m.Digest != "" && strings.TrimSpace(m.Digest) != digest {
		return InstallResult{}, fmt.Errorf("content digest mismatch: manifest %s computed %s", m.Digest, digest)
	}
	if catalogContentDigest != "" && !digestsEqual(catalogContentDigest, digest) {
		return InstallResult{}, fmt.Errorf("content digest mismatch: catalog %s computed %s", catalogContentDigest, digest)
	}

	dest := roots.InstallDir(p.ID)
	if err := roots.ConfinePath(dest); err != nil {
		// dest may not exist yet — confine parent
		if err2 := roots.ConfinePath(roots.PluginsDir); err2 != nil {
			return InstallResult{}, err
		}
	}

	// Atomic commit under lockfile lock: swap staging into place + write lock entry.
	var result InstallResult
	var backup string // hidden under plugins root; restored if lock write fails after swap
	err = WithLockfileLock(roots.LockPath, func(lf Lockfile) (Lockfile, bool, error) {
		_, onDisk := os.Stat(dest)
		prev, inLock := lf.Plugins[p.ID]
		if !opts.Force && (onDisk == nil || inLock) {
			return lf, true, fmt.Errorf("plugin %q already installed in %s scope (use --force to replace)", p.ID, opts.Scope)
		}

		// Replace destination atomically: rename old aside, rename staging in, remove old.
		// Backup uses a leading-dot name so Discover/List skip it.
		if onDisk == nil {
			backup = filepath.Join(roots.PluginsDir, ".bak-"+sanitizeDirComponent(p.ID)+"-"+fmt.Sprintf("%d", os.Getpid()))
			_ = os.RemoveAll(backup)
			if err := os.Rename(dest, backup); err != nil {
				backup = ""
				return lf, true, fmt.Errorf("replace existing install: %w", err)
			}
		}
		if err := os.Rename(staging, dest); err != nil {
			if backup != "" {
				_ = os.Rename(backup, dest)
				backup = ""
			}
			return lf, true, fmt.Errorf("activate install: %w", err)
		}
		stagingOK = true

		enabled := true
		if opts.PreserveEnabled && inLock {
			enabled = EntryEnabled(prev)
		}
		entry := LockfileEntry{
			Enabled:     boolPtr(enabled),
			Version:     p.Version,
			Digest:      digest,
			Source:      &source,
			InstalledAt: nowRFC3339(),
		}
		// Invalidate prior trust on replace when digest/source/executable change.
		if inLock && prev.Trust != nil {
			if !digestsEqual(prev.Digest, digest) || !sourceIdentityEqual(prev.Source, &source) {
				entry.Trust = nil // cleared
			} else {
				// Same digest+source: preserve trust binding.
				entry.Trust = prev.Trust
			}
		}
		// Fresh install: never copy trust from catalog metadata.
		lf = setLockEntry(lf, p.ID, entry)
		result = InstallResult{
			ID:      p.ID,
			Version: p.Version,
			Digest:  digest,
			Scope:   opts.Scope,
			Root:    dest,
			Source:  source,
			Enabled: enabled,
		}
		return lf, false, nil
	})
	if err != nil {
		// If rename succeeded but lock write failed, roll back: restore previous
		// install when we had a backup; otherwise remove the partial dest.
		if stagingOK {
			_ = roots.removeAllUnderPlugins(dest)
			if backup != "" {
				_ = os.Rename(backup, dest)
				backup = ""
			}
			stagingOK = false
		}
		if backup != "" {
			_ = roots.removeAllUnderPlugins(backup)
		}
		return InstallResult{}, err
	}
	// Success: drop backup of previous install.
	if backup != "" {
		_ = roots.removeAllUnderPlugins(backup)
	}
	return result, nil
}

// catalogMaterialize fetches catalog metadata, downloads the artifact, verifies
// the artifact digest, and extracts into destDir. Returns source identity and
// optional expected content digest from catalog metadata.
func catalogMaterialize(ctx context.Context, destDir string, opts InstallOptions) (SourceIdentity, string, error) {
	reg := strings.TrimSpace(opts.CatalogRegistry)
	pkg := strings.TrimSpace(opts.CatalogPackage)
	if reg == "" {
		return SourceIdentity{}, "", fmt.Errorf("catalog install requires --registry")
	}
	if pkg == "" {
		return SourceIdentity{}, "", fmt.Errorf("catalog install requires a package id")
	}
	cat, _, err := FetchCatalog(ctx, opts.HTTPClient, reg)
	if err != nil {
		return SourceIdentity{}, "", err
	}
	_, ver, err := cat.FindVersion(pkg, opts.CatalogVersion)
	if err != nil {
		return SourceIdentity{}, "", err
	}
	// Bound download by catalog size hint when present (never above global cap).
	maxBytes := int64(maxArtifactBytes)
	if ver.Size > 0 && ver.Size <= maxBytes {
		maxBytes = ver.Size
	}
	data, err := downloadBytes(ctx, opts.HTTPClient, ver.URL, maxBytes)
	if err != nil {
		return SourceIdentity{}, "", fmt.Errorf("download artifact: %w", err)
	}
	got := artifactDigest(data)
	if !digestsEqual(got, ver.Digest) {
		return SourceIdentity{}, "", fmt.Errorf("artifact digest mismatch: got %s want %s", got, ver.Digest)
	}
	// Signature field is reserved; presence must not grant trust or skip digest checks.
	_ = ver.Signature

	if err := extractArchive(data, destDir); err != nil {
		return SourceIdentity{}, "", fmt.Errorf("extract artifact: %w", err)
	}
	src := SourceIdentity{
		Type:     SourceCatalog,
		Registry: cat.Registry,
		Package:  pkg,
		Version:  strings.TrimSpace(ver.Version),
		URL:      strings.TrimSpace(ver.URL),
		Digest:   strings.TrimSpace(ver.Digest),
	}
	return src, strings.TrimSpace(ver.ContentDigest), nil
}

func formatDiagSummary(diags []Diagnostic) string {
	if len(diags) == 0 {
		return "unknown validation error"
	}
	var parts []string
	for _, d := range diags {
		if d.Severity == SeverityError {
			parts = append(parts, d.String())
		}
	}
	if len(parts) == 0 {
		parts = append(parts, diags[0].String())
	}
	if len(parts) > 3 {
		parts = parts[:3]
	}
	return strings.Join(parts, "; ")
}

// ParseInstallSource interprets a single CLI argument as local path, git URL, or catalog:pkg[@ver].
func ParseInstallSource(arg string) (localPath, gitURL, catalogPackage, catalogVersion string, err error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return "", "", "", "", fmt.Errorf("install source is required")
	}
	if strings.HasPrefix(strings.ToLower(arg), "catalog:") {
		rest := strings.TrimSpace(arg[len("catalog:"):])
		if rest == "" {
			return "", "", "", "", fmt.Errorf("catalog: requires package id")
		}
		pkg, ver := splitPackageVersion(rest)
		if err := ValidatePluginID(pkg); err != nil {
			return "", "", "", "", fmt.Errorf("catalog package: %w", err)
		}
		return "", "", pkg, ver, nil
	}
	if isGitURL(arg) {
		return "", arg, "", "", nil
	}
	// git-scp form already handled; plain paths and relative dirs are local.
	// Also accept explicit file://
	if strings.HasPrefix(arg, "file://") {
		return strings.TrimPrefix(arg, "file://"), "", "", "", nil
	}
	return arg, "", "", "", nil
}

func splitPackageVersion(s string) (pkg, version string) {
	s = strings.TrimSpace(s)
	// package ids use dots; version follows last @ when present.
	if i := strings.LastIndex(s, "@"); i > 0 {
		return s[:i], s[i+1:]
	}
	return s, ""
}
