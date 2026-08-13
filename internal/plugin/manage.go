package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jonathanung/strike-cli/internal/version"
)

// ListOptions controls ListInstalled.
type ListOptions struct {
	WorkDir     string
	GlobalRoot  string
	ProjectRoot string
	// Scope limits to one scope; empty lists both.
	Scope Scope
	// StrikeVersion for load diagnostics on inspect-like listing.
	StrikeVersion string
}

// InstalledPlugin is one on-disk install (enabled or disabled).
type InstalledPlugin struct {
	ID        string
	Version   string
	Name      string
	Scope     Scope
	Root      string
	Enabled   bool
	Digest    string
	Source    *SourceIdentity
	Trust     *TrustRecord
	Manifest  *Manifest
	LoadError string // non-empty when manifest/load failed
}

// ListInstalled returns plugins found under configured roots (including disabled).
func ListInstalled(opts ListOptions) ([]InstalledPlugin, []Diagnostic, error) {
	strikeVer := opts.StrikeVersion
	if strikeVer == "" {
		strikeVer = version.Version
	}
	globalRoot := opts.GlobalRoot
	if globalRoot == "" {
		globalRoot = defaultGlobalRoot()
	}
	projectRoot := opts.ProjectRoot
	if projectRoot == "" && opts.WorkDir != "" {
		projectRoot = defaultProjectRoot(opts.WorkDir)
	}
	globalLock, projectLock, lockDiags := loadLockfiles(globalRoot, projectRoot)

	var out []InstalledPlugin
	scan := func(strikeRoot string, scope Scope, lock Lockfile) {
		if strikeRoot == "" {
			return
		}
		if opts.Scope != "" && opts.Scope != scope {
			return
		}
		pluginsDir := filepath.Join(strikeRoot, "plugins")
		entries, err := os.ReadDir(pluginsDir)
		if err != nil {
			return
		}
		var names []string
		for _, e := range entries {
			if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
				names = append(names, e.Name())
			}
		}
		sort.Strings(names)
		for _, name := range names {
			root := filepath.Join(pluginsDir, name)
			ip := InstalledPlugin{
				Scope: scope,
				Root:  root,
			}
			m, _, err := ReadManifest(root)
			if err != nil {
				ip.ID = name
				ip.LoadError = err.Error()
				if e, ok := lock.Plugins[name]; ok {
					ip.Enabled = EntryEnabled(e)
					ip.Digest = e.Digest
					ip.Source = e.Source
					ip.Trust = e.Trust
					ip.Version = e.Version
				} else {
					ip.Enabled = true
				}
				out = append(out, ip)
				continue
			}
			ip.ID = m.ID
			ip.Version = m.Version
			ip.Name = m.Name
			ip.Manifest = &m
			// Enablement: project lock overrides for project installs; for global
			// use global lock only (IsEnabled also checks project which may disable
			// a global id when project has an entry — match Discover).
			ip.Enabled = IsEnabled(m.ID, globalLock, projectLock)
			if e, ok := lock.Plugins[m.ID]; ok {
				ip.Digest = e.Digest
				ip.Source = e.Source
				ip.Trust = e.Trust
				if e.Version != "" {
					ip.Version = e.Version
				}
			}
			if ip.Digest == "" {
				if d, err := ComputeDigest(root); err == nil {
					ip.Digest = d
				}
			}
			out = append(out, ip)
		}
	}
	scan(globalRoot, ScopeGlobal, globalLock)
	scan(projectRoot, ScopeProject, projectLock)

	// Stable: scope (global first) then id.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Scope != out[j].Scope {
			return out[i].Scope == ScopeGlobal
		}
		return out[i].ID < out[j].ID
	})
	return out, lockDiags, nil
}

// EnableOptions controls Enable/Disable.
type EnableOptions struct {
	ID          string
	Scope       Scope // empty: find install; prefer project if both
	WorkDir     string
	GlobalRoot  string
	ProjectRoot string
}

// Enable sets enabled=true in the lockfile for the install scope.
func Enable(opts EnableOptions) error {
	return setEnabled(opts, true)
}

// Disable sets enabled=false; source files are preserved.
func Disable(opts EnableOptions) error {
	return setEnabled(opts, false)
}

func setEnabled(opts EnableOptions, enabled bool) error {
	id := strings.TrimSpace(opts.ID)
	if err := ValidatePluginKey(id); err != nil {
		return err
	}
	roots, scope, err := resolveManageScope(opts)
	if err != nil {
		return err
	}
	dest := roots.InstallDir(id)
	// Also accept directory that contains this id even if dir name differs.
	if _, err := os.Stat(dest); err != nil {
		found, findErr := findInstallRoot(roots, id)
		if findErr != nil {
			return fmt.Errorf("plugin %q not installed in %s scope", id, scope)
		}
		dest = found
	}
	_ = dest

	return WithLockfileLock(roots.LockPath, func(lf Lockfile) (Lockfile, bool, error) {
		e := lf.Plugins[id]
		e.Enabled = boolPtr(enabled)
		// Preserve provenance fields.
		lf = setLockEntry(lf, id, e)
		return lf, false, nil
	})
}

