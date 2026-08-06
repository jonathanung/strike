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
// and where it was loaded from (builtin, user, or project).
type Entry struct {
	ID     string
	Name   string
	Theme  Theme
	Source string // "builtin" | "user" | "project"
}

// BuiltinID is the stock strike palette id.
const BuiltinID = "strike"

// Builtin returns the embedded themes with strike (Default) first.
func Builtin() []Entry {
	out := []Entry{{
		ID:     BuiltinID,
		Name:   "Strike",
		Theme:  Default(),
		Source: "builtin",
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
		entry.Source = "builtin"
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
// Invalid files and disabled/malformed plugins are skipped.
//
// Plugin discovery is duplicated here with stdlib only so the TUI boundary
// (no internal/plugin import) still surfaces plugin themes in /theme.
func Catalog(workDir string) []Entry {
	byID := map[string]Entry{}
	order := []string{}
	add := func(list []Entry) {
		for _, e := range list {
			if _, seen := byID[e.ID]; !seen {
				order = append(order, e.ID)
			}
			byID[e.ID] = e
		}
	}
	add(Builtin())
	if dir := UserThemesDir(); dir != "" {
		add(loadDir(dir, "user"))
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
		add(loadDir(dir, "project"))
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
		if e.IsDir() {
			names = append(names, e.Name())
		}
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
			data, err := os.ReadFile(abs)
			if err != nil {
				continue
			}
			stem := strings.TrimSuffix(filepath.Base(abs), filepath.Ext(abs))
			entry, err := Parse(data, strings.ToLower(stem))
			if err != nil {
				continue
			}
			entry.Source = "plugin"
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
	// Strip line comments for jsonc.
	cleaned := stripLineComments(data)
	var doc struct {
		SchemaVersion int    `json:"schemaVersion"`
		ID            string `json:"id"`
		Contributions struct {
			Themes []struct {
				Path string `json:"path"`
			} `json:"themes"`
		} `json:"contributions"`
	}
	if err := json.Unmarshal(cleaned, &doc); err != nil {
		return "", nil, false
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

func stripLineComments(data []byte) []byte {
	// Enough for simple plugin.jsonc fixtures; full JSONC lives in internal/plugin.
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
	sep := string(os.PathSeparator)
	if joined != rootAbs && !strings.HasPrefix(joined, rootAbs+sep) {
		return "", fs.ErrInvalid
	}
	return joined, nil
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
