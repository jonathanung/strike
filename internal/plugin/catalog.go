package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// CatalogSchemaVersion is the highest remote catalog format this client implements.
const CatalogSchemaVersion = 1

// Catalog is a remote plugin index (docs/plugins.md §6.3, #729).
// Metadata alone never enables or executes installed content.
type Catalog struct {
	SchemaVersion int              `json:"schemaVersion"`
	Registry      string           `json:"registry,omitempty"` // canonical registry identity
	Packages      []CatalogPackage `json:"packages"`
}

// CatalogPackage is one plugin identity with published versions.
type CatalogPackage struct {
	ID          string           `json:"id"`
	Name        string           `json:"name,omitempty"`
	Description string           `json:"description,omitempty"`
	Versions    []CatalogVersion `json:"versions"`
}

// CatalogVersion is one immutable published artifact.
type CatalogVersion struct {
	Version string `json:"version"`
	// URL is the artifact download URL (.tar.gz or .zip).
	URL string `json:"url"`
	// Digest is the SHA-256 of the artifact bytes (sha256:<hex>). Required.
	Digest string `json:"digest"`
	// ContentDigest is the optional expected content-tree digest after extract.
	ContentDigest string `json:"contentDigest,omitempty"`
	// Capabilities are declared tags for update review (may mirror manifest).
	Capabilities []string `json:"capabilities,omitempty"`
	// Strike compatibility hint (install still validates the bundle manifest).
	Strike StrikeRange `json:"strike,omitempty"`
	// ManifestSchema is the plugin.json schemaVersion this artifact targets.
	ManifestSchema int `json:"manifestSchema,omitempty"`
	// Size is optional expected artifact byte length (advisory upper bound).
	Size int64 `json:"size,omitempty"`
	// Signature is reserved for future detached signature verification.
	// v1 clients verify digests only; non-empty values are ignored (not trusted as authz).
	Signature string `json:"signature,omitempty"`
}

// CatalogHit is one search result (latest matching version).
type CatalogHit struct {
	ID          string
	Name        string
	Description string
	Version     CatalogVersion
	Registry    string
}

// ParseCatalog decodes and validates a catalog document.
func ParseCatalog(data []byte) (Catalog, error) {
	data = bytesTrimSpace(data)
	if len(data) == 0 {
		return Catalog{}, fmt.Errorf("empty catalog")
	}
	var c Catalog
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return Catalog{}, fmt.Errorf("parse catalog: %w", err)
	}
	if err := dec.Decode(&struct{}{}); err == nil {
		return Catalog{}, fmt.Errorf("parse catalog: trailing data after JSON value")
	}
	if err := validateCatalog(c); err != nil {
		return Catalog{}, err
	}
	return c, nil
}

func validateCatalog(c Catalog) error {
	if c.SchemaVersion == 0 {
		return fmt.Errorf("catalog schemaVersion is required")
	}
	if c.SchemaVersion > CatalogSchemaVersion {
		return fmt.Errorf("catalog schemaVersion %d is newer than supported %d (upgrade Strike)", c.SchemaVersion, CatalogSchemaVersion)
	}
	if c.SchemaVersion < 1 {
		return fmt.Errorf("catalog schemaVersion must be >= 1")
	}
	if len(c.Packages) == 0 {
		return fmt.Errorf("catalog packages must be non-empty")
	}
	seen := map[string]struct{}{}
	for i, p := range c.Packages {
		if err := ValidatePluginID(p.ID); err != nil {
			return fmt.Errorf("packages[%d].id: %w", i, err)
		}
		id := strings.TrimSpace(p.ID)
		if _, ok := seen[id]; ok {
			return fmt.Errorf("duplicate package id %q", id)
		}
		seen[id] = struct{}{}
		if len(p.Versions) == 0 {
			return fmt.Errorf("package %q: versions must be non-empty", id)
		}
		vseen := map[string]struct{}{}
		for j, v := range p.Versions {
			if !bundleVersionRE.MatchString(strings.TrimSpace(v.Version)) {
				return fmt.Errorf("package %q versions[%d]: invalid semver %q", id, j, v.Version)
			}
			ver := strings.TrimSpace(v.Version)
			if _, ok := vseen[ver]; ok {
				return fmt.Errorf("package %q: duplicate version %q", id, ver)
			}
			vseen[ver] = struct{}{}
			if err := validateHTTPURL(v.URL); err != nil {
				return fmt.Errorf("package %q@%s url: %w", id, ver, err)
			}
			if err := validateDigestString(v.Digest); err != nil {
				return fmt.Errorf("package %q@%s digest: %w", id, ver, err)
			}
			if v.ContentDigest != "" {
				if err := validateDigestString(v.ContentDigest); err != nil {
					return fmt.Errorf("package %q@%s contentDigest: %w", id, ver, err)
				}
			}
			if v.Size < 0 {
				return fmt.Errorf("package %q@%s size must be >= 0", id, ver)
			}
			if v.ManifestSchema < 0 {
				return fmt.Errorf("package %q@%s manifestSchema must be >= 0", id, ver)
			}
		}
	}
	return nil
}

