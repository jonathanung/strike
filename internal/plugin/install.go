package plugin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jonathanung/strike-cli/internal/version"
)

// InstallOptions controls local or git plugin installation.
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

	// Force replaces an existing install of the same id.
	Force bool
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

// Install validates and atomically installs a plugin from a local path or git source.
// Failed validation leaves no partially enabled plugin (staging cleaned; lockfile unchanged).
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
	default:
		return InstallResult{}, fmt.Errorf("install requires a local path or git URL")
	}

	if err := source.Validate(); err != nil {
		return InstallResult{}, err
	}

	// Validate staged bundle fully before enabling.
	m, _, err := ReadManifest(staging)
	if err != nil {
		return InstallResult{}, fmt.Errorf("validate manifest: %w", err)
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

	dest := roots.InstallDir(p.ID)
	if err := roots.ConfinePath(dest); err != nil {
		// dest may not exist yet — confine parent
		if err2 := roots.ConfinePath(roots.PluginsDir); err2 != nil {
			return InstallResult{}, err
		}
	}

	// Atomic commit under lockfile lock: swap staging into place + write lock entry.
	var result InstallResult
	err = WithLockfileLock(roots.LockPath, func(lf Lockfile) (Lockfile, bool, error) {
		_, onDisk := os.Stat(dest)
		_, inLock := lf.Plugins[p.ID]
		if !opts.Force && (onDisk == nil || inLock) {
			return lf, true, fmt.Errorf("plugin %q already installed in %s scope (use --force to replace)", p.ID, opts.Scope)
		}

		// Replace destination atomically: rename old aside, rename staging in, remove old.
		var backup string
		if onDisk == nil {
			backup = dest + ".bak-" + sanitizeDirComponent(p.ID)
			_ = os.RemoveAll(backup)
			if err := os.Rename(dest, backup); err != nil {
				return lf, true, fmt.Errorf("replace existing install: %w", err)
			}
		}
		if err := os.Rename(staging, dest); err != nil {
			if backup != "" {
				_ = os.Rename(backup, dest)
			}
			return lf, true, fmt.Errorf("activate install: %w", err)
		}
		stagingOK = true
		if backup != "" {
			_ = roots.removeAllUnderPlugins(backup)
		}

		enabled := true
		entry := LockfileEntry{
			Enabled:     boolPtr(enabled),
			Version:     p.Version,
			Digest:      digest,
			Source:      &source,
			InstalledAt: nowRFC3339(),
		}
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
		// If rename succeeded but lock write failed, roll back dest so we leave
		// nothing partially enabled.
		if stagingOK {
			_ = roots.removeAllUnderPlugins(dest)
			stagingOK = false
		}
		return InstallResult{}, err
	}
	return result, nil
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

// ParseInstallSource interprets a single CLI argument as local path or git URL.
func ParseInstallSource(arg string) (localPath, gitURL string, err error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return "", "", fmt.Errorf("install source is required")
	}
	if isGitURL(arg) {
		return "", arg, nil
	}
	// git-scp form already handled; plain paths and relative dirs are local.
	// Also accept explicit file://
	if strings.HasPrefix(arg, "file://") {
		return strings.TrimPrefix(arg, "file://"), "", nil
	}
	return arg, "", nil
}
