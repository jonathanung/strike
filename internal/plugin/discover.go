package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jonathanung/strike-cli/internal/version"
)

// Options controls discovery roots and the running Strike version.
type Options struct {
	// WorkDir is the project working directory (may be empty).
	WorkDir string
	// GlobalRoot overrides ~/.strike (tests). Empty uses defaultGlobalRoot().
	GlobalRoot string
	// ProjectRoot overrides <workDir>/.strike (tests). Empty derives from WorkDir.
	ProjectRoot string
	// StrikeVersion overrides version.Version for compatibility checks.
	StrikeVersion string
}

// FileRef is one resolved contribution file with provenance.
type FileRef struct {
	PluginID string
	Version  string
	Source   Scope
	RelPath  string // manifest-relative path
	AbsPath  string
	// ProfileName is set for provider contributions when declared.
	ProfileName string
}

// Plugin is one enabled, validated plugin ready for passive contribution load.
type Plugin struct {
	ID       string
	Version  string
	Name     string
	Source   Scope
	Root     string
	Manifest Manifest

	Agents    []FileRef
	Skills    []FileRef
	Workflows []FileRef
	Themes    []FileRef
	Providers []FileRef
}

// Result is the outcome of Discover.
type Result struct {
	// Plugins in deterministic merge order: global (id asc) then project (id asc).
	// Same id: project replaces global entirely (global omitted).
	Plugins []Plugin
	// Diagnostics collected during discovery (malformed, disabled, collisions prep, …).
	Diagnostics []Diagnostic
}

// Discover finds enabled plugins under global and project roots, validates
// manifests, resolves passive contribution paths, and returns a stable result.
// Executable contributions (mcp/harnesses/hooks) are not activated.
func Discover(opts Options) Result {
	var out Result
	globalRoot := opts.GlobalRoot
	if globalRoot == "" {
		globalRoot = defaultGlobalRoot()
	}
	projectRoot := opts.ProjectRoot
	if projectRoot == "" && opts.WorkDir != "" {
		projectRoot = defaultProjectRoot(opts.WorkDir)
	}
	strikeVer := opts.StrikeVersion
	if strikeVer == "" {
		strikeVer = version.Version
	}

	globalLock, projectLock, lockDiags := loadLockfiles(globalRoot, projectRoot)
	out.Diagnostics = append(out.Diagnostics, lockDiags...)

	globalPlugins := scanScope(globalRoot, ScopeGlobal, strikeVer, &out.Diagnostics)
	projectPlugins := scanScope(projectRoot, ScopeProject, strikeVer, &out.Diagnostics)

	// Project id shadows global entirely.
	projectIDs := map[string]struct{}{}
	for _, p := range projectPlugins {
		projectIDs[p.ID] = struct{}{}
	}
	var merged []Plugin
	for _, p := range globalPlugins {
		if _, shadowed := projectIDs[p.ID]; shadowed {
			out.Diagnostics = append(out.Diagnostics, Diagnostic{
				Severity: SeverityInfo,
				Code:     "shadowed",
				Message:  "project install shadows global plugin with the same id",
				PluginID: p.ID,
				Version:  p.Version,
				Source:   ScopeGlobal,
			})
			continue
		}
		if !IsEnabled(p.ID, globalLock, projectLock) {
			out.Diagnostics = append(out.Diagnostics, Diagnostic{
				Severity: SeverityInfo,
				Code:     "disabled",
				Message:  "plugin disabled; contributing nothing",
				PluginID: p.ID,
				Version:  p.Version,
				Source:   p.Source,
			})
			continue
		}
		merged = append(merged, p)
	}
	for _, p := range projectPlugins {
		if !IsEnabled(p.ID, globalLock, projectLock) {
			out.Diagnostics = append(out.Diagnostics, Diagnostic{
				Severity: SeverityInfo,
				Code:     "disabled",
				Message:  "plugin disabled; contributing nothing",
				PluginID: p.ID,
				Version:  p.Version,
				Source:   p.Source,
			})
			continue
		}
		merged = append(merged, p)
	}
	out.Plugins = merged
	return out
}

