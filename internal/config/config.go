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
	"fmt"
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
	VimMode string `json:"vimMode,omitempty"`
	// Notify controls desktop notifications (OSC 9 + bell) for permission/
	// question asks and long turn completion: "on", "off", or
	// "unfocused-only" (default). Unknown values are ignored at load time.
	Notify string `json:"notify,omitempty"`
	// PermissionAutoApproveSeconds enables permission-modal auto-allow once
	// after N seconds (yolo-lite). Zero disables (default). Clamped to 1–60
	// when positive.
	PermissionAutoApproveSeconds int `json:"permissionAutoApproveSeconds,omitempty"`
	// PermissionAutoApproveExclude lists permission names (e.g. "bash") that
	// never auto-approve even when seconds > 0. Compared case-insensitively.
	PermissionAutoApproveExclude []string           `json:"permissionAutoApproveExclude,omitempty"`
	Permissions                  permission.Ruleset `json:"permissions,omitempty"`
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
	// MaxChildDepth bounds nested task tool spawns (root depth 0). Zero means
	// engine default (1: children cannot spawn further tasks).
	MaxChildDepth int `json:"maxChildDepth,omitempty"`
	// Keybinds remaps app-level binding ids to key sequence(s). Ids match the
	// TUI keybind catalog (e.g. "nav.jump-bottom"). Merged last-wins per id
	// across global then project layers. Unknown ids fail Load.
	Keybinds map[string]KeybindChords `json:"keybinds,omitempty"`
	// Session holds per-session runtime preferences (worktree isolation).
	Session SessionConfig `json:"session,omitempty"`
}

// SessionConfig is the JSON "session" object in config.
type SessionConfig struct {
	// Worktree is off (default), auto (second+ concurrent root), or always.
	Worktree string `json:"worktree,omitempty"`
	// WorktreeCleanup is keep (default) or delete on session close.
	WorktreeCleanup string `json:"worktreeCleanup,omitempty"`
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
	c.PermissionAutoApproveSeconds = ClampPermissionAutoApproveSeconds(c.PermissionAutoApproveSeconds)
	c.PermissionAutoApproveExclude = normalizePermissionAutoApproveExclude(c.PermissionAutoApproveExclude)
	c.CompactionStrategy = NormalizeCompactionStrategy(c.CompactionStrategy)
	c.CompactionModel = strings.TrimSpace(c.CompactionModel)
	c.Notify = NormalizeNotify(c.Notify)
	// Keybinds: unknown ids / invalid chords fail the layer (and thus Load).
	if err := ValidateKeybinds(c.Keybinds); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// ClampPermissionAutoApproveSeconds maps config values: ≤0 → 0 (off), >60 → 60.
func ClampPermissionAutoApproveSeconds(n int) int {
	if n <= 0 {
		return 0
	}
	if n > 60 {
		return 60
	}
	return n
}

func normalizePermissionAutoApproveExclude(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, raw := range in {
		name := strings.ToLower(strings.TrimSpace(raw))
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// PermissionAutoApproveExcluded reports whether permission is on the exclude list.
func PermissionAutoApproveExcluded(permission string, exclude []string) bool {
	if len(exclude) == 0 {
		return false
	}
	want := strings.ToLower(strings.TrimSpace(permission))
	for _, name := range exclude {
		if name == want {
			return true
		}
	}
	return false
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

// Notify mode values for Config.Notify / desktop notifications.
const (
	NotifyOn            = "on"
	NotifyOff           = "off"
	NotifyUnfocusedOnly = "unfocused-only"
)

// NormalizeNotify maps config aliases to on|off|unfocused-only.
// Empty and unknown values become "" (TUI default = unfocused-only).
func NormalizeNotify(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "on", "true", "1", "yes", "always":
		return NotifyOn
	case "off", "false", "0", "no", "never":
		return NotifyOff
	case "unfocused-only", "unfocused", "blur":
		return NotifyUnfocusedOnly
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
	if layer.Notify != "" {
		base.Notify = layer.Notify
	}
	if layer.PermissionAutoApproveSeconds != 0 {
		base.PermissionAutoApproveSeconds = layer.PermissionAutoApproveSeconds
	}
	if layer.PermissionAutoApproveExclude != nil {
		base.PermissionAutoApproveExclude = layer.PermissionAutoApproveExclude
	}
	if layer.CompactionStrategy != "" {
		base.CompactionStrategy = layer.CompactionStrategy
	}
	if layer.CompactionModel != "" {
		base.CompactionModel = layer.CompactionModel
	}
	if layer.MaxChildDepth != 0 {
		base.MaxChildDepth = layer.MaxChildDepth
	}
	if layer.Session.Worktree != "" {
		base.Session.Worktree = layer.Session.Worktree
	}
	if layer.Session.WorktreeCleanup != "" {
		base.Session.WorktreeCleanup = layer.Session.WorktreeCleanup
	}
	base.Permissions = append(base.Permissions, layer.Permissions...)
	base.Hooks = append(base.Hooks, layer.Hooks...)
	base.Providers = mergeProviders(base.Providers, layer.Providers)
	base.Keybinds = MergeKeybinds(base.Keybinds, layer.Keybinds)
	return base
}
