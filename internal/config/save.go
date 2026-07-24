package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

// SetGlobalDefaults persists non-empty fields into ~/.strike/config,
// creating it if needed. Fields passed as "" are left unchanged, and
// unrelated config (permissions, systemPrompt) is preserved.
func SetGlobalDefaults(provider, model, agent string, effort protocol.Effort) error {
	path := GlobalPath()
	if path == "" {
		return fmt.Errorf("cannot locate home directory")
	}
	var cfg Config
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// fresh config
	case err != nil:
		return err
	default:
		// A corrupt config should surface, not be silently clobbered.
		if err := json.Unmarshal(data, &cfg); err != nil {
			return fmt.Errorf("existing %s is not valid JSON (%v) — fix it before saving defaults", path, err)
		}
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
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0o644)
}
