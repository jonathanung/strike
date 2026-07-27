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
		cfg.Provider = provider
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
