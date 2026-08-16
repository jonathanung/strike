package theme

import (
	"embed"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed themes/*.json
var builtinFS embed.FS

// Entry is one selectable theme: stable id, display name, resolved palette,
// and where it was loaded from (builtin, user, project, or plugin).
type Entry struct {
	ID     string
	Name   string
	Theme  Theme
	Source string // "builtin" | "user" | "project" | "plugin"
	// PluginID is set when Source is "plugin" (manifest id for provenance).
	PluginID string
	// Overrode is the previous source label when this entry won an id collision
	// (deterministic §4.1 precedence). Empty when no collision.
	Overrode string
}

// Source labels for Entry.Source.
const (
	SourceBuiltin = "builtin"
	SourceUser    = "user"
	SourceProject = "project"
	SourcePlugin  = "plugin"
)

// Provenance returns a short display label including plugin id when present.
func (e Entry) Provenance() string {
	if e.Source == SourcePlugin && e.PluginID != "" {
		return SourcePlugin + ":" + e.PluginID
	}
	if e.Source == "" {
		return SourceBuiltin
	}
	return e.Source
}

// BuiltinID is the stock strike palette id.
const BuiltinID = "strike"

// Builtin returns the embedded themes with strike (Default) first.
func Builtin() []Entry {
	out := []Entry{{
		ID:     BuiltinID,
		Name:   "Strike",
		Theme:  Default(),
		Source: SourceBuiltin,
	}}
	entries, err := fs.ReadDir(builtinFS, "themes")
	if err != nil {
		return out
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		data, err := builtinFS.ReadFile("themes/" + name)
		if err != nil {
			continue
		}
		stem := strings.TrimSuffix(name, ".json")
		entry, err := Parse(data, stem)
		if err != nil || entry.ID == BuiltinID {
			continue
		}
		entry.Source = SourceBuiltin
		out = append(out, entry)
	}
	return out
}

// UserThemesDir is ~/.strike/themes.
func UserThemesDir() string {
	root := globalRoot()
	if root == "" {
		return ""
	}
	return filepath.Join(root, "themes")
}

// ProjectThemesDir is <workDir>/.strike/themes. Existing .strike directory
// symlinks are resolved so project themes can live outside the work tree.
func ProjectThemesDir(workDir string) string {
	if workDir == "" {
		return ""
	}
	root := filepath.Join(workDir, ".strike")
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	return filepath.Join(root, "themes")
}

// Catalog merges builtins, user themes, global plugin themes, project themes,
// then project plugin themes (docs/plugins.md §4.1). Later sources override
// earlier ones with the same id. workDir may be empty (skips project layers).
// Invalid files, staging dirs, and disabled/malformed plugins are skipped so
// bad contributions cannot break startup or silently shadow winners.
//
// Plugin discovery is duplicated here with stdlib only so the TUI boundary
// (no internal/integrate/plugin import) still surfaces plugin themes in /theme.
// Catalog install/update of theme plugins uses the same lockfile path as other
// plugins (host.Plugins / strike plugin); this loader only reads contributions.
func Catalog(workDir string) []Entry {
	byID := map[string]Entry{}
	order := []string{}
	add := func(list []Entry) {
		for _, e := range list {
			if e.ID == "" {
				continue
			}
			if prev, seen := byID[e.ID]; seen {
				// Record what this winner replaced (visible collision).
				e.Overrode = prev.Provenance()
			} else {
				order = append(order, e.ID)
			}
			byID[e.ID] = e
		}
	}
	add(Builtin())
	if dir := UserThemesDir(); dir != "" {
		add(loadDir(dir, SourceUser))
	}
	// Project lockfile can disable global plugins too.
	var projectDisabled map[string]bool
	if workDir != "" {
		pr := filepath.Join(workDir, ".strike")
		if resolved, err := filepath.EvalSymlinks(pr); err == nil {
			pr = resolved
		}
		projectDisabled = readPluginDisabled(filepath.Join(pr, "plugins.lock.json"))
	}
	add(loadPluginThemeLayer(globalRoot(), projectDisabled))
	if dir := ProjectThemesDir(workDir); dir != "" {
		add(loadDir(dir, SourceProject))
	}
	if workDir != "" {
		add(loadPluginThemeLayer(filepath.Join(workDir, ".strike"), nil))
	}
	out := make([]Entry, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	return out
}

// loadPluginThemeLayer loads theme contributions from <strikeRoot>/plugins.
// Malformed plugins are skipped (must not silently shadow). Disabled plugins
// (plugins.lock.json) contribute nothing. extraDisabled merges additional ids
// (e.g. project lockfile disabling a global plugin).
func loadPluginThemeLayer(strikeRoot string, extraDisabled map[string]bool) []Entry {
	if strikeRoot == "" {
		return nil
	}
	if resolved, err := filepath.EvalSymlinks(strikeRoot); err == nil {
		strikeRoot = resolved
	}
	disabled := readPluginDisabled(filepath.Join(strikeRoot, "plugins.lock.json"))
	for id, v := range extraDisabled {
		if v {
			if disabled == nil {
				disabled = map[string]bool{}
			}
			disabled[id] = true
		}
	}
	pluginsDir := filepath.Join(strikeRoot, "plugins")
	entries, err := os.ReadDir(pluginsDir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		// Skip lifecycle staging/backup dirs (must not load partial installs).
		if strings.HasPrefix(name, ".") || strings.HasPrefix(name, ".staging") {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	var out []Entry
	// Track by plugin id for stable ordering (id ascending).
	type plug struct {
		id     string
		themes []Entry
	}
	var plugs []plug
	for _, name := range names {
		root := filepath.Join(pluginsDir, name)
		id, themePaths, ok := readPluginThemePaths(root)
		if !ok {
			continue
		}
		if disabled[id] {
			continue
		}
		var themes []Entry
		sort.Strings(themePaths)
		for _, rel := range themePaths {
			abs, err := resolveUnderPluginRoot(root, rel)
			if err != nil {
				continue
			}
			st, err := os.Stat(abs)
			if err != nil || !st.Mode().IsRegular() {
				continue
			}
			data, err := os.ReadFile(abs)
			if err != nil {
				continue
			}
			stem := strings.TrimSuffix(filepath.Base(abs), filepath.Ext(abs))
			entry, err := Parse(data, strings.ToLower(stem))
			if err != nil {
				// Invalid theme JSON must not break catalog/startup.
				continue
			}
			entry.Source = SourcePlugin
			entry.PluginID = id
			themes = append(themes, entry)
		}
		if len(themes) > 0 {
			plugs = append(plugs, plug{id: id, themes: themes})
		}
	}
	sort.SliceStable(plugs, func(i, j int) bool { return plugs[i].id < plugs[j].id })
	for _, p := range plugs {
		out = append(out, p.themes...)
	}
	return out
}

func readPluginDisabled(lockPath string) map[string]bool {
	data, err := os.ReadFile(lockPath)
	if err != nil {
		return nil
	}
	// Minimal JSONC: strip // comments only for this tiny file.
	var raw struct {
		Plugins map[string]struct {
			Enabled *bool `json:"enabled"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil
	}
	out := map[string]bool{}
	for id, e := range raw.Plugins {
		if e.Enabled != nil && !*e.Enabled {
			out[id] = true
		}
	}
	return out
}

func readPluginThemePaths(root string) (id string, paths []string, ok bool) {
	var data []byte
	var err error
	for _, name := range []string{"plugin.json", "plugin.jsonc"} {
		data, err = os.ReadFile(filepath.Join(root, name))
		if err == nil {
			break
		}
	}
	if err != nil {
		return "", nil, false
	}
	cleaned := stripLineComments(data)
	var doc struct {
		Schema        string                     `json:"$schema"`
		SchemaVersion int                        `json:"schemaVersion"`
		ID            string                     `json:"id"`
		Name          string                     `json:"name"`
		Extensions    map[string]json.RawMessage `json:"extensions"`
		Contributions struct {
			Themes []struct {
				Path string `json:"path"`
			} `json:"themes"`
		} `json:"contributions"`
	}
	if err := json.Unmarshal(cleaned, &doc); err != nil {
		return "", nil, false
	}
	if strings.TrimSpace(doc.Schema) == "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json" {
		id = strings.TrimSpace(doc.Name)
		if id == "" {
			return "", nil, false
		}
		if strikeCLISkipFromExtensions(doc.Extensions) {
			return id, nil, false
		}
		extDir := filepath.Join(root, "com.strike.cli")
		if _, err := confinedExistingPath(root, extDir); err != nil {
			return id, nil, false
		}
		themeDir := filepath.Join(root, "com.strike.cli", "themes")
		resolvedThemes, err := confinedExistingPath(root, themeDir)
		if err != nil {
			return id, nil, false
		}
		st, err := os.Stat(resolvedThemes)
		if err != nil || !st.IsDir() {
			return id, nil, false
		}
		entries, err := os.ReadDir(resolvedThemes)
		if err != nil {
			return id, nil, false
		}
		var names []string
		for _, e := range entries {
			if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			if !strings.EqualFold(filepath.Ext(e.Name()), ".json") {
				continue
			}
			names = append(names, e.Name())
		}
		sort.Strings(names)
		for _, name := range names {
			rel := "com.strike.cli/themes/" + name
			abs, err := resolveUnderPluginRoot(root, rel)
			if err != nil {
				continue
			}
			st, err := os.Stat(abs)
			if err != nil || !st.Mode().IsRegular() {
				continue
			}
			paths = append(paths, rel)
		}
		if len(paths) == 0 {
			return id, nil, false
		}
		return id, paths, true
	}
	if doc.SchemaVersion != 1 || strings.TrimSpace(doc.ID) == "" {
		return "", nil, false
	}
	for _, t := range doc.Contributions.Themes {
		if p := strings.TrimSpace(t.Path); p != "" {
			paths = append(paths, p)
		}
	}
	if len(paths) == 0 {
		return doc.ID, nil, false
	}
	return doc.ID, paths, true
}

func strikeCLISkipFromExtensions(ext map[string]json.RawMessage) bool {
	raw, ok := ext["com.strike.cli"]
	if !ok {
		return false
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return true
	}
	if v, ok := obj["displayName"]; ok {
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			return true
		}
	}
	if v, ok := obj["strike"]; ok {
		var sr struct {
			Min string `json:"min"`
			Max string `json:"max"`
		}
		if err := json.Unmarshal(v, &sr); err != nil {
			return true
		}
	}
	if v, ok := obj["capabilities"]; ok {
		var caps []string
		if err := json.Unmarshal(v, &caps); err != nil {
			return true
		}
	}
	if v, ok := obj["digest"]; ok {
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			return true
		}
	}
	return false
}

func stripLineComments(data []byte) []byte {
	// Enough for simple plugin.jsonc fixtures; full JSONC lives in internal/integrate/plugin.
	lines := strings.Split(string(data), "\n")
	var b strings.Builder
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "//") {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

func resolveUnderPluginRoot(root, rel string) (string, error) {
	if rel == "" || strings.HasPrefix(rel, "/") || strings.Contains(rel, "..") {
		return "", fs.ErrInvalid
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(rootAbs); err == nil {
		rootAbs = resolved
	}
	joined := filepath.Clean(filepath.Join(rootAbs, filepath.FromSlash(rel)))
	if !pathUnderRoot(rootAbs, joined) {
		return "", fs.ErrInvalid
	}
	if fi, err := os.Lstat(joined); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 || fi.IsDir() {
			resolved, err := filepath.EvalSymlinks(joined)
			if err != nil {
				return "", err
			}
			if !pathUnderRoot(rootAbs, resolved) {
				return "", fs.ErrInvalid
			}
			return resolved, nil
		}
		parent := filepath.Dir(joined)
		if resolvedParent, err := filepath.EvalSymlinks(parent); err == nil {
			resolved := filepath.Join(resolvedParent, filepath.Base(joined))
			if !pathUnderRoot(rootAbs, resolved) {
				return "", fs.ErrInvalid
			}
			return resolved, nil
		}
	}
	return joined, nil
}

func confinedExistingPath(root, path string) (string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(rootAbs); err == nil {
		rootAbs = resolved
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	if !pathUnderRoot(rootAbs, abs) {
		return "", fs.ErrInvalid
	}
	return abs, nil
}

func pathUnderRoot(root, path string) bool {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if root == path {
		return true
	}
	sep := string(os.PathSeparator)
	return strings.HasPrefix(path, root+sep)
}

// Lookup finds an entry by id (case-sensitive).
func Lookup(entries []Entry, id string) (Entry, bool) {
	id = strings.TrimSpace(id)
	for _, e := range entries {
		if e.ID == id {
			return e, true
		}
	}
	return Entry{}, false
}

func loadDir(dir, source string) []Entry {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".json") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	var out []Entry
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		stem := strings.TrimSuffix(name, filepath.Ext(name))
		entry, err := Parse(data, strings.ToLower(stem))
		if err != nil {
			continue
		}
		entry.Source = source
		out = append(out, entry)
	}
	return out
}

// globalRoot mirrors config.GlobalRoot without importing config (TUI boundary).
// Existing ~/.strike directory symlinks are resolved so themes load from the
// real state directory.
func globalRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	root := filepath.Join(home, ".strike")
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		return resolved
	}
	return root
}
