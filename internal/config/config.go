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
	"strings"

	"github.com/jonathanung/strike-cli/internal/permission"
	"github.com/jonathanung/strike-cli/internal/protocol"
)

type Config struct {
	Provider     string          `json:"provider,omitempty"`
	Model        string          `json:"model,omitempty"`
	Effort       protocol.Effort `json:"effort,omitempty"`
	SystemPrompt string          `json:"systemPrompt,omitempty"`
	DefaultAgent string          `json:"defaultAgent,omitempty"`
	// Theme is the preferred TUI color theme id (bundled or JSON under
	// ~/.strike/themes or ./.strike/themes). Empty means the stock "strike"
	// palette.
	Theme string `json:"theme,omitempty"`
	// VimMode is how /vim presents the editor: "pane" (default, embedded
	// right-pane PTY), "overlay" (embedded modal), or "takeover" (full-screen
	// tea.ExecProcess handoff). Unknown values are ignored at load time.
	VimMode     string             `json:"vimMode,omitempty"`
	Permissions permission.Ruleset `json:"permissions,omitempty"`
	// Hooks mixes declarative rules (action) and shell commands (command).
	// Global then project layers concatenate. Invalid entries are dropped.
	Hooks []Hook `json:"hooks,omitempty"`
	// Providers are user-declared custom/self-hosted endpoints (name, base
	// URL, wire api). API keys are never stored here — only in auth.json.
	Providers []CustomProvider `json:"providers,omitempty"`
	// CompactionStrategy is "trim" (default: drop older turns) or "summarize"
	// (replace dropped turns with a model-authored summary). Unknown values
	// are ignored at load time.
	CompactionStrategy string `json:"compactionStrategy,omitempty"`
	// CompactionModel optionally pins the model id used for summarize
	// compaction (same provider as the session). Empty uses the session model.
	CompactionModel string `json:"compactionModel,omitempty"`
}

// Hook is one lifecycle hook entry. Exactly one of Action or Command should
// be set:
//   - Action (log|block|notify): declarative rule evaluated in-process
//   - Command: shell hook (event JSON on stdin; exit allow/block; stdout inject)
type Hook struct {
	// Event is pre_tool_use, post_tool_use, turn_start, or turn_end.
	// Shell hooks only run for pre_tool_use / post_tool_use.
	Event string `json:"event"`
	// Matcher is a doublestar glob over the tool name; empty matches all.
	Matcher string `json:"matcher,omitempty"`
	// Action is log, block, or notify (declarative). Mutually exclusive with Command.
	Action string `json:"action,omitempty"`
	// Message is optional text for block/notify declarative rules.
	Message string `json:"message,omitempty"`
	// Command runs via bash -c with the event payload on stdin (shell hook).
	Command string `json:"command,omitempty"`
	// TimeoutMs bounds shell execution (default 30000, max 120000).
	TimeoutMs int `json:"timeoutMs,omitempty"`
}

// IsShell reports a shell-command hook (has command, no action).
func (h Hook) IsShell() bool {
	return strings.TrimSpace(h.Command) != "" && strings.TrimSpace(h.Action) == ""
}

// IsRule reports a declarative action hook (has action, no command).
func (h Hook) IsRule() bool {
	return strings.TrimSpace(h.Action) != "" && strings.TrimSpace(h.Command) == ""
}

// ShellHooks returns shell-command entries for the engine.
func (c Config) ShellHooks() []Hook {
	out := make([]Hook, 0, len(c.Hooks))
	for _, h := range c.Hooks {
		if h.IsShell() {
			out = append(out, h)
		}
	}
	return out
}

// HookRules returns declarative rules for the permission/engine evaluator.
func (c Config) HookRules() permission.HookRuleset {
	out := make(permission.HookRuleset, 0, len(c.Hooks))
	for _, h := range c.Hooks {
		if !h.IsRule() {
			continue
		}
		out = append(out, permission.HookRule{
			Event:   h.Event,
			Matcher: h.Matcher,
			Action:  h.Action,
			Message: h.Message,
		})
	}
	return out
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
	// Drop invalid hooks (neither shell nor rule, or bad declarative fields).
	if len(c.Hooks) > 0 {
		valid := make([]Hook, 0, len(c.Hooks))
		for _, h := range c.Hooks {
			switch {
			case h.IsShell():
				// Shell hooks only fire on tool events; keep any event string
				// and let the engine matcher filter.
				if strings.TrimSpace(h.Event) == "" {
					continue
				}
				valid = append(valid, h)
			case h.IsRule():
				rule := permission.HookRule{
					Event:   h.Event,
					Matcher: h.Matcher,
					Action:  h.Action,
					Message: h.Message,
				}
				if err := permission.ValidateHookRule(rule); err != nil {
					continue
				}
				valid = append(valid, h)
			default:
				// Both action+command or neither: drop.
			}
		}
		c.Hooks = valid
	}
	c.CompactionStrategy = NormalizeCompactionStrategy(c.CompactionStrategy)
	c.CompactionModel = strings.TrimSpace(c.CompactionModel)
	return c, nil
}

// NormalizeCompactionStrategy maps config aliases to trim|summarize.
// Empty and unknown values become "" (engine default = trim).
func NormalizeCompactionStrategy(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "trim":
		return "trim"
	case "summarize", "summary":
		return "summarize"
	default:
		return ""
	}
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
	if layer.Theme != "" {
		base.Theme = layer.Theme
	}
	if layer.VimMode != "" {
		base.VimMode = layer.VimMode
	}
	if layer.CompactionStrategy != "" {
		base.CompactionStrategy = layer.CompactionStrategy
	}
	if layer.CompactionModel != "" {
		base.CompactionModel = layer.CompactionModel
	}
	base.Permissions = append(base.Permissions, layer.Permissions...)
	base.Hooks = append(base.Hooks, layer.Hooks...)
	base.Providers = mergeProviders(base.Providers, layer.Providers)
	return base
}
