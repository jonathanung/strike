package plugin

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// OutdatedOptions controls CheckOutdated.
type OutdatedOptions struct {
	WorkDir       string
	GlobalRoot    string
	ProjectRoot   string
	Scope         Scope // empty: both
	ID            string
	Registry      string // default registry when install source lacks one
	StrikeVersion string
	HTTPClient    httpDoer
}

// OutdatedItem is one installed plugin with a newer catalog version available.
type OutdatedItem struct {
	Installed InstalledPlugin
	Latest    CatalogVersion
	Registry  string
	Review    UpdateReview // pre-download review (new digest may be catalog contentDigest)
}

// CheckOutdated lists catalog-sourced installs that have a newer published version.
// Local/git installs are skipped unless they have catalog provenance.
func CheckOutdated(ctx context.Context, opts OutdatedOptions) ([]OutdatedItem, error) {
	list, _, err := ListInstalled(ListOptions{
		WorkDir:       opts.WorkDir,
		GlobalRoot:    opts.GlobalRoot,
		ProjectRoot:   opts.ProjectRoot,
		Scope:         opts.Scope,
		StrikeVersion: opts.StrikeVersion,
	})
	if err != nil {
		return nil, err
	}
	// Cache catalogs by registry URL.
	cats := map[string]Catalog{}
	var out []OutdatedItem
	wantID := strings.TrimSpace(opts.ID)
	for _, ip := range list {
		if wantID != "" && ip.ID != wantID {
			continue
		}
		if ip.Source == nil || ip.Source.Type != SourceCatalog {
			continue
		}
		reg := strings.TrimSpace(ip.Source.Registry)
		if reg == "" {
			reg = strings.TrimSpace(opts.Registry)
		}
		if reg == "" {
			continue
		}
		cat, ok := cats[reg]
		if !ok {
			c, _, err := FetchCatalog(ctx, opts.HTTPClient, reg)
			if err != nil {
				return nil, fmt.Errorf("registry %s: %w", reg, err)
			}
			cats[reg] = c
			cat = c
		}
		pkgName := ip.Source.Package
		if pkgName == "" {
			pkgName = ip.ID
		}
		_, latest, err := cat.FindVersion(pkgName, "")
		if err != nil {
			continue
		}
		cur := ip.Version
		if ip.Source.Version != "" {
			cur = ip.Source.Version
		}
		cmp, err := compareSemver(latest.Version, cur)
		if err != nil || cmp <= 0 {
			continue
		}
		// Build a lightweight review from catalog metadata + installed manifest.
		newMan := Manifest{
			ID:           ip.ID,
			Version:      latest.Version,
			Capabilities: latest.Capabilities,
		}
		if ip.Manifest != nil {
			// Keep old contribs as baseline; catalog may not list full contrib matrix.
			newMan.Contributions = ip.Manifest.Contributions
			if len(latest.Capabilities) == 0 {
				newMan.Capabilities = ip.Manifest.Capabilities
			}
		}
		newSrc := SourceIdentity{
			Type:     SourceCatalog,
			Registry: cat.Registry,
			Package:  pkgName,
			Version:  latest.Version,
			URL:      latest.URL,
			Digest:   latest.Digest,
		}
		contentDig := latest.ContentDigest
		review := BuildUpdateReview(ip, newMan, newSrc, contentDig, "")
		// Catalog-only review cannot see executable diffs until download; mark version bump.
		if review.OldVersion != review.NewVersion {
			// Trust invalidation on version change when trust present is already handled.
		}
		out = append(out, OutdatedItem{
			Installed: ip,
			Latest:    latest,
			Registry:  cat.Registry,
			Review:    review,
		})
	}
	return out, nil
}

// UpdateOptions controls Update (catalog or re-install from catalog pin).
type UpdateOptions struct {
	ID            string
	Scope         Scope
	WorkDir       string
	GlobalRoot    string
	ProjectRoot   string
	Registry      string // override registry
	Version       string // empty: latest
	StrikeVersion string
	HTTPClient    httpDoer
	// Confirm must be true after the caller showed UpdateReview (CLI --yes).
	Confirm bool
	// Force allows update when not outdated (re-pin same or different version).
	Force bool
}

// UpdateResult is the outcome of a successful update.
type UpdateResult struct {
	Install InstallResult
	Review  UpdateReview
}