func scanScope(strikeRoot string, scope Scope, strikeVer string, diags *[]Diagnostic) []Plugin {
	if strikeRoot == "" {
		return nil
	}
	pluginsDir := filepath.Join(strikeRoot, "plugins")
	entries, err := os.ReadDir(pluginsDir)
	if err != nil {
		return nil
	}
	var dirNames []string
	for _, e := range entries {
		// Skip hidden/staging/backup dirs used by lifecycle install.
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			dirNames = append(dirNames, e.Name())
		}
	}
	sort.Strings(dirNames)

	var out []Plugin
	seenID := map[string]string{} // id → root (duplicate dirs)
	for _, name := range dirNames {
		root := filepath.Join(pluginsDir, name)
		p, dlist := loadOne(root, scope, strikeVer)
		*diags = append(*diags, dlist...)
		if p == nil {
			continue
		}
		if prev, ok := seenID[p.ID]; ok {
			*diags = append(*diags, Diagnostic{
				Severity: SeverityError,
				Code:     "duplicate_id",
				Message:  fmt.Sprintf("duplicate plugin id also found at %s; skipping", prev),
				PluginID: p.ID,
				Version:  p.Version,
				Source:   scope,
				Path:     root,
			})
			continue
		}
		seenID[p.ID] = root
		out = append(out, *p)
	}
	// Stable by plugin id (dir walk already sorted; re-sort by id for contract).
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func loadOne(root string, scope Scope, strikeVer string) (*Plugin, []Diagnostic) {
	var diags []Diagnostic
	m, manPath, err := ReadManifest(root)
	if err != nil {
		diags = append(diags, Diagnostic{
			Severity: SeverityError,
			Code:     "malformed",
			Message:  err.Error(),
			Source:   scope,
			Path:     root,
		})
		return nil, diags
	}
	base := Diagnostic{
		PluginID: m.ID,
		Version:  m.Version,
		Source:   scope,
		Path:     manPath,
	}

	if m.SchemaVersion > SchemaVersion {
		d := base
		d.Severity = SeverityError
		d.Code = "schema_version"
		d.Message = fmt.Sprintf("unsupported schemaVersion %d (max %d); upgrade Strike", m.SchemaVersion, SchemaVersion)
		diags = append(diags, d)
		return nil, diags
	}

	if ok, reason := strikeCompatible(strikeVer, m.Strike.Min, m.Strike.Max); !ok {
		d := base
		d.Severity = SeverityError
		d.Code = "strike_version"
		d.Message = reason
		diags = append(diags, d)
		return nil, diags
	}

	if m.Digest != "" {
		got, err := ComputeDigest(root)
		if err != nil {
			d := base
			d.Severity = SeverityError
			d.Code = "digest"
			d.Message = err.Error()
			diags = append(diags, d)
			return nil, diags
		}
		if got != strings.TrimSpace(m.Digest) {
			d := base
			d.Severity = SeverityError
			d.Code = "digest"
			d.Message = fmt.Sprintf("content digest mismatch: manifest %s computed %s", m.Digest, got)
			diags = append(diags, d)
			return nil, diags
		}
	}

	p := &Plugin{
		ID:       m.ID,
		Version:  m.Version,
		Name:     m.Name,
		Source:   scope,
		Root:     root,
		Manifest: m,
	}

	resolve := func(kind string, entries []PathEntry, extOK func(string) bool) []FileRef {
		var refs []FileRef
		// Contribution path ascending for stability.
		sorted := append([]PathEntry(nil), entries...)
		sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })
		for _, e := range sorted {
			abs, err := ResolveUnderRoot(root, e.Path)
			if err != nil {
				d := base
				d.Severity = SeverityError
				d.Code = "path"
				d.Path = e.Path
				d.Message = err.Error()
				diags = append(diags, d)
				continue
			}
			if st, err := os.Stat(abs); err != nil || st.IsDir() {
				d := base
				d.Severity = SeverityError
				d.Code = "missing"
				d.Path = e.Path
				if err != nil {
					d.Message = fmt.Sprintf("contribution file not found: %v", err)
				} else {
					d.Message = "contribution path is a directory"
				}
				diags = append(diags, d)
				continue
			}
			if extOK != nil && !extOK(abs) {
				d := base
				d.Severity = SeverityError
				d.Code = "malformed"
				d.Path = e.Path
				d.Message = fmt.Sprintf("unexpected file type for %s contribution", kind)
				diags = append(diags, d)
				continue
			}
			refs = append(refs, FileRef{
				PluginID: m.ID,
				Version:  m.Version,
				Source:   scope,
				RelPath:  e.Path,
				AbsPath:  abs,
			})
		}
		return refs
	}

	p.Agents = resolve("agents", m.Contributions.Agents, func(p string) bool {
		return strings.EqualFold(filepath.Ext(p), ".md")
	})
	p.Skills = resolve("skills", m.Contributions.Skills, func(p string) bool {
		base := filepath.Base(p)
		return strings.EqualFold(filepath.Ext(p), ".md") || strings.EqualFold(base, "SKILL.md")
	})
	p.Workflows = resolve("workflows", m.Contributions.Workflows, func(p string) bool {
		return strings.EqualFold(filepath.Ext(p), ".json")
	})
	p.Themes = resolve("themes", m.Contributions.Themes, func(p string) bool {
		return strings.EqualFold(filepath.Ext(p), ".json")
	})

	// Providers with optional profileName.
	provEntries := append([]ProviderEntry(nil), m.Contributions.Providers...)
	sort.SliceStable(provEntries, func(i, j int) bool { return provEntries[i].Path < provEntries[j].Path })
	for _, e := range provEntries {
		abs, err := ResolveUnderRoot(root, e.Path)
		if err != nil {
			d := base
			d.Severity = SeverityError
			d.Code = "path"
			d.Path = e.Path
			d.Message = err.Error()
			diags = append(diags, d)
			continue
		}
		if st, err := os.Stat(abs); err != nil || st.IsDir() {
			d := base
			d.Severity = SeverityError
			d.Code = "missing"
			d.Path = e.Path
			d.Message = "provider contribution file not found"
			diags = append(diags, d)
			continue
		}
		p.Providers = append(p.Providers, FileRef{
			PluginID:    m.ID,
			Version:     m.Version,
			Source:      scope,
			RelPath:     e.Path,
			AbsPath:     abs,
			ProfileName: strings.TrimSpace(e.ProfileName),
		})
	}

	// If every passive path failed and there were passive entries, skip plugin
	// entirely so it cannot partially shadow. Executable-only plugins are kept
	// in the list with empty passive slices (no activation here).
	passiveDeclared := len(m.Contributions.Agents)+len(m.Contributions.Skills)+
		len(m.Contributions.Workflows)+len(m.Contributions.Themes)+len(m.Contributions.Providers) > 0
	passiveOK := len(p.Agents)+len(p.Skills)+len(p.Workflows)+len(p.Themes)+len(p.Providers) > 0
	if passiveDeclared && !passiveOK {
		d := base
		d.Severity = SeverityError
		d.Code = "malformed"
		d.Message = "no valid passive contributions; plugin skipped"
		diags = append(diags, d)
		return nil, diags
	}

	// Note executable contributions when present. CompileExecutables refines
	// this with trust match (trusted vs untrusted); Discover stays passive-only.
	if HasExecutableContributionsAt(m, root) {
		d := base
		d.Severity = SeverityInfo
		d.Code = "executable_inactive"
		d.Message = "executable contributions present; activation requires matching trust record"
		diags = append(diags, d)
	}

	return p, diags
}

