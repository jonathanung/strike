package theme

import (
	"embed"
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

// ProjectThemesDir is <workDir>/.strike/themes.
func ProjectThemesDir(workDir string) string {
	if workDir == "" {
		return ""
	}
	return filepath.Join(workDir, ".strike", "themes")
}

// Catalog merges builtins, then user themes, then project themes. Later
// sources override earlier ones with the same id. workDir may be empty
// (skips project themes). Invalid files are skipped.
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
	if dir := ProjectThemesDir(workDir); dir != "" {
		add(loadDir(dir, "project"))
	}
	out := make([]Entry, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	return out
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
func globalRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".strike")
}