// Update installs a newer (or forced) catalog version with rollback-safe replace.
// Failed download/verification/validation preserves the prior working version.
// Requires Confirm; never auto-applies (no unattended updates).
func Update(ctx context.Context, opts UpdateOptions) (UpdateResult, error) {
	if !opts.Confirm {
		return UpdateResult{}, fmt.Errorf("update requires confirmation (--yes) after review")
	}
	id := strings.TrimSpace(opts.ID)
	if err := ValidatePluginKey(id); err != nil {
		return UpdateResult{}, err
	}
	ip, err := Inspect(EnableOptions{
		ID:          id,
		Scope:       opts.Scope,
		WorkDir:     opts.WorkDir,
		GlobalRoot:  opts.GlobalRoot,
		ProjectRoot: opts.ProjectRoot,
	})
	if err != nil {
		return UpdateResult{}, err
	}
	reg := strings.TrimSpace(opts.Registry)
	pkg := id
	if ip.Source != nil && ip.Source.Type == SourceCatalog {
		if reg == "" {
			reg = ip.Source.Registry
		}
		if ip.Source.Package != "" {
			pkg = ip.Source.Package
		}
	}
	if reg == "" {
		return UpdateResult{}, fmt.Errorf("update requires a catalog registry (install from catalog or pass --registry)")
	}

	// Pre-download review from catalog metadata for the CLI to have shown.
	cat, _, err := FetchCatalog(ctx, opts.HTTPClient, reg)
	if err != nil {
		return UpdateResult{}, err
	}
	_, ver, err := cat.FindVersion(pkg, opts.Version)
	if err != nil {
		return UpdateResult{}, err
	}
	if !opts.Force {
		cur := ip.Version
		if ip.Source != nil && ip.Source.Version != "" {
			cur = ip.Source.Version
		}
		cmp, err := compareSemver(ver.Version, cur)
		if err != nil {
			return UpdateResult{}, err
		}
		if cmp <= 0 {
			return UpdateResult{}, fmt.Errorf("plugin %q is already at %s (latest catalog %s); use --force to reinstall", id, cur, ver.Version)
		}
	}

	// Install with Force replaces atomically; prior version restored on failure.
	res, err := Install(ctx, InstallOptions{
		Scope:           ip.Scope,
		WorkDir:         opts.WorkDir,
		GlobalRoot:      opts.GlobalRoot,
		ProjectRoot:     opts.ProjectRoot,
		StrikeVersion:   opts.StrikeVersion,
		CatalogRegistry: cat.Registry,
		CatalogPackage:  pkg,
		CatalogVersion:  ver.Version,
		HTTPClient:      opts.HTTPClient,
		Force:           true,
		// Preserve enablement across update.
		PreserveEnabled: true,
	})
	if err != nil {
		return UpdateResult{}, err
	}

	// Full review with post-install digest and new manifest.
	newIP := InstalledPlugin{
		ID:       res.ID,
		Version:  res.Version,
		Digest:   res.Digest,
		Source:   &res.Source,
		Manifest: nil,
		Trust:    ip.Trust, // prior trust for invalidation comparison
	}
	if m, _, err := ReadManifest(res.Root); err == nil {
		newIP.Manifest = &m
	}
	// Build review against previous install state.
	var newMan Manifest
	if newIP.Manifest != nil {
		newMan = *newIP.Manifest
	} else {
		newMan = Manifest{ID: res.ID, Version: res.Version}
	}
	review := BuildUpdateReview(ip, newMan, res.Source, res.Digest, res.Root)

	// Clear trust on lockfile when invalidated (executable/digest/source change).
	if review.TrustInvalidated || review.ExecutableChanged || !digestsEqual(ip.Digest, res.Digest) {
		_ = clearTrust(res.ID, ip.Scope, opts)
		review.TrustInvalidated = true
		if ip.Trust != nil && ip.Trust.Digest != "" {
			review.HadTrust = true
		}
	}
	return UpdateResult{Install: res, Review: review}, nil
}

