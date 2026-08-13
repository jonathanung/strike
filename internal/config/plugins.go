package config

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"

	"github.com/jonathanung/strike-cli/internal/plugin"
)

// pluginCache holds the last Discover result per workDir for a process.
// Loaders (agents/skills/workflows/providers) share one discovery pass.
var (
	pluginMu    sync.Mutex
	pluginCache = map[string]plugin.Result{}
)

// ResetPluginCache clears cached discovery (tests).
func ResetPluginCache() {
	pluginMu.Lock()
	defer pluginMu.Unlock()
	pluginCache = map[string]plugin.Result{}
}

// DiscoverPlugins returns enabled plugins and diagnostics for workDir.
// Results are memoized per workDir for the process lifetime of the cache.
func DiscoverPlugins(workDir string) plugin.Result {
	key := workDir
	pluginMu.Lock()
	if r, ok := pluginCache[key]; ok {
		pluginMu.Unlock()
		return r
	}
	pluginMu.Unlock()

	r := plugin.Discover(plugin.Options{WorkDir: workDir})
	pluginMu.Lock()
	pluginCache[key] = r
	pluginMu.Unlock()
	return r
}

// PluginDiagnostics returns diagnostics from DiscoverPlugins(workDir).
func PluginDiagnostics(workDir string) []plugin.Diagnostic {
	return DiscoverPlugins(workDir).Diagnostics
}

// applyPluginAgentLayer merges only plugins from the given scope, with
// collision diagnostics when overwriting an existing name.
func applyPluginAgentLayer(plugins []plugin.Plugin, scope plugin.Scope, byName map[string]Agent, order *[]string) []plugin.Diagnostic {
	var diags []plugin.Diagnostic
	for _, p := range plugins {
		if p.Source != scope {
			continue
		}
		for _, ref := range p.Agents {
			agent, err := parseAgentFile(ref.AbsPath)
			if err != nil {
				diags = append(diags, pluginDiag(ref, "malformed", err.Error()))
				continue
			}
			if agent == nil {
				diags = append(diags, pluginDiag(ref, "malformed", "empty or invalid agent document"))
				continue
			}
			if _, exists := byName[agent.Name]; exists {
				diags = append(diags, plugin.Diagnostic{
					Severity:  plugin.SeverityWarning,
					Code:      "collision",
					Message:   fmt.Sprintf("agent %q overrides earlier source", agent.Name),
					PluginID:  ref.PluginID,
					Version:   ref.Version,
					Source:    ref.Source,
					Path:      ref.RelPath,
					Collision: agent.Name,
				})
			} else {
				*order = append(*order, agent.Name)
			}
			byName[agent.Name] = *agent
		}
	}
	return diags
}

func applyPluginSkillLayer(plugins []plugin.Plugin, scope plugin.Scope, byName map[string]Skill, order *[]string) []plugin.Diagnostic {
	var diags []plugin.Diagnostic
	for _, p := range plugins {
		if p.Source != scope {
			continue
		}
		for _, ref := range p.Skills {
			skill, err := parseSkillFile(ref.AbsPath)
			if err != nil {
				diags = append(diags, pluginDiag(ref, "malformed", err.Error()))
				continue
			}
			if skill == nil {
				diags = append(diags, pluginDiag(ref, "malformed", "empty or invalid skill document"))
				continue
			}
			if _, exists := byName[skill.Name]; exists {
				diags = append(diags, plugin.Diagnostic{
					Severity:  plugin.SeverityWarning,
					Code:      "collision",
					Message:   fmt.Sprintf("skill %q overrides earlier source", skill.Name),
					PluginID:  ref.PluginID,
					Version:   ref.Version,
					Source:    ref.Source,
					Path:      ref.RelPath,
					Collision: skill.Name,
				})
			} else {
				*order = append(*order, skill.Name)
			}
			byName[skill.Name] = *skill
		}
	}
	return diags
}

func applyPluginWorkflowLayer(plugins []plugin.Plugin, scope plugin.Scope, byName map[string]Workflow, order *[]string) []plugin.Diagnostic {
	var diags []plugin.Diagnostic
	for _, p := range plugins {
		if p.Source != scope {
			continue
		}
		for _, ref := range p.Workflows {
			w, err := LoadWorkflowFileSource(ref.AbsPath, WorkflowSourcePlugin)
			if err != nil {
				diags = append(diags, pluginDiag(ref, "malformed", err.Error()))
				continue
			}
			if _, exists := byName[w.Name]; exists {
				diags = append(diags, plugin.Diagnostic{
					Severity:  plugin.SeverityWarning,
					Code:      "collision",
					Message:   fmt.Sprintf("workflow %q overrides earlier source", w.Name),
					PluginID:  ref.PluginID,
					Version:   ref.Version,
					Source:    ref.Source,
					Path:      ref.RelPath,
					Collision: w.Name,
				})
			} else {
				*order = append(*order, w.Name)
			}
			byName[w.Name] = w
		}
	}
	return diags
}

