package local

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jonathanung/strike-cli/internal/config"
	"github.com/jonathanung/strike-cli/internal/frontend/host"
)

// defaultGlobalConfigStub matches config.Restore's starter global config.
const defaultGlobalConfigStub = `{
  "provider": "anthropic",
  "defaultAgent": "build"
}
`

// emptyJSONObjectStub is the minimal valid map-shaped sidecar / project config.
const emptyJSONObjectStub = "{}\n"

// configFilesAdapter implements host.ConfigFiles against real disk paths.
type configFilesAdapter struct{}

func (configFilesAdapter) List(workDir string) []host.ConfigFileRef {
	var out []host.ConfigFileRef
	if root := config.GlobalRoot(); root != "" {
		out = append(out, listScopeRefs(host.ConfigScopeGlobal, root, workDir)...)
	}
	if workDir != "" {
		// Always show project primary slots even when .strike is missing so
		// users can create project config from the picker (issue #514 default).
		projRoot := filepath.Join(workDir, ".strike")
		if resolved, err := filepath.EvalSymlinks(projRoot); err == nil {
			projRoot = resolved
		}
		out = append(out, listScopeRefs(host.ConfigScopeProject, projRoot, workDir)...)
	}
	return out
}

func listScopeRefs(scope host.ConfigFileScope, root, workDir string) []host.ConfigFileRef {
	var out []host.ConfigFileRef
	out = append(out, primarySlotRefs(scope, root, workDir)...)
	out = append(out, listExtraRefs(scope, root, workDir, "agents", "*.md")...)
	out = append(out, listExtraRefs(scope, root, workDir, "skills", "*.md")...)
	out = append(out, listExtraRefs(scope, root, workDir, "themes", "*")...)
	out = append(out, listExtraRefs(scope, root, workDir, "workflows", "*.json")...)
	return out
}

func primarySlotRefs(scope host.ConfigFileScope, root, workDir string) []host.ConfigFileRef {
	type slot struct {
		id    string
		label string
		path  string
	}
	var slots []slot
	switch scope {
	case host.ConfigScopeGlobal:
		slots = []slot{
			{"config", "Main config", config.GlobalPath()},
			{"mcp", "MCP servers", config.GlobalMCPFilePath()},
			{"providers", "Providers", config.GlobalProvidersFilePath()},
			{"keybinds", "Keybinds", config.GlobalKeybindsFilePath()},
		}
	case host.ConfigScopeProject:
		slots = []slot{
			{"config", "Main config", config.ProjectPath(workDir)},
			{"mcp", "MCP servers", config.ProjectMCPFilePath(workDir)},
			{"providers", "Providers", config.ProjectProvidersFilePath(workDir)},
			{"keybinds", "Keybinds", config.ProjectKeybindsFilePath(workDir)},
		}
	}
	out := make([]host.ConfigFileRef, 0, len(slots))
	for _, s := range slots {
		path := s.path
		if path == "" && root != "" {
			// firstExisting returns jsonc preference when missing; rebuild.
			path = preferredCreatePath(root, s.id)
		}
		if path == "" {
			continue
		}
		exists := fileExists(path)
		out = append(out, host.ConfigFileRef{
			Slot:      s.id,
			Scope:     scope,
			Label:     s.label,
			Path:      path,
			Display:   displayConfigPath(scope, path, workDir),
			Exists:    exists,
			CanCreate: true,
		})
	}
	return out
}

func preferredCreatePath(root, slot string) string {
	switch slot {
	case "config":
		return filepath.Join(root, "config")
	case "mcp":
		return filepath.Join(root, "mcp.jsonc")
	case "providers":
		return filepath.Join(root, "providers.jsonc")
	case "keybinds":
		return filepath.Join(root, "keybinds.jsonc")
	default:
		return ""
	}
}

func listExtraRefs(scope host.ConfigFileScope, root, workDir, kind, pattern string) []host.ConfigFileRef {
	dir := filepath.Join(root, kind)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		switch kind {
		case "themes":
			// Theme loader accepts JSON theme files.
			if !strings.HasSuffix(strings.ToLower(name), ".json") {
				continue
			}
		case "agents", "skills":
			if !strings.HasSuffix(strings.ToLower(name), ".md") {
				continue
			}
		case "workflows":
			if !strings.HasSuffix(strings.ToLower(name), ".json") {
				continue
			}
		}
		// pattern is informational; filters above match the globs in the issue.
		_ = pattern
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]host.ConfigFileRef, 0, len(names))
	for _, name := range names {
		path := filepath.Join(dir, name)
		label := kind + "/" + name
		out = append(out, host.ConfigFileRef{
			Kind:      kind,
			Scope:     scope,
			Label:     label,
			Path:      path,
			Display:   displayConfigPath(scope, path, workDir),
			Exists:    true,
			CanCreate: false,
		})
	}
	return out
}

