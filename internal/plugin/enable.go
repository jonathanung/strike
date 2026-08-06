package plugin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// LockfileName is the enablement/provenance file under a .strike root.
// Full install lock schema is owned by #727; this loader only reads enablement.
const LockfileName = "plugins.lock.json"

// Lockfile is the minimal on-disk enablement map.
type Lockfile struct {
	SchemaVersion int                      `json:"schemaVersion"`
	Plugins       map[string]LockfileEntry `json:"plugins,omitempty"`
}

// LockfileEntry holds per-plugin flags. Missing enabled defaults to true.
type LockfileEntry struct {
	Enabled *bool  `json:"enabled,omitempty"`
	Source  string `json:"source,omitempty"` // reserved for #727
	Digest  string `json:"digest,omitempty"` // reserved for #727
}

// ReadLockfile loads path; missing file yields an empty lockfile.
func ReadLockfile(path string) (Lockfile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Lockfile{SchemaVersion: 1, Plugins: map[string]LockfileEntry{}}, nil
		}
		return Lockfile{}, err
	}
	stripped, err := stripJSONC(data)
	if err != nil {
		return Lockfile{}, fmt.Errorf("%s: %w", path, err)
	}
	var lf Lockfile
	if err := json.Unmarshal(stripped, &lf); err != nil {
		return Lockfile{}, fmt.Errorf("%s: %w", path, err)
	}
	if lf.Plugins == nil {
		lf.Plugins = map[string]LockfileEntry{}
	}
	return lf, nil
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
