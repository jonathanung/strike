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
	Provider     string          `json:"provider,omitempty"`
	Model        string          `json:"model,omitempty"`
	Effort       protocol.Effort `json:"effort,omitempty"`
	SystemPrompt string          `json:"systemPrompt,omitempty"`
	DefaultAgent string          `json:"defaultAgent,omitempty"`
	// VimMode is how /vim presents the editor: "pane" (default, embedded
	// right-pane PTY), "overlay" (embedded modal), or "takeover" (full-screen
	// tea.ExecProcess handoff). Unknown values are ignored at load time.
	VimMode     string             `json:"vimMode,omitempty"`
	Permissions permission.Ruleset `json:"permissions,omitempty"`
	// Providers are user-declared custom/self-hosted endpoints (name, base
	// URL, wire api). API keys are never stored here — only in auth.json.
	Providers []CustomProvider `json:"providers,omitempty"`
}

func Default() Config {
	return Config{
		Provider: "anthropic",
	}
}

// DefaultModel is used when neither config nor flags set a model.
// For custom providers, prefer CustomProvider.Models[0] via DefaultModelCustom.
func DefaultModel(provider string) string {
	switch provider {
	case "openai":
		return "gpt-5.5"
	case "xai":
		return "grok-4.5"
	case "echo":
		return "echo"
	default:
		return "claude-sonnet-5"
	}
}

// DefaultModelCustom returns the first configured model id for a custom
// provider, or empty when none are listed (caller may leave model unset).
func DefaultModelCustom(p CustomProvider) string {
	if len(p.Models) > 0 {
		return p.Models[0]
	}
	return ""
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
	// Drop invalid custom providers at load so a bad row does not brick startup.
	if len(c.Providers) > 0 {
		valid := make([]CustomProvider, 0, len(c.Providers))
		for _, p := range c.Providers {
			p = NormalizeCustomProvider(p)
			if err := p.Validate(); err != nil {
				continue
			}
			valid = append(valid, p)
		}
		c.Providers = valid
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
	if layer.VimMode != "" {
		base.VimMode = layer.VimMode
	}
	base.Permissions = append(base.Permissions, layer.Permissions...)
	base.Providers = mergeProviders(base.Providers, layer.Providers)
	return base
}
