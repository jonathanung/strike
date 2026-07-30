package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/jonathanung/strike-cli/internal/permission"
	"github.com/jonathanung/strike-cli/internal/protocol"
)

var globalMu sync.Mutex

// SetGlobalDefaults persists non-empty fields into ~/.strike/config,
// creating it if needed. Fields passed as "" are left unchanged, and
// unrelated config (permissions, systemPrompt) is preserved.
// mode is a permissionMode string (default|plan|soft-approve|accept-edits|yolo);
// empty leaves the stored default unchanged.
func SetGlobalDefaults(provider, model, agent string, effort protocol.Effort, mode string) error {
	globalMu.Lock()
	defer globalMu.Unlock()

	cfg, unlock, err := readGlobalForWrite()
	if err != nil {
		return err
	}
	if provider != "" {
		cfg.Provider = CanonicalProviderID(provider)
	}
	if model != "" {
		cfg.Model = model
	}
	if agent != "" {
		cfg.DefaultAgent = agent
	}
	if effort != "" {
		parsed, ok := protocol.ParseEffort(string(effort))
		if !ok {
			unlock()
			return fmt.Errorf("unknown effort %q", effort)
		}
		cfg.Effort = parsed
	}
	if mode != "" {
		parsed, ok := protocol.ParsePermissionMode(mode)
		if !ok {
			unlock()
			return fmt.Errorf("unknown permission mode %q", mode)
		}
		cfg.PermissionMode = parsed
	}
	return writeGlobal(cfg, unlock)
}

// SetGlobalTheme persists the preferred TUI theme id into ~/.strike/config.
func SetGlobalTheme(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("theme id is required")
	}
	globalMu.Lock()
	defer globalMu.Unlock()

	cfg, unlock, err := readGlobalForWrite()
	if err != nil {
		return err
	}
	cfg.Theme = id
	return writeGlobal(cfg, unlock)
}

// SetGlobalKeybinds persists overrides into ~/.strike/keybinds.jsonc, merging
// with any existing binds in that file. Pass nil to delete the file entirely
// (reset to defaults). Unknown ids from the new binds are silently dropped;
// unknown ids already in the file survive round-trip.
func SetGlobalKeybinds(binds map[string]KeybindChords) error {
	globalMu.Lock()
	defer globalMu.Unlock()

	path := keybindsWritePath()
	if path == "" {
		return fmt.Errorf("cannot locate home directory")
	}

	if len(binds) == 0 {
		return deleteKeybindsFile(path)
	}

	existing, err := readKeybindsRelaxed(path)
	if err != nil {
		return err
	}
	if existing == nil {
		existing = make(map[string]KeybindChords)
	}
	// Overwrite only known ids; unknown ids in the file survive.
	for id, chords := range binds {
		if _, ok := KnownKeybindIDs[id]; ok {
			existing[id] = append(KeybindChords(nil), chords...)
		}
	}
	return writeKeybindsFile(path, existing)
}

func keybindsWritePath() string {
	root := GlobalRoot()
	if root == "" {
		return ""
	}
	return filepath.Join(root, "keybinds.jsonc")
}

// readKeybindsRelaxed reads the file and returns all keybind entries, including
// any with unknown ids (they survive round-trip). Missing file → nil, nil.
func readKeybindsRelaxed(path string) (map[string]KeybindChords, error) {
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("read keybinds file: %w", err)
	}
	stripped, err := stripJSONC(data)
	if err != nil {
		return nil, err
	}
	stripped = bytesTrimSpace(stripped)
	if len(stripped) == 0 || stripped[0] != '{' {
		return nil, fmt.Errorf("keybinds file must be a JSON object")
	}
	// Accept wrapped {"keybinds":{...}} or flat.
	var wrapped struct {
		Keybinds map[string]KeybindChords `json:"keybinds"`
	}
	if err := json.Unmarshal(stripped, &wrapped); err != nil {
		return nil, err
	}
	if wrapped.Keybinds != nil {
		return wrapped.Keybinds, nil
	}
	var flat map[string]KeybindChords
	if err := json.Unmarshal(stripped, &flat); err != nil {
		return nil, err
	}
	return flat, nil
}