func defaultGlobalRoot() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	root := filepath.Join(home, ".strike")
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		return resolved
	}
	return root
}

func defaultProjectRoot(workDir string) string {
	if workDir == "" {
		return ""
	}
	root := filepath.Join(workDir, ".strike")
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		return resolved
	}
	return root
}

// GlobalPluginsDir returns ~/.strike/plugins.
func GlobalPluginsDir() string {
	r := defaultGlobalRoot()
	if r == "" {
		return ""
	}
	return filepath.Join(r, "plugins")
}

// ProjectPluginsDir returns <workDir>/.strike/plugins.
func ProjectPluginsDir(workDir string) string {
	r := defaultProjectRoot(workDir)
	if r == "" {
		return ""
	}
	return filepath.Join(r, "plugins")
}

// AllFileRefs returns passive refs of kind across plugins in merge order.
func (r Result) AllFileRefs(kind string) []FileRef {
	var out []FileRef
	for _, p := range r.Plugins {
		switch kind {
		case "agents":
			out = append(out, p.Agents...)
		case "skills":
			out = append(out, p.Skills...)
		case "workflows":
			out = append(out, p.Workflows...)
		case "themes":
			out = append(out, p.Themes...)
		case "providers":
			out = append(out, p.Providers...)
		}
	}
	return out
}