func clearTrust(id string, scope Scope, opts UpdateOptions) error {
	roots, _, err := resolveManageScope(EnableOptions{
		ID:          id,
		Scope:       scope,
		WorkDir:     opts.WorkDir,
		GlobalRoot:  opts.GlobalRoot,
		ProjectRoot: opts.ProjectRoot,
	})
	if err != nil {
		return err
	}
	return WithLockfileLock(roots.LockPath, func(lf Lockfile) (Lockfile, bool, error) {
		e, ok := lf.Plugins[id]
		if !ok {
			return lf, true, nil
		}
		if e.Trust == nil {
			return lf, true, nil
		}
		e.Trust = nil
		lf = setLockEntry(lf, id, e)
		return lf, false, nil
	})
}

// PreviewUpdate builds a full UpdateReview without installing. It downloads and
// extracts the candidate artifact into a temp directory so contribution and
// executable diffs are accurate before the user confirms (--yes).
func PreviewUpdate(ctx context.Context, opts UpdateOptions) (UpdateReview, CatalogVersion, error) {
	ip, err := Inspect(EnableOptions{
		ID:          opts.ID,
		Scope:       opts.Scope,
		WorkDir:     opts.WorkDir,
		GlobalRoot:  opts.GlobalRoot,
		ProjectRoot: opts.ProjectRoot,
	})
	if err != nil {
		return UpdateReview{}, CatalogVersion{}, err
	}
	reg := strings.TrimSpace(opts.Registry)
	pkg := strings.TrimSpace(opts.ID)
	if ip.Source != nil && ip.Source.Type == SourceCatalog {
		if reg == "" {
			reg = ip.Source.Registry
		}
		if ip.Source.Package != "" {
			pkg = ip.Source.Package
		}
	}
	if reg == "" {
		return UpdateReview{}, CatalogVersion{}, fmt.Errorf("update requires a catalog registry")
	}
	cat, _, err := FetchCatalog(ctx, opts.HTTPClient, reg)
	if err != nil {
		return UpdateReview{}, CatalogVersion{}, err
	}
	_, ver, err := cat.FindVersion(pkg, opts.Version)
	if err != nil {
		return UpdateReview{}, CatalogVersion{}, err
	}
	src := SourceIdentity{
		Type:     SourceCatalog,
		Registry: cat.Registry,
		Package:  pkg,
		Version:  ver.Version,
		URL:      ver.URL,
		Digest:   ver.Digest,
	}

	// Download + extract to temp for accurate contribution/executable review.
	// Does not touch the install root or lockfile.
	tmp, err := os.MkdirTemp("", "strike-plugin-preview-*")
	if err != nil {
		return UpdateReview{}, CatalogVersion{}, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	maxBytes := int64(maxArtifactBytes)
	if ver.Size > 0 && ver.Size <= maxBytes {
		maxBytes = ver.Size
	}
	data, err := downloadBytes(ctx, opts.HTTPClient, ver.URL, maxBytes)
	if err != nil {
		return UpdateReview{}, CatalogVersion{}, fmt.Errorf("download artifact for review: %w", err)
	}
	got := artifactDigest(data)
	if !digestsEqual(got, ver.Digest) {
		return UpdateReview{}, CatalogVersion{}, fmt.Errorf("artifact digest mismatch: got %s want %s", got, ver.Digest)
	}
	if err := extractArchive(data, tmp); err != nil {
		return UpdateReview{}, CatalogVersion{}, fmt.Errorf("extract artifact for review: %w", err)
	}
	newMan, _, err := ReadManifest(tmp)
	if err != nil {
		return UpdateReview{}, CatalogVersion{}, fmt.Errorf("preview manifest: %w", err)
	}
	if newMan.ID != ip.ID && newMan.ID != pkg {
		return UpdateReview{}, CatalogVersion{}, fmt.Errorf("catalog package %q does not match manifest id %q", pkg, newMan.ID)
	}
	contentDig, err := ComputeDigest(tmp)
	if err != nil {
		return UpdateReview{}, CatalogVersion{}, err
	}
	if ver.ContentDigest != "" && !digestsEqual(ver.ContentDigest, contentDig) {
		return UpdateReview{}, CatalogVersion{}, fmt.Errorf("content digest mismatch: catalog %s computed %s", ver.ContentDigest, contentDig)
	}

	review := BuildUpdateReview(ip, newMan, src, contentDig, tmp)
	// Version bump with prior trust always invalidates (fail closed).
	if review.HadTrust && review.OldVersion != review.NewVersion {
		review.TrustInvalidated = true
	}
	return review, ver, nil
}
