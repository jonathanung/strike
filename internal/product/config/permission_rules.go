package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jonathanung/strike/harness/permission"
	"github.com/jonathanung/strike/internal/protocol"
)

// ErrConfirmRequired is returned when a write would weaken a permission
// posture and the caller did not pass confirm. Deny rules are not a ceiling
// inside one file: last-match-wins, so a later allow or ask overrides an
// earlier deny in that file. Removing a deny is the same kind of weaken.
var ErrConfirmRequired = errors.New("confirmation required")

// PermissionRuleRecord is one user-authored rule in the global or project file.
type PermissionRuleRecord struct {
	Scope      string `json:"scope"`
	Index      int    `json:"index"`
	Permission string `json:"permission"`
	Pattern    string `json:"pattern"`
	Action     string `json:"action"`
}

// PermissionRulesView is the editable rule list. It is not the effective
// engine stack: defaults, presets, agent, phase, and managed layers are absent.
type PermissionRulesView struct {
	WorkDir       string                 `json:"workDir,omitempty"`
	GlobalMode    string                 `json:"globalMode"`
	ProjectMode   string                 `json:"projectMode,omitempty"`
	EffectiveMode string                 `json:"effectiveMode"`
	Rules         []PermissionRuleRecord `json:"rules"`
	Note          string                 `json:"note"`
}

const permissionRulesNote = "Deny rules still apply in yolo and accept-edits. Inside one file the last matching rule wins, so a later allow or ask overrides an earlier deny in that file. Managed denies are not listed and cannot be overridden from here."

// ReadPermissionRules lists rules stored in ~/.strike/config and, when workDir
// is set, <workDir>/.strike/config. Missing files are empty, not an error.
func ReadPermissionRules(workDir string) (PermissionRulesView, error) {
	view := PermissionRulesView{
		WorkDir: workDir,
		Rules:   []PermissionRuleRecord{},
		Note:    permissionRulesNote,
	}
	global, err := ReadGlobalDefaults()
	if err != nil {
		return view, err
	}
	if err := validateStoredRulePatterns("global", global.Permissions); err != nil {
		return view, err
	}
	view.GlobalMode = modeOrDefault(global.PermissionMode)
	view.Rules = append(view.Rules, ruleRecords("global", global.Permissions)...)
	view.EffectiveMode = view.GlobalMode
	if workDir == "" {
		return view, nil
	}
	project, err := readProjectFile(workDir)
	if err != nil {
		return view, err
	}
	if err := validateStoredRulePatterns("project", project.Permissions); err != nil {
		return view, err
	}
	if project.PermissionMode != "" {
		view.ProjectMode = modeOrDefault(project.PermissionMode)
		view.EffectiveMode = view.ProjectMode
	}
	view.Rules = append(view.Rules, ruleRecords("project", project.Permissions)...)
	return view, nil
}

func modeOrDefault(mode protocol.PermissionMode) string {
	if mode == "" {
		return string(protocol.PermissionModeDefault)
	}
	return string(mode.Normalize())
}

func ruleRecords(scope string, rules permission.Ruleset) []PermissionRuleRecord {
	out := make([]PermissionRuleRecord, 0, len(rules))
	for i, rule := range rules {
		out = append(out, PermissionRuleRecord{
			Scope:      scope,
			Index:      i,
			Permission: rule.Permission,
			Pattern:    rule.Pattern,
			Action:     string(rule.Action),
		})
	}
	return out
}

// AppendPermissionRule validates and appends one rule. Allow and ask require
// confirm because they can override an earlier deny in the same file.
func AppendPermissionRule(workDir, scope string, rule permission.Rule, confirm bool) error {
	rule, err := normalizeRule(rule)
	if err != nil {
		return err
	}
	if rule.Action != permission.Deny && !confirm {
		return fmt.Errorf("%w to add an %s rule", ErrConfirmRequired, rule.Action)
	}
	switch scope {
	case "global":
		return mutateGlobal(func(cfg *Config) error {
			cfg.Permissions = append(cfg.Permissions, rule)
			return nil
		})
	case "project":
		if strings.TrimSpace(workDir) == "" {
			return fmt.Errorf("project rules need a working directory")
		}
		return mutateProject(workDir, func(cfg *Config) error {
			cfg.Permissions = append(cfg.Permissions, rule)
			return nil
		})
	default:
		return fmt.Errorf("scope must be global or project")
	}
}

