package plugin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// SchemaVersion is the highest manifest schema this loader implements.
const SchemaVersion = 1

// Manifest is the root plugin.json / plugin.jsonc document (schemaVersion 1).
type Manifest struct {
	SchemaVersion int             `json:"schemaVersion"`
	ID            string          `json:"id"`
	Version       string          `json:"version"`
	Name          string          `json:"name"`
	Description   string          `json:"description,omitempty"`
	Strike        StrikeRange     `json:"strike"`
	Source        json.RawMessage `json:"source,omitempty"` // install lockfile overrides; ignored at load
	Contributions Contributions   `json:"contributions"`
	Capabilities  []string        `json:"capabilities,omitempty"`
	Digest        string          `json:"digest,omitempty"`
	Schema        string          `json:"$schema,omitempty"` // editor only; ignored
}

// StrikeRange is the Strike binary compatibility window.
type StrikeRange struct {
	Min string `json:"min"`
	Max string `json:"max,omitempty"` // exclusive upper bound when set
}

// Contributions lists declared contribution entries. Executable kinds may be
// present in the manifest but are not activated by the passive loader.
type Contributions struct {
	Agents    []PathEntry       `json:"agents,omitempty"`
	Skills    []PathEntry       `json:"skills,omitempty"`
	Workflows []PathEntry       `json:"workflows,omitempty"`
	Themes    []PathEntry       `json:"themes,omitempty"`
	Providers []ProviderEntry   `json:"providers,omitempty"`
	MCP       []json.RawMessage `json:"mcp,omitempty"`
	Harnesses []json.RawMessage `json:"harnesses,omitempty"`
	Hooks     []json.RawMessage `json:"hooks,omitempty"`
	Panes     []json.RawMessage `json:"panes,omitempty"`
}

// PathEntry is a passive contribution that points at a file under the plugin root.
type PathEntry struct {
	Path string `json:"path"`
}

// ProviderEntry is a provider profile contribution (configuration only).
type ProviderEntry struct {
	Path        string `json:"path"`
	ProfileName string `json:"profileName,omitempty"`
}

// pluginIDRE matches reverse-DNS style ids (acme.review-pack).
var pluginIDRE = regexp.MustCompile(`^[a-z][a-z0-9-]*(\.[a-z][a-z0-9-]*)+$`)

// pluginIDSingleRE matches a single-segment slug.
var pluginIDSingleRE = regexp.MustCompile(`^[a-z][a-z0-9-]{1,63}$`)