func deleteKeybindsFile(path string) error {
	err := os.Remove(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func writeKeybindsFile(path string, binds map[string]KeybindChords) error {
	out, err := json.MarshalIndent(binds, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal keybinds: %w", err)
	}
	payload := append(out, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".keybinds-")
	if err != nil {
		return fmt.Errorf("create temp keybinds: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(payload); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp keybinds: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("fsync temp keybinds: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp keybinds: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename temp keybinds: %w", err)
	}
	if dirFd, err := os.Open(dir); err == nil {
		dirFd.Sync()
		dirFd.Close()
	}
	return nil
}

// SetGlobalPresentation persists non-empty editor/reader presentation modes
// into ~/.strike/config. Empty fields are left unchanged. Values match config
// keys vimMode/nanoMode/mdReadMode (pane|embedded|overlay|modal|takeover).
func SetGlobalPresentation(vimMode, nanoMode, mdReadMode string) error {
	globalMu.Lock()
	defer globalMu.Unlock()

	cfg, unlock, err := readGlobalForWrite()
	if err != nil {
		return err
	}
	if vimMode != "" {
		if !validEditorMode(vimMode) {
			unlock()
			return fmt.Errorf("unknown vimMode %q (want pane|embedded|overlay|modal|takeover)", vimMode)
		}
		cfg.VimMode = normalizeEditorMode(vimMode)
	}
	if nanoMode != "" {
		if !validEditorMode(nanoMode) {
			unlock()
			return fmt.Errorf("unknown nanoMode %q (want pane|embedded|overlay|modal|takeover)", nanoMode)
		}
		cfg.NanoMode = normalizeEditorMode(nanoMode)
	}
	if mdReadMode != "" {
		if !validMdReadMode(mdReadMode) {
			unlock()
			return fmt.Errorf("unknown mdReadMode %q (want embedded|pane|modal|overlay)", mdReadMode)
		}
		cfg.MdReadMode = normalizeMdReadMode(mdReadMode)
	}
	return writeGlobal(cfg, unlock)
}

// ReadGlobalDefaults returns the global config file contents used as user
// defaults. Missing file yields a zero Config and nil error.
func ReadGlobalDefaults() (Config, error) {
	globalMu.Lock()
	defer globalMu.Unlock()

	path := GlobalPath()
	if path == "" {
		return Config{}, fmt.Errorf("cannot locate home directory")
	}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return Config{}, nil
	case err != nil:
		return Config{}, err
	case len(data) == 0:
		return Config{}, nil
	default:
		var cfg Config
		if err := json.Unmarshal(data, &cfg); err != nil {
			return Config{}, fmt.Errorf("%s is not valid JSON: %w", path, err)
		}
		return cfg, nil
	}
}

func validEditorMode(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "pane", "embedded", "overlay", "modal", "takeover":
		return true
	default:
		return false
	}
}

// normalizeEditorMode stores canonical pane|overlay|takeover (aliases collapse).
func normalizeEditorMode(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "embedded", "pane":
		return "pane"
	case "modal", "overlay":
		return "overlay"
	case "takeover":
		return "takeover"
	default:
		return strings.ToLower(strings.TrimSpace(v))
	}
}

func validMdReadMode(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "embedded", "pane", "modal", "overlay":
		return true
	default:
		return false
	}
}

func normalizeMdReadMode(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "pane", "embedded":
		return "embedded"
	case "overlay", "modal":
		return "modal"
	default:
		return strings.ToLower(strings.TrimSpace(v))
	}
}

func readGlobalForWrite() (Config, func() error, error) {
	path := GlobalPath()
	if path == "" {
		return Config{}, nil, fmt.Errorf("cannot locate home directory")
	}
	unlock, err := lockGlobalFile(path)
	if err != nil {
		return Config{}, nil, err
	}
	var readErr error
	defer func() {
		if readErr != nil {
			unlock()
		}
	}()
	var cfg Config
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return cfg, unlock, nil
	case err != nil:
		readErr = err
		return Config{}, nil, err
	case len(data) == 0:
		// Lock created the file; treat empty as not existing.
		return cfg, unlock, nil
	default:
		if err := json.Unmarshal(data, &cfg); err != nil {
			readErr = fmt.Errorf("existing %s is not valid JSON (%v) — fix it before saving defaults", path, err)
			return Config{}, nil, readErr
		}
		return cfg, unlock, nil
	}
}

func writeGlobal(cfg Config, unlock func() error) error {
	defer unlock()
	path := GlobalPath()
	if path == "" {
		return fmt.Errorf("cannot locate home directory")
	}
	// If config itself is a file symlink (stow/dotfiles), write the referent
	// so atomic rename does not replace the symlink node with a plain file.
	path, err := resolveWritePath(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	payload := append(out, '\n')
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".config-")
	if err != nil {
		return fmt.Errorf("create temp config: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(payload); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("fsync temp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp config: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename temp config: %w", err)
	}
	// Fsync the directory so the rename is durable.
	if dirFd, err := os.Open(dir); err == nil {
		dirFd.Sync()
		dirFd.Close()
	}
	return nil
}

// resolveWritePath returns path suitable for atomic rename. File symlinks are
// resolved to their referent; missing paths and regular files are unchanged.
func resolveWritePath(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return path, nil
		}
		return "", err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return path, nil
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve config symlink: %w", err)
	}
	return real, nil
}

// ProjectPath is the project config file, <workDir>/.strike/config (JSON).
func ProjectPath(workDir string) string {
	if workDir == "" {
		return ""
	}
	return filepath.Join(projectRoot(workDir), "config")
}

// AppendProjectPermission appends an allow rule to the project config at
// <workDir>/.strike/config, creating the file (and .strike/) if needed.
// Unrelated fields are preserved. Empty permission names are rejected.
func AppendProjectPermission(workDir string, rule permission.Rule) error {
	if workDir == "" {
		return fmt.Errorf("empty work directory")
	}
	if rule.Permission == "" {
		return fmt.Errorf("empty permission name")
	}
	if rule.Action == "" {
		rule.Action = permission.Allow
	}
	if rule.Pattern == "" {
		rule.Pattern = "*"
	}
	path := ProjectPath(workDir)
	var cfg Config
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// fresh project config
	case err != nil:
		return err
	default:
		if err := json.Unmarshal(data, &cfg); err != nil {
			return fmt.Errorf("existing %s is not valid JSON (%v) — fix it before saving permissions", path, err)
		}
	}
	cfg.Permissions = append(cfg.Permissions, rule)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0o644)
}
