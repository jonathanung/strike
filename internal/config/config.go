// Package config loads strike configuration and user extensions. Layers:
// defaults, then global (~/.strike/config), then project (./.strike/config)
// — all JSON. Scalar fields override; permission rules concatenate so later
// layers win under last-match-wins evaluation. The same two .strike roots
// also hold agents/ and skills/ folders (see agents.go). Loaded by
// cmd/strike at startup and wrapped by internal/host/local (Settings, and
// the agent/skill listings); internal/tui never imports it directly.
package config

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/jonathanung/strike-cli/internal/permission"
	"github.com/jonathanung/strike-cli/internal/protocol"
)

type Config struct {
	Provider     string             `json:"provider,omitempty"`
	Model        string             `json:"model,omitempty"`
	Effort       protocol.Effort    `json:"effort,omitempty"`
	SystemPrompt string             `json:"systemPrompt,omitempty"`
	DefaultAgent string             `json:"defaultAgent,omitempty"`
	Permissions  permission.Ruleset `json:"permissions,omitempty"`
}

func Default() Config {
	return Config{
		Provider: "anthropic",
	}
}

// DefaultModel is used when neither config nor flags set a model.
func DefaultModel(provider string) string {
	switch provider {
	case "openai":
		return "gpt-5.5"
	case "xai":
		return "grok-4.5"
	default:
		return "claude-sonnet-5"
	}
}

// GlobalRoot is ~/.strike — strike's home for all user-level state.
func GlobalRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".strike")
}

// GlobalPath is the global config file, ~/.strike/config (JSON).
func GlobalPath() string {
	root := GlobalRoot()
	if root == "" {
		return ""
	}
	return filepath.Join(root, "config")
}

// projectRoot is the per-project .strike directory.
func projectRoot(workDir string) string {
	return filepath.Join(workDir, ".strike")
}

// Load merges default <- global <- project config.
func Load(workDir string) (Config, error) {
	cfg := Default()
	for _, path := range []string{GlobalPath(), filepath.Join(projectRoot(workDir), "config")} {
		if path == "" {
			continue
		}
		layer, err := read(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return cfg, err
		}
		cfg = merge(cfg, layer)
	}
	return cfg, nil
}

func read(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return Config{}, err
	}
	return c, nil
}

func merge(base, layer Config) Config {
	if layer.Provider != "" {
		base.Provider = layer.Provider
	}
	if layer.Model != "" {
		base.Model = layer.Model
	}
	if layer.Effort != "" {
		base.Effort = layer.Effort
	}
	if layer.SystemPrompt != "" {
		base.SystemPrompt = layer.SystemPrompt
	}
	if layer.DefaultAgent != "" {
		base.DefaultAgent = layer.DefaultAgent
	}
	base.Permissions = append(base.Permissions, layer.Permissions...)
	return base
}
