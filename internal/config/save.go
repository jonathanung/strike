package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

// SetGlobalDefaults persists non-empty fields into ~/.strike/config,
// creating it if needed. Fields passed as "" are left unchanged, and
// unrelated config (permissions, systemPrompt) is preserved.
func SetGlobalDefaults(provider, model, agent string, effort protocol.Effort) error {
	cfg, err := readGlobalForWrite()
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
			return fmt.Errorf("unknown effort %q", effort)
		}
		cfg.Effort = parsed
	}
	return writeGlobal(cfg)
}

// SetGlobalTheme persists the preferred TUI theme id into ~/.strike/config.
func SetGlobalTheme(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("theme id is required")
	}
	cfg, err := readGlobalForWrite()
	if err != nil {
		return err
	}
	cfg.Theme = id
	return writeGlobal(cfg)
}

func readGlobalForWrite() (Config, error) {
	path := GlobalPath()
	if path == "" {
		return Config{}, fmt.Errorf("cannot locate home directory")
	}
	var cfg Config
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return cfg, nil
	case err != nil:
		return Config{}, err
	default:
		if err := json.Unmarshal(data, &cfg); err != nil {
			return Config{}, fmt.Errorf("existing %s is not valid JSON (%v) — fix it before saving defaults", path, err)
		}
		return cfg, nil
	}
}

func writeGlobal(cfg Config) error {
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
	return os.WriteFile(path, append(out, '\n'), 0o644)
}
