package config

import (
	"fmt"
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

func applyPluginProviders(workDir string, cfg Config) (Config, []plugin.Diagnostic) {
	res := DiscoverPlugins(workDir)
	var diags []plugin.Diagnostic
	// Apply global then project plugin providers (Discover order).
	for _, p := range res.Plugins {
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