// DeletePermissionRule removes the rule at index. Removing a deny requires confirm.
func DeletePermissionRule(workDir, scope string, index int, confirm bool) error {
	if index < 0 {
		return fmt.Errorf("index out of range")
	}
	drop := func(cfg *Config) error {
		if index >= len(cfg.Permissions) {
			return fmt.Errorf("index out of range")
		}
		if cfg.Permissions[index].Action == permission.Deny && !confirm {
			return fmt.Errorf("%w to remove a deny rule", ErrConfirmRequired)
		}
		cfg.Permissions = append(cfg.Permissions[:index], cfg.Permissions[index+1:]...)
		return nil
	}
	switch scope {
	case "global":
		return mutateGlobal(drop)
	case "project":
		if strings.TrimSpace(workDir) == "" {
			return fmt.Errorf("project rules need a working directory")
		}
		return mutateProject(workDir, drop)
	default:
		return fmt.Errorf("scope must be global or project")
	}
}

// validateStoredRulePatterns surfaces a hand-edited rule whose glob syntax
// is invalid. Load (read in config.go) drops such a rule with a stderr log
// so it never reaches evaluation silently inert and never bricks startup;
// this read path is where the editor sees the problem, so it returns the
// error instead — the caller must fix the file, as with invalid JSON.
func validateStoredRulePatterns(scope string, rules permission.Ruleset) error {
	for _, rule := range rules {
		if err := permission.ValidatePattern(rule.Pattern); err != nil {
			return fmt.Errorf("%s permission rule (%s %s): %w — fix it in the config file", scope, rule.Permission, rule.Action, err)
		}
	}
	return nil
}

func normalizeRule(rule permission.Rule) (permission.Rule, error) {
	rule.Permission = strings.TrimSpace(rule.Permission)
	rule.Pattern = strings.TrimSpace(rule.Pattern)
	rule.Action = permission.Action(strings.ToLower(strings.TrimSpace(string(rule.Action))))
	if rule.Pattern == "" {
		return rule, fmt.Errorf("pattern is required")
	}
	if strings.ContainsAny(rule.Pattern, "\n\r\x00") {
		return rule, fmt.Errorf("pattern must be a single line")
	}
	if len(rule.Pattern) > 512 {
		return rule, fmt.Errorf("pattern is too long")
	}
	// Reject invalid glob syntax explicitly: evaluation treats it as
	// never-matching, so storing it would leave the rule silently inert
	// (fail-open for a deny). ValidateRuleset below checks the same thing
	// for non-API callers.
	if err := permission.ValidatePattern(rule.Pattern); err != nil {
		return rule, err
	}
	if err := permission.ValidateRuleset(permission.Ruleset{rule}); err != nil {
		return rule, err
	}
	return rule, nil
}

func mutateGlobal(fn func(*Config) error) error {
	globalMu.Lock()
	defer globalMu.Unlock()
	cfg, unlock, err := readGlobalForWrite()
	if err != nil {
		return err
	}
	if err := fn(&cfg); err != nil {
		unlock()
		return err
	}
	return writeGlobal(cfg, unlock)
}

func mutateProject(workDir string, fn func(*Config) error) error {
	globalMu.Lock()
	defer globalMu.Unlock()
	cfg, err := readProjectFile(workDir)
	if err != nil {
		return err
	}
	if err := fn(&cfg); err != nil {
		return err
	}
	return writeProjectFile(workDir, cfg)
}

func readProjectFile(workDir string) (Config, error) {
	path := ProjectPath(workDir)
	if path == "" {
		return Config{}, fmt.Errorf("empty work directory")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Config{}, nil
		}
		return Config{}, err
	}
	if len(data) == 0 {
		return Config{}, nil
	}
	cfg, err := unmarshalConfigJSONC(data)
	if err != nil {
		return Config{}, fmt.Errorf("existing %s is not valid JSON/JSONC (%v) — fix it before saving permissions", path, err)
	}
	return cfg, nil
}

func writeProjectFile(workDir string, cfg Config) error {
	path := ProjectPath(workDir)
	if path == "" {
		return fmt.Errorf("empty work directory")
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