func applyPluginProviders(workDir string, cfg Config, scope plugin.Scope) (Config, []plugin.Diagnostic) {
	res := DiscoverPlugins(workDir)
	var diags []plugin.Diagnostic
	for _, p := range res.Plugins {
		if p.Source != scope {
			continue
		}
		for _, ref := range p.Providers {
			pf, err := ReadProvidersFile(ref.AbsPath)
			if err != nil {
				diags = append(diags, pluginDiag(ref, "malformed", err.Error()))
				continue
			}
			// Reject disable-default flags from plugins (policy surface).
			if pf.DisableDefaultAll != nil || len(pf.DisableDefaultPer) > 0 {
				diags = append(diags, pluginDiag(ref, "malformed", "plugin providers must not set disable-default flags"))
				continue
			}
			// profileName forces a single custom profile id when set.
			if ref.ProfileName != "" {
				if len(pf.Customs) == 0 && len(pf.Overlays) == 0 && len(pf.Endpoints) == 0 {
					diags = append(diags, pluginDiag(ref, "malformed", "provider file has no profiles"))
					continue
				}
				if len(pf.Customs) > 1 {
					diags = append(diags, pluginDiag(ref, "malformed", "profileName requires a single custom provider in the file"))
					continue
				}
				if len(pf.Customs) == 1 {
					c := pf.Customs[0]
					c.Name = ref.ProfileName
					c = NormalizeCustomProvider(c)
					if err := c.Validate(); err != nil {
						diags = append(diags, pluginDiag(ref, "malformed", err.Error()))
						continue
					}
					// Hard limit: shipped wire adapters only (Validate already checks WireAPI).
					pf.Customs = []CustomProvider{c}
					pf.Overlays = nil
					pf.Endpoints = nil
				}
			}
			// Ensure every custom validates (wire-only; no arbitrary code).
			ok := true
			for i := range pf.Customs {
				pf.Customs[i] = NormalizeCustomProvider(pf.Customs[i])
				if err := pf.Customs[i].Validate(); err != nil {
					diags = append(diags, pluginDiag(ref, "malformed", err.Error()))
					ok = false
					break
				}
				// Refuse inline credential-looking header values that are not env refs.
				for k, v := range pf.Customs[i].Headers {
					if looksLikeSecretLiteral(v) {
						diags = append(diags, pluginDiag(ref, "secret", fmt.Sprintf("header %q looks like a credential literal; use secret refs / env only", k)))
						ok = false
						break
					}
				}
				if !ok {
					break
				}
			}
			if !ok {
				continue
			}
			cfg = applyProvidersFile(cfg, pf)
		}
	}
	return cfg, diags
}

func pluginDiag(ref plugin.FileRef, code, msg string) plugin.Diagnostic {
	return plugin.Diagnostic{
		Severity: plugin.SeverityError,
		Code:     code,
		Message:  msg,
		PluginID: ref.PluginID,
		Version:  ref.Version,
		Source:   ref.Source,
		Path:     ref.RelPath,
	}
}

func looksLikeSecretLiteral(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" {
		return false
	}
	if ContainsEnvRef(v) {
		return false
	}
	// secret:// refs are OK
	if strings.HasPrefix(v, "secret://") {
		return false
	}
	lower := strings.ToLower(v)
	if strings.HasPrefix(lower, "sk-") || strings.HasPrefix(lower, "rk-") {
		return true
	}
	if strings.HasPrefix(lower, "bearer ") {
		return true
	}
	// Long high-entropy-ish tokens
	if len(v) >= 32 && !strings.ContainsAny(v, " \t") {
		return true
	}
	return false
}

