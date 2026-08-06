package plugin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// LockfileName is the enablement/provenance file under a .strike root.
const LockfileName = "plugins.lock.json"

// LockfileSchemaVersion is the lockfile format version written by lifecycle cmds.
const LockfileSchemaVersion = 1

// Lockfile records installed plugins' provenance, digest, and enablement.
// Credentials must never appear here (docs/plugins.md §2, §10).
type Lockfile struct {
	SchemaVersion int                      `json:"schemaVersion"`
	Plugins       map[string]LockfileEntry `json:"plugins,omitempty"`
}

// LockfileEntry holds per-plugin install record. Missing enabled defaults to true.
type LockfileEntry struct {
	Enabled     *bool           `json:"enabled,omitempty"`
	Version     string          `json:"version,omitempty"`
	Digest      string          `json:"digest,omitempty"`
	Source      *SourceIdentity `json:"source,omitempty"`
	InstalledAt string          `json:"installedAt,omitempty"` // RFC3339
	// Trust is the explicit executable grant (docs/plugins.md §5). Absent means
	// passive-only load; MCP/harness/shell hooks stay inactive.
	Trust *TrustRecord `json:"trust,omitempty"`
}

// ReadLockfile loads path; missing or empty file yields an empty lockfile.
func ReadLockfile(path string) (Lockfile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return emptyLockfile(), nil
		}
		return Lockfile{}, err
	}
	stripped, err := stripJSONC(data)
	if err != nil {
		return Lockfile{}, fmt.Errorf("%s: %w", path, err)
	}
	stripped = bytesTrimSpace(stripped)
	if len(stripped) == 0 {
		return emptyLockfile(), nil
	}
	var lf Lockfile
	if err := json.Unmarshal(stripped, &lf); err != nil {
		return Lockfile{}, fmt.Errorf("%s: %w", path, err)
	}
	if lf.Plugins == nil {
		lf.Plugins = map[string]LockfileEntry{}
	}
	if lf.SchemaVersion == 0 {
		lf.SchemaVersion = LockfileSchemaVersion
	}
	return lf, nil
}

func emptyLockfile() Lockfile {
	return Lockfile{SchemaVersion: LockfileSchemaVersion, Plugins: map[string]LockfileEntry{}}
}

// WriteLockfile atomically persists lf to path (no flock; caller should hold lock).
func WriteLockfile(path string, lf Lockfile) error {
	if lf.SchemaVersion == 0 {
		lf.SchemaVersion = LockfileSchemaVersion
	}
	if lf.Plugins == nil {
		lf.Plugins = map[string]LockfileEntry{}
	}
	// Stable key order for deterministic diffs.
	type pair struct {
		id string
		e  LockfileEntry
	}
	var pairs []pair
	for id, e := range lf.Plugins {
		pairs = append(pairs, pair{id, e})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].id < pairs[j].id })
	ordered := make(map[string]LockfileEntry, len(pairs))
	for _, p := range pairs {
		ordered[p.id] = p.e
	}
	lf.Plugins = ordered

	data, err := json.MarshalIndent(lf, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeFileAtomic(path, data, 0o644)
}

// WithLockfileLock runs fn while holding an exclusive lock on the lockfile path.
// fn receives the current lockfile and must return the lockfile to write (or an error).
// If fn returns skipWrite=true, the file is not rewritten.
func WithLockfileLock(path string, fn func(lf Lockfile) (next Lockfile, skipWrite bool, err error)) error {
	unlock, err := lockFile(path)
	if err != nil {
		return err
	}
	defer func() { _ = unlock() }()

	lf, err := ReadLockfile(path)
	if err != nil {
		return err
	}
	next, skip, err := fn(lf)
	if err != nil {
		return err
	}
	if skip {
		return nil
	}
	return WriteLockfile(path, next)
}

// IsEnabled reports whether plugin id should load. Project lockfile entries
// override global for the same id. Absent entries default to enabled.
func IsEnabled(id string, global, project Lockfile) bool {
	if e, ok := project.Plugins[id]; ok && e.Enabled != nil {
		return *e.Enabled
	}
	if e, ok := global.Plugins[id]; ok && e.Enabled != nil {
		return *e.Enabled
	}
	return true
}

// EntryEnabled reports enablement for a single lockfile (absent → true).
func EntryEnabled(e LockfileEntry) bool {
	if e.Enabled == nil {
		return true
	}
	return *e.Enabled
}

func loadLockfiles(globalRoot, projectRoot string) (global, project Lockfile, diags []Diagnostic) {
	if globalRoot != "" {
		lf, err := ReadLockfile(filepath.Join(globalRoot, LockfileName))
		if err != nil {
			diags = append(diags, Diagnostic{
				Severity: SeverityError,
				Code:     "lockfile",
				Message:  err.Error(),
				Source:   ScopeGlobal,
			})
		} else {
			global = lf
		}
	}
	if projectRoot != "" {
		lf, err := ReadLockfile(filepath.Join(projectRoot, LockfileName))
		if err != nil {
			diags = append(diags, Diagnostic{
				Severity: SeverityError,
				Code:     "lockfile",
				Message:  err.Error(),
				Source:   ScopeProject,
			})
		} else {
			project = lf
		}
	}
	return global, project, diags
}

// LockfilePath returns <strikeRoot>/plugins.lock.json.
func LockfilePath(strikeRoot string) string {
	if strikeRoot == "" {
		return ""
	}
	return filepath.Join(strikeRoot, LockfileName)
}

// boolPtr returns a pointer to v.
func boolPtr(v bool) *bool { return &v }

// nowRFC3339 is overridable in tests.
var nowRFC3339 = func() string { return time.Now().UTC().Format(time.RFC3339) }

// setLockEntry merges entry into lf for id.
func setLockEntry(lf Lockfile, id string, e LockfileEntry) Lockfile {
	if lf.Plugins == nil {
		lf.Plugins = map[string]LockfileEntry{}
	}
	lf.Plugins[id] = e
	return lf
}

// deleteLockEntry removes id from lf.
func deleteLockEntry(lf Lockfile, id string) Lockfile {
	if lf.Plugins != nil {
		delete(lf.Plugins, id)
	}
	return lf
}
