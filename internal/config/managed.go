package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/jonathanung/strike-cli/internal/permission"
)

// Managed env overrides (tests / custom deploy roots). When set, system
// defaults are ignored and only this root is consulted.
const envManagedRoot = "STRIKE_MANAGED_ROOT"

// ManagedInfo describes the enterprise/MDM config layer applied after global
// and project. Empty Path means no managed file was loaded. Lock flags are
// true when the managed layer set the corresponding security dial so callers
// (CLI, session resume, /mode) must not loosen them.
//
// Permissions holds the managed ruleset (also merged into Config.Permissions
// for effective dumps). DenyRules is the subset used as a late evaluation
// ceiling so session grants, --dangerously-skip-permissions, and phase
// widens cannot override managed denies.
type ManagedInfo struct {
	// Path is the primary managed-config file that contributed (or the
	// drop-in directory when only drop-ins exist). Empty if none loaded.
	Path string `json:"-"`
	// Sources lists every managed file merged, in order.
	Sources []string `json:"-"`

	PermissionMode   bool `json:"-"`
	Sandbox          bool `json:"-"`
	PermissionPreset bool `json:"-"`
	// ContentGuard is true when managed set contentGuard.mode (lock dial).
	ContentGuard bool `json:"-"`
	// ContentGuardForcedDeny is true when managed contentGuard.mode is deny
	// (write guards cannot be widened by project/session/yolo).
	ContentGuardForcedDeny bool `json:"-"`
	// Permissions is true when managed contributed any permission rules.
	Permissions bool `json:"-"`

	// Rules is a defensive copy of managed permissions[] (all actions).
	Rules permission.Ruleset `json:"-"`
	// DenyRules is Rules filtered to action=deny (late ceiling).
	DenyRules permission.Ruleset `json:"-"`
}

// Active reports whether any managed config file was applied.
func (m ManagedInfo) Active() bool {
	return m.Path != "" || len(m.Sources) > 0
}

// ManagedRoot returns the system directory for enterprise managed config.
// Override with STRIKE_MANAGED_ROOT (absolute or relative directory).
//
// Default paths (file-based MDM, Claude Code–compatible layout):
//
//	Linux / other Unix: /etc/strike
//	macOS:              /Library/Application Support/Strike
//	Windows:            C:\Program Files\Strike
//
// Files consulted under the root (see LoadManaged):
//
//	managed-config.json / managed-config.jsonc
//	managed-config.d/*.json(c)  — sorted alphabetically, merged after the base file
func ManagedRoot() string {
	if root := strings.TrimSpace(os.Getenv(envManagedRoot)); root != "" {
		return resolveExisting(root)
	}
	return defaultManagedRoot()
}

func defaultManagedRoot() string {
	switch runtime.GOOS {
	case "darwin":
		return "/Library/Application Support/Strike"
	case "windows":
		// Prefer Program Files (Claude Code–style). ProgramData is not used.
		if pf := os.Getenv("ProgramFiles"); pf != "" {
			return filepath.Join(pf, "Strike")
		}
		return `C:\Program Files\Strike`
	default:
		// linux, freebsd, openbsd, …
		return "/etc/strike"
	}
}

// ManagedConfigPath is the primary managed config file under ManagedRoot
// (managed-config without extension; read() tries bare path).
func ManagedConfigPath() string {
	root := ManagedRoot()
	if root == "" {
		return ""
	}
	return filepath.Join(root, "managed-config")
}

// ManagedDropInDir is ManagedRoot/managed-config.d for fragment policies.
func ManagedDropInDir() string {
	root := ManagedRoot()
	if root == "" {
		return ""
	}
	return filepath.Join(root, "managed-config.d")
}

// managedFileCandidates returns paths to try for a stem (bare, .json, .jsonc).
func managedFileCandidates(stem string) []string {
	if stem == "" {
		return nil
	}
	// Prefer extensionless then jsonc then json (same idea as keybinds/mcp).
	return []string{stem, stem + ".jsonc", stem + ".json"}
}

// LoadManaged reads and merges the managed config layer. Missing root or
// files are not an error (returns zero Config + empty ManagedInfo).
// Parse/validation errors fail closed so a broken MDM push is visible.
func LoadManaged() (Config, ManagedInfo, error) {
	var info ManagedInfo
	cfg := Config{}

	// Base file: first existing candidate wins (not merge across extensions).
	baseStem := ManagedConfigPath()
	var baseLayer Config
	var basePath string
	var foundBase bool
	for _, p := range managedFileCandidates(baseStem) {
		layer, err := read(p)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			continue
		case err != nil:
			return Config{}, ManagedInfo{}, err
		default:
			baseLayer = layer
			basePath = p
			foundBase = true
		}
		break
	}
	if foundBase {
		cfg = merge(cfg, baseLayer)
		info.Sources = append(info.Sources, basePath)
		info.Path = basePath
		stampManagedLocks(&info, baseLayer)
	}

	// Drop-ins: all *.json / *.jsonc, sorted by base name, merged in order.
	dropDir := ManagedDropInDir()
	dropIns, err := listManagedDropIns(dropDir)
	if err != nil {
		return Config{}, ManagedInfo{}, err
	}
	for _, p := range dropIns {
		layer, err := read(p)
		if err != nil {
			return Config{}, ManagedInfo{}, fmt.Errorf("managed drop-in %s: %w", p, err)
		}
		cfg = merge(cfg, layer)
		info.Sources = append(info.Sources, p)
		if info.Path == "" {
			info.Path = p
		}
		stampManagedLocks(&info, layer)
	}

	if len(cfg.Permissions) > 0 {
		info.Rules = append(permission.Ruleset(nil), cfg.Permissions...)
		info.DenyRules = managedDenyOnly(cfg.Permissions)
		info.Permissions = true
	}
	return cfg, info, nil
}

func stampManagedLocks(info *ManagedInfo, layer Config) {
	if layer.PermissionMode != "" {
		info.PermissionMode = true
	}
	if strings.TrimSpace(layer.Sandbox) != "" {
		info.Sandbox = true
	}
	if strings.TrimSpace(layer.PermissionPreset) != "" {
		info.PermissionPreset = true
	}
	if strings.TrimSpace(layer.ContentGuard.Mode) != "" {
		info.ContentGuard = true
		if strings.EqualFold(strings.TrimSpace(layer.ContentGuard.Mode), "deny") {
			info.ContentGuardForcedDeny = true
		}
	}
	if len(layer.Permissions) > 0 {
		info.Permissions = true
	}
}

func managedDenyOnly(rs permission.Ruleset) permission.Ruleset {
	if len(rs) == 0 {
		return nil
	}
	out := make(permission.Ruleset, 0, len(rs))
	for _, r := range rs {
		if r.Action == permission.Deny {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// listManagedDropIns returns sorted *.json / *.jsonc paths under dir.
// Missing dir is empty. Hidden files (leading .) are ignored.
func listManagedDropIns(dir string) ([]string, error) {
	if dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		lower := strings.ToLower(name)
		if !strings.HasSuffix(lower, ".json") && !strings.HasSuffix(lower, ".jsonc") {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]string, 0, len(names))
	for _, name := range names {
		out = append(out, filepath.Join(dir, name))
	}
	return out, nil
}