// ReadHooksLayer loads hooks from a single config file path (missing → nil).
func ReadHooksLayer(path string) ([]Hook, error) {
	if path == "" {
		return nil, nil
	}
	layer, err := read(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return append([]Hook(nil), layer.Hooks...), nil
}

// HookLayers returns user global and project hooks separately (for §4.1
// interleaving with plugin hooks). Managed/MDM hooks are not included.
func HookLayers(workDir string) (global, project []Hook, err error) {
	global, err = ReadHooksLayer(GlobalPath())
	if err != nil {
		return nil, nil, err
	}
	if workDir != "" {
		project, err = ReadHooksLayer(filepath.Join(projectRoot(workDir), "config"))
		if err != nil {
			return nil, nil, err
		}
	}
	return global, project, nil
}

// ApplyPluginExecutables merges trusted plugin MCP/harness/hook contributions
// into cfg. User MCP/harness names win; plugin-plugin collisions are reported
// in the returned diagnostics. Hook order is:
//
//	user global → plugin global → user project → plugin project → managed (cfg.Hooks tail if any)
//
// When rebuildHooks is true, cfg.Hooks is replaced using HookLayers + plugin
// hooks. Managed hooks already present only in cfg (from Load) are preserved
// by appending any cfg hooks not found in the user layers (best-effort).
func ApplyPluginExecutables(workDir string, cfg Config, exec plugin.ExecutableSet, rebuildHooks bool) (Config, []plugin.Diagnostic) {
	diags := append([]plugin.Diagnostic(nil), exec.Diagnostics...)

	// MCP: add plugin servers when name not already set by user config.
	if len(exec.MCP) > 0 {
		if cfg.MCP.Servers == nil {
			cfg.MCP.Servers = map[string]MCPServer{}
		}
		for _, m := range exec.MCP {
			if _, exists := cfg.MCP.Servers[m.Name]; exists {
				continue
			}
			srv := MCPServer{
				Type:    m.Transport,
				Command: m.Command,
				Args:    append([]string(nil), m.Args...),
				Env:     cloneStringMap(m.Env),
				Cwd:     m.Cwd,
				URL:     m.URL,
				Headers: cloneStringMap(m.Headers),
			}
			cfg.MCP.Servers[m.Name] = srv
		}
	}

	// Harnesses: add when name not already set.
	if len(exec.Harnesses) > 0 {
		if cfg.Harnesses == nil {
			cfg.Harnesses = map[string]HarnessConfig{}
		}
		for _, h := range exec.Harnesses {
			if _, exists := cfg.Harnesses[h.Name]; exists {
				continue
			}
			cfg.Harnesses[h.Name] = HarnessConfig{
				Command:       h.Command,
				Args:          append([]string(nil), h.Args...),
				Env:           cloneStringMap(h.Env),
				Mode:          h.Mode,
				MaxConcurrent: h.MaxConcurrent,
				IdleTimeoutMs: h.IdleTimeoutMs,
				MaxRestarts:   h.MaxRestarts,
			}
		}
	}

	if rebuildHooks {
		userGlobal, userProject, err := HookLayers(workDir)
		if err != nil {
			diags = append(diags, plugin.Diagnostic{
				Severity: plugin.SeverityWarning,
				Code:     "hooks",
				Message:  "could not split user hook layers: " + err.Error(),
			})
		} else {
			// Preserve managed-only hooks: entries in cfg.Hooks not in user layers.
			managedTail := hooksNotIn(cfg.Hooks, append(append([]Hook{}, userGlobal...), userProject...))
			var merged []Hook
			merged = append(merged, userGlobal...)
			merged = append(merged, compiledHooksToConfig(exec.GlobalHooks)...)
			merged = append(merged, userProject...)
			merged = append(merged, compiledHooksToConfig(exec.ProjectHooks)...)
			merged = append(merged, managedTail...)
			cfg.Hooks = merged
		}
	}

	return cfg, diags
}

func compiledHooksToConfig(in []plugin.CompiledHook) []Hook {
	out := make([]Hook, 0, len(in))
	for _, h := range in {
		out = append(out, Hook{
			Event:     h.Event,
			Matcher:   h.Matcher,
			Action:    h.Action,
			Message:   h.Message,
			Command:   h.Command,
			TimeoutMs: h.TimeoutMs,
		})
	}
	return out
}

func hooksNotIn(all, known []Hook) []Hook {
	if len(all) == 0 {
		return nil
	}
	type key struct {
		event, matcher, action, command, message string
		timeout                                  int
	}
	seen := map[key]int{}
	for _, h := range known {
		k := key{h.Event, h.Matcher, h.Action, h.Command, h.Message, h.TimeoutMs}
		seen[k]++
	}
	var out []Hook
	for _, h := range all {
		k := key{h.Event, h.Matcher, h.Action, h.Command, h.Message, h.TimeoutMs}
		if n := seen[k]; n > 0 {
			seen[k] = n - 1
			continue
		}
		out = append(out, h)
	}
	return out
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