// bundleVersionRE is a practical semver 2.0 check (core + optional pre/build).
var bundleVersionRE = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-((?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*)(?:\.(?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*))*))?(?:\+([0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*))?$`)

// ParseManifest decodes plugin.json / plugin.jsonc bytes with strict unknown-field rejection.
func ParseManifest(data []byte) (Manifest, error) {
	stripped, err := stripJSONC(data)
	if err != nil {
		return Manifest{}, err
	}
	stripped = bytesTrimSpace(stripped)
	if len(stripped) == 0 {
		return Manifest{}, fmt.Errorf("empty manifest")
	}
	var m Manifest
	dec := json.NewDecoder(strings.NewReader(string(stripped)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("parse manifest: %w", err)
	}
	// Reject trailing junk.
	if err := dec.Decode(&struct{}{}); err == nil {
		return Manifest{}, fmt.Errorf("parse manifest: trailing data after JSON value")
	}
	if err := validateManifest(m); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

// ReadManifest reads and parses plugin.json or plugin.jsonc from a plugin root.
func ReadManifest(root string) (Manifest, string, error) {
	for _, name := range []string{"plugin.json", "plugin.jsonc"} {
		path := filepath.Join(root, name)
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return Manifest{}, "", err
		}
		m, err := ParseManifest(data)
		if err != nil {
			return Manifest{}, path, fmt.Errorf("%s: %w", path, err)
		}
		return m, path, nil
	}
	return Manifest{}, "", fmt.Errorf("no plugin.json or plugin.jsonc in %s", root)
}

func validateManifest(m Manifest) error {
	if m.SchemaVersion == 0 {
		return fmt.Errorf("schemaVersion is required")
	}
	if m.SchemaVersion < 1 {
		return fmt.Errorf("schemaVersion must be >= 1, got %d", m.SchemaVersion)
	}
	// Higher versions are handled by the caller (skip plugin); still require id shape when present.
	if err := ValidatePluginID(m.ID); err != nil {
		return err
	}
	if strings.TrimSpace(m.Version) == "" {
		return fmt.Errorf("version is required")
	}
	if !bundleVersionRE.MatchString(strings.TrimSpace(m.Version)) {
		return fmt.Errorf("version %q is not valid semver", m.Version)
	}
	name := strings.TrimSpace(m.Name)
	if name == "" {
		return fmt.Errorf("name is required")
	}
	if len(name) > 80 {
		return fmt.Errorf("name exceeds 80 characters")
	}
	if m.Description != "" && len(m.Description) > 500 {
		return fmt.Errorf("description exceeds 500 characters")
	}
	if strings.TrimSpace(m.Strike.Min) == "" {
		return fmt.Errorf("strike.min is required")
	}
	if !bundleVersionRE.MatchString(strings.TrimSpace(m.Strike.Min)) {
		return fmt.Errorf("strike.min %q is not valid semver", m.Strike.Min)
	}
	if max := strings.TrimSpace(m.Strike.Max); max != "" && !bundleVersionRE.MatchString(max) {
		return fmt.Errorf("strike.max %q is not valid semver", m.Strike.Max)
	}
	if m.Digest != "" {
		if err := validateDigestString(m.Digest); err != nil {
			return err
		}
	}
	if err := validateContributions(m.Contributions); err != nil {
		return err
	}
	return nil
}

func validateContributions(c Contributions) error {
	if !hasAnyContribution(c) {
		return fmt.Errorf("contributions must include at least one entry")
	}
	seen := map[string]struct{}{}
	checkPath := func(kind, path string) error {
		path = strings.TrimSpace(path)
		if path == "" {
			return fmt.Errorf("contributions.%s: path is required", kind)
		}
		key := kind + "\x00" + path
		if _, ok := seen[key]; ok {
			return fmt.Errorf("contributions.%s: duplicate path %q", kind, path)
		}
		seen[key] = struct{}{}
		if err := validateRelPathSyntax(path); err != nil {
			return fmt.Errorf("contributions.%s path %q: %w", kind, path, err)
		}
		return nil
	}
	for _, e := range c.Agents {
		if err := checkPath("agents", e.Path); err != nil {
			return err
		}
	}
	for _, e := range c.Skills {
		if err := checkPath("skills", e.Path); err != nil {
			return err
		}
	}
	for _, e := range c.Workflows {
		if err := checkPath("workflows", e.Path); err != nil {
			return err
		}
	}
	for _, e := range c.Themes {
		if err := checkPath("themes", e.Path); err != nil {
			return err
		}
	}
	for _, e := range c.Providers {
		if err := checkPath("providers", e.Path); err != nil {
			return err
		}
		if pn := strings.TrimSpace(e.ProfileName); pn != "" {
			if err := validateProfileName(pn); err != nil {
				return fmt.Errorf("contributions.providers profileName: %w", err)
			}
		}
	}
	return nil
}

func hasAnyContribution(c Contributions) bool {
	return len(c.Agents) > 0 || len(c.Skills) > 0 || len(c.Workflows) > 0 ||
		len(c.Themes) > 0 || len(c.Providers) > 0 || len(c.MCP) > 0 ||
		len(c.Harnesses) > 0 || len(c.Hooks) > 0 || len(c.Panes) > 0
}

// ValidatePluginID checks the plugin id grammar from the bundle contract.
func ValidatePluginID(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("id is required")
	}
	if len(id) > 128 {
		return fmt.Errorf("id exceeds 128 characters")
	}
	if pluginIDRE.MatchString(id) || pluginIDSingleRE.MatchString(id) {
		return nil
	}
	return fmt.Errorf("id %q is not a valid plugin id", id)
}

func validateProfileName(name string) error {
	// Same slug rules as custom provider names (lowercase letter start).
	if !regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`).MatchString(name) {
		return fmt.Errorf("%q must be a lowercase slug (letter, then letters/digits/_/-)", name)
	}
	return nil
}

func validateRelPathSyntax(path string) error {
	if path == "" {
		return fmt.Errorf("empty path")
	}
	if strings.HasPrefix(path, "/") || strings.HasPrefix(path, `\`) {
		return fmt.Errorf("absolute paths are not allowed")
	}
	// Windows drive
	if len(path) >= 2 && path[1] == ':' {
		return fmt.Errorf("absolute paths are not allowed")
	}
	clean := strings.ReplaceAll(path, `\`, "/")
	for _, seg := range strings.Split(clean, "/") {
		if seg == ".." {
			return fmt.Errorf("path must not contain '..'")
		}
		if seg == "" {
			return fmt.Errorf("path must not contain empty segments")
		}
	}
	return nil
}

func validateDigestString(d string) error {
	d = strings.TrimSpace(d)
	const prefix = "sha256:"
	if !strings.HasPrefix(d, prefix) {
		return fmt.Errorf("digest must start with sha256:")
	}
	hex := d[len(prefix):]
	if len(hex) != 64 {
		return fmt.Errorf("digest must be sha256:<64 hex chars>")
	}
	for _, r := range hex {
		if r >= '0' && r <= '9' || r >= 'a' && r <= 'f' {
			continue
		}
		return fmt.Errorf("digest hex must be lowercase")
	}
	return nil
}

func bytesTrimSpace(b []byte) []byte {
	return []byte(strings.TrimSpace(string(b)))
}
