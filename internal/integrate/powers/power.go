// Package powers implements Powers (issue #1347): named capability packs
// that group MCP servers and skills Strike already knows, activated per
// session on demand instead of loaded for every turn.
//
// A Power is an Agent Plugins package (#1141): plugin.json declares the name
// and activation keywords, an optional mcp.json declares servers, and
// skills/<name>/SKILL.md declares skills. Packs live under
// <workspace>/.strike/powers/<pack>/ and ~/.strike/powers/<pack>/; the
// workspace copy wins on a name clash.
package powers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jonathanung/strike/internal/integrate/plugin"
)

// Scope says where a pack was loaded from.
type Scope string

const (
	ScopeWorkspace Scope = "workspace"
	ScopeGlobal    Scope = "global"
)

// Power is one loaded pack. Servers and Skills are references by name; the
// definitions stay where Strike already manages them.
type Power struct {
	Name     string
	Keywords []string
	Servers  []string
	Skills   []string
	Dir      string
	Scope    Scope
}

// LoadDir reads one pack directory.
func LoadDir(dir string, scope Scope) (Power, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "plugin.json"))
	if err != nil {
		return Power{}, fmt.Errorf("powers: %s: %w", dir, err)
	}
	var m struct {
		Name     string   `json:"name"`
		Keywords []string `json:"keywords"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return Power{}, fmt.Errorf("powers: %s/plugin.json: %w", dir, err)
	}
	if err := plugin.ValidateAPSName(m.Name); err != nil {
		return Power{}, fmt.Errorf("powers: %s/plugin.json: %w", dir, err)
	}
	p := Power{Name: strings.TrimSpace(m.Name), Dir: dir, Scope: scope, Keywords: normalizeKeywords(m.Keywords)}
	if p.Servers, err = loadServers(filepath.Join(dir, "mcp.json")); err != nil {
		return Power{}, fmt.Errorf("powers: %s/mcp.json: %w", dir, err)
	}
	if p.Skills, err = loadSkills(filepath.Join(dir, "skills")); err != nil {
		return Power{}, fmt.Errorf("powers: %s/skills: %w", dir, err)
	}
	return p, nil
}

// Discover loads every pack under the workspace and global powers roots.
// Either root may be empty or missing. A workspace pack replaces a global
// pack with the same name. Packs that fail to load are reported in errs and
// skipped; the rest still load.
func Discover(workspaceRoot, globalRoot string) (packs []Power, errs []error) {
	byName := map[string]Power{}
	for _, src := range []struct {
		root  string
		scope Scope
	}{{globalRoot, ScopeGlobal}, {workspaceRoot, ScopeWorkspace}} {
		if src.root == "" {
			continue
		}
		entries, err := os.ReadDir(src.root)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("powers: %w", err))
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			p, err := LoadDir(filepath.Join(src.root, e.Name()), src.scope)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			byName[p.Name] = p // workspace pass runs last and wins
		}
	}
	for _, p := range byName {
		packs = append(packs, p)
	}
	sort.Slice(packs, func(i, j int) bool { return packs[i].Name < packs[j].Name })
	return packs, errs
}

func loadServers(path string) ([]string, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var m struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	if m.MCPServers == nil {
		return nil, errors.New("missing mcpServers")
	}
	out := make([]string, 0, len(m.MCPServers))
	for name := range m.MCPServers {
		if strings.TrimSpace(name) == "" {
			return nil, errors.New("empty server name")
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

func loadSkills(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if st, err := os.Stat(filepath.Join(dir, e.Name(), "SKILL.md")); err == nil && st.Mode().IsRegular() {
			out = append(out, e.Name())
		}
	}
	return out, nil // ReadDir is already sorted
}

func normalizeKeywords(in []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, k := range in {
		k = strings.ToLower(strings.Join(strings.Fields(k), " "))
		if k == "" {
			continue
		}
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, k)
	}
	return out
}