// RemoveOptions controls Remove.
type RemoveOptions struct {
	ID          string
	Scope       Scope
	WorkDir     string
	GlobalRoot  string
	ProjectRoot string
	// Confirm must be true (CLI maps --yes).
	Confirm bool
}

// Remove deletes the plugin directory and lockfile entry. Requires Confirm.
func Remove(opts RemoveOptions) error {
	if !opts.Confirm {
		return fmt.Errorf("remove requires confirmation (--yes)")
	}
	id := strings.TrimSpace(opts.ID)
	if err := ValidatePluginKey(id); err != nil {
		return err
	}
	roots, scope, err := resolveManageScope(EnableOptions{
		ID:          opts.ID,
		Scope:       opts.Scope,
		WorkDir:     opts.WorkDir,
		GlobalRoot:  opts.GlobalRoot,
		ProjectRoot: opts.ProjectRoot,
	})
	if err != nil {
		return err
	}

	dest := roots.InstallDir(id)
	if _, err := os.Stat(dest); err != nil {
		found, findErr := findInstallRoot(roots, id)
		if findErr != nil {
			// Still drop lockfile entry if present.
			return WithLockfileLock(roots.LockPath, func(lf Lockfile) (Lockfile, bool, error) {
				if _, ok := lf.Plugins[id]; !ok {
					return lf, true, fmt.Errorf("plugin %q not installed in %s scope", id, scope)
				}
				return deleteLockEntry(lf, id), false, nil
			})
		}
		dest = found
	}

	return WithLockfileLock(roots.LockPath, func(lf Lockfile) (Lockfile, bool, error) {
		if err := roots.removeAllUnderPlugins(dest); err != nil {
			return lf, true, err
		}
		lf = deleteLockEntry(lf, id)
		return lf, false, nil
	})
}

func resolveManageScope(opts EnableOptions) (Roots, Scope, error) {
	scope := opts.Scope
	if scope == "" {
		// Prefer project if installed there, else global.
		pr, err := ResolveRoots(ScopeProject, Options{
			WorkDir:     opts.WorkDir,
			GlobalRoot:  opts.GlobalRoot,
			ProjectRoot: opts.ProjectRoot,
		})
		if err == nil {
			if root, err := findInstallRoot(pr, opts.ID); err == nil && root != "" {
				return pr, ScopeProject, nil
			}
		}
		gr, err := ResolveRoots(ScopeGlobal, Options{
			WorkDir:     opts.WorkDir,
			GlobalRoot:  opts.GlobalRoot,
			ProjectRoot: opts.ProjectRoot,
		})
		if err != nil {
			return Roots{}, "", err
		}
		return gr, ScopeGlobal, nil
	}
	r, err := ResolveRoots(scope, Options{
		WorkDir:     opts.WorkDir,
		GlobalRoot:  opts.GlobalRoot,
		ProjectRoot: opts.ProjectRoot,
	})
	return r, scope, err
}

func findInstallRoot(roots Roots, id string) (string, error) {
	// Prefer id-named directory.
	cand := roots.InstallDir(id)
	if m, _, err := ReadManifest(cand); err == nil && m.ID == id {
		return cand, nil
	}
	entries, err := os.ReadDir(roots.PluginsDir)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		root := filepath.Join(roots.PluginsDir, e.Name())
		m, _, err := ReadManifest(root)
		if err == nil && m.ID == id {
			return root, nil
		}
	}
	return "", fmt.Errorf("not found")
}

// Inspect returns a single installed plugin by id (project preferred when both).
func Inspect(opts EnableOptions) (InstalledPlugin, error) {
	list, _, err := ListInstalled(ListOptions{
		WorkDir:     opts.WorkDir,
		GlobalRoot:  opts.GlobalRoot,
		ProjectRoot: opts.ProjectRoot,
		Scope:       opts.Scope,
	})
	if err != nil {
		return InstalledPlugin{}, err
	}
	id := strings.TrimSpace(opts.ID)
	var match *InstalledPlugin
	for i := range list {
		if list[i].ID != id {
			continue
		}
		// Prefer project when scope unset.
		if match == nil || list[i].Scope == ScopeProject {
			p := list[i]
			match = &p
		}
	}
	if match == nil {
		return InstalledPlugin{}, fmt.Errorf("plugin %q not found", id)
	}
	return *match, nil
}