func displayConfigPath(scope host.ConfigFileScope, abs, workDir string) string {
	if abs == "" {
		return ""
	}
	switch scope {
	case host.ConfigScopeGlobal:
		if root := config.GlobalRoot(); root != "" {
			if rel, err := filepath.Rel(root, abs); err == nil && !strings.HasPrefix(rel, "..") {
				return filepath.ToSlash(filepath.Join("~/.strike", rel))
			}
		}
		return abs
	case host.ConfigScopeProject:
		if workDir != "" {
			proj := filepath.Join(workDir, ".strike")
			if resolved, err := filepath.EvalSymlinks(proj); err == nil {
				proj = resolved
			}
			if rel, err := filepath.Rel(proj, abs); err == nil && !strings.HasPrefix(rel, "..") {
				return filepath.ToSlash(filepath.Join("./.strike", rel))
			}
			if rel, err := filepath.Rel(workDir, abs); err == nil && !strings.HasPrefix(rel, "..") {
				return filepath.ToSlash(filepath.Join(".", rel))
			}
		}
		return abs
	default:
		return abs
	}
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

func (configFilesAdapter) Ensure(ref host.ConfigFileRef) (path string, created bool, err error) {
	path = strings.TrimSpace(ref.Path)
	if path == "" {
		return "", false, fmt.Errorf("empty config path")
	}
	if !ref.CanCreate {
		if !fileExists(path) {
			return "", false, fmt.Errorf("%s does not exist", ref.Display)
		}
		return path, false, nil
	}
	if err := assertUnderStrikeRoot(path, string(ref.Scope), ""); err != nil {
		// Scope-only check needs workDir for project; re-check with path parents.
		if err2 := assertPathUnderKnownRoots(path); err2 != nil {
			return "", false, err2
		}
	}
	if fileExists(path) {
		return path, false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", false, fmt.Errorf("create directory: %w", err)
	}
	body := stubBodyFor(ref)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return "", false, fmt.Errorf("create %s: %w", path, err)
	}
	return path, true, nil
}

func stubBodyFor(ref host.ConfigFileRef) string {
	switch ref.Slot {
	case "config":
		if ref.Scope == host.ConfigScopeGlobal {
			return defaultGlobalConfigStub
		}
		return emptyJSONObjectStub
	case "mcp", "providers", "keybinds":
		return emptyJSONObjectStub
	default:
		return emptyJSONObjectStub
	}
}

// assertPathUnderKnownRoots rejects path escape outside global/project .strike.
func assertPathUnderKnownRoots(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(filepath.Dir(abs)); err == nil {
		// Prefer resolved parent when it exists.
		_ = resolved
	}
	if root := config.GlobalRoot(); root != "" {
		if underRoot(abs, root) {
			return nil
		}
	}
	// Project: path must contain "/.strike/" or end with "/.strike/…" segment.
	clean := filepath.Clean(abs)
	sep := string(os.PathSeparator)
	marker := sep + ".strike" + sep
	if strings.Contains(clean, marker) || strings.HasSuffix(clean, sep+".strike") {
		// Still reject auth.json and other forbidden basenames.
		base := filepath.Base(clean)
		if forbiddenConfigBase(base) {
			return fmt.Errorf("path not allowed: %s", base)
		}
		return nil
	}
	return fmt.Errorf("path outside .strike roots")
}

func assertUnderStrikeRoot(path, scope, workDir string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	var root string
	switch host.ConfigFileScope(scope) {
	case host.ConfigScopeGlobal:
		root = config.GlobalRoot()
	case host.ConfigScopeProject:
		if workDir == "" {
			return assertPathUnderKnownRoots(path)
		}
		root = filepath.Join(workDir, ".strike")
		if resolved, err := filepath.EvalSymlinks(root); err == nil {
			root = resolved
		}
	default:
		return assertPathUnderKnownRoots(path)
	}
	if root == "" {
		return fmt.Errorf("cannot resolve .strike root")
	}
	if !underRoot(abs, root) {
		return fmt.Errorf("path outside .strike root")
	}
	if forbiddenConfigBase(filepath.Base(abs)) {
		return fmt.Errorf("path not allowed: %s", filepath.Base(abs))
	}
	return nil
}

func underRoot(abs, root string) bool {
	abs = filepath.Clean(abs)
	root = filepath.Clean(root)
	if abs == root {
		return true
	}
	sep := string(os.PathSeparator)
	return strings.HasPrefix(abs, root+sep)
}

func forbiddenConfigBase(base string) bool {
	switch strings.ToLower(base) {
	case "auth.json", "onboarding.json":
		return true
	default:
		return false
	}
}

func (configFilesAdapter) LoadKeybinds(workDir string) (map[string][]string, error) {
	// Merge global then project dedicated keybinds files (same order as Load).
	merged := map[string]config.KeybindChords{}
	if root := config.GlobalRoot(); root != "" {
		binds, err := loadKeybindsDir(root)
		if err != nil {
			return nil, err
		}
		merged = config.MergeKeybinds(merged, binds)
	}
	if workDir != "" {
		root := filepath.Join(workDir, ".strike")
		if resolved, err := filepath.EvalSymlinks(root); err == nil {
			root = resolved
		}
		binds, err := loadKeybindsDir(root)
		if err != nil {
			return nil, err
		}
		merged = config.MergeKeybinds(merged, binds)
	}
	return config.KeybindsMap(merged), nil
}

func loadKeybindsDir(dir string) (map[string]config.KeybindChords, error) {
	for _, name := range []string{"keybinds.jsonc", "keybinds.json"} {
		path := filepath.Join(dir, name)
		binds, err := config.ReadKeybindsFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		return binds, nil
	}
	return nil, nil
}