// FetchCatalog downloads and parses a catalog from registryURL.
// registryURL may be a directory base (appends /catalog.json) or a direct .json URL.
func FetchCatalog(ctx context.Context, client httpDoer, registryURL string) (Catalog, string, error) {
	indexURL, err := resolveCatalogIndexURL(registryURL)
	if err != nil {
		return Catalog{}, "", err
	}
	data, err := downloadBytes(ctx, client, indexURL, maxCatalogBytes)
	if err != nil {
		return Catalog{}, "", fmt.Errorf("fetch catalog: %w", err)
	}
	c, err := ParseCatalog(data)
	if err != nil {
		return Catalog{}, "", err
	}
	// Prefer document registry identity; fall back to caller URL.
	reg := strings.TrimSpace(c.Registry)
	if reg == "" {
		reg = strings.TrimRight(strings.TrimSpace(registryURL), "/")
	}
	c.Registry = reg
	return c, indexURL, nil
}

func resolveCatalogIndexURL(registryURL string) (string, error) {
	raw := strings.TrimSpace(registryURL)
	if err := validateHTTPURL(raw); err != nil {
		return "", fmt.Errorf("registry: %w", err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	path := u.Path
	lower := strings.ToLower(path)
	if strings.HasSuffix(lower, ".json") || strings.HasSuffix(lower, ".jsonc") {
		return raw, nil
	}
	// Treat as base directory.
	if !strings.HasSuffix(path, "/") {
		u.Path = path + "/catalog.json"
	} else {
		u.Path = path + "catalog.json"
	}
	return u.String(), nil
}

// FindPackage returns the package by id, or false.
func (c Catalog) FindPackage(id string) (CatalogPackage, bool) {
	id = strings.TrimSpace(id)
	for _, p := range c.Packages {
		if p.ID == id {
			return p, true
		}
	}
	return CatalogPackage{}, false
}

// FindVersion returns a specific version of a package.
func (c Catalog) FindVersion(id, version string) (CatalogPackage, CatalogVersion, error) {
	p, ok := c.FindPackage(id)
	if !ok {
		return CatalogPackage{}, CatalogVersion{}, fmt.Errorf("package %q not found in catalog", id)
	}
	version = strings.TrimSpace(version)
	if version == "" {
		v, err := p.LatestVersion()
		if err != nil {
			return p, CatalogVersion{}, err
		}
		return p, v, nil
	}
	for _, v := range p.Versions {
		if strings.TrimSpace(v.Version) == version {
			return p, v, nil
		}
	}
	return p, CatalogVersion{}, fmt.Errorf("package %q version %q not found in catalog", id, version)
}

// LatestVersion returns the highest semver among published versions.
func (p CatalogPackage) LatestVersion() (CatalogVersion, error) {
	if len(p.Versions) == 0 {
		return CatalogVersion{}, fmt.Errorf("package %q has no versions", p.ID)
	}
	best := p.Versions[0]
	for _, v := range p.Versions[1:] {
		c, err := compareSemver(v.Version, best.Version)
		if err != nil {
			continue
		}
		if c > 0 {
			best = v
		}
	}
	return best, nil
}

// Search filters packages by substring match on id, name, or description.
// Empty query returns all packages (latest version each), sorted by id.
func (c Catalog) Search(query string) []CatalogHit {
	q := strings.ToLower(strings.TrimSpace(query))
	var hits []CatalogHit
	for _, p := range c.Packages {
		if q != "" {
			blob := strings.ToLower(p.ID + " " + p.Name + " " + p.Description)
			if !strings.Contains(blob, q) {
				continue
			}
		}
		v, err := p.LatestVersion()
		if err != nil {
			continue
		}
		hits = append(hits, CatalogHit{
			ID:          p.ID,
			Name:        p.Name,
			Description: p.Description,
			Version:     v,
			Registry:    c.Registry,
		})
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].ID < hits[j].ID })
	return hits
}

// artifactDigest returns sha256:<hex> for raw artifact bytes.
func artifactDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// digestsEqual compares two sha256:<hex> strings (case-insensitive hex).
func digestsEqual(a, b string) bool {
	a = strings.TrimSpace(strings.ToLower(a))
	b = strings.TrimSpace(strings.ToLower(b))
	return a != "" && a == b
}
