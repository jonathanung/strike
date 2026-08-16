package local

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/plugin"
	"github.com/jonathanung/strike-cli/internal/trust/secret"
	"github.com/jonathanung/strike-cli/internal/version"
)

// NewPanes builds a host.Panes backed by installed plugin contributions.
// workDir scopes project installs; empty still allows global plugins.
func NewPanes(workDir string) host.Panes {
	return panesAdapter{workDir: strings.TrimSpace(workDir)}
}

// NewPanesForTest pins roots for unit tests.
func NewPanesForTest(workDir, globalRoot, projectRoot string) host.Panes {
	return panesAdapter{
		workDir:     strings.TrimSpace(workDir),
		globalRoot:  strings.TrimSpace(globalRoot),
		projectRoot: strings.TrimSpace(projectRoot),
	}
}

type panesAdapter struct {
	workDir     string
	globalRoot  string
	projectRoot string
	strikeVer   string
}

func (a panesAdapter) strikeVersion() string {
	if a.strikeVer != "" {
		return a.strikeVer
	}
	return version.Version
}

func (a panesAdapter) List() ([]host.PaneInfo, error) {
	report, err := plugin.Doctor(plugin.DoctorOptions{
		WorkDir:       a.workDir,
		GlobalRoot:    a.globalRoot,
		ProjectRoot:   a.projectRoot,
		StrikeVersion: a.strikeVersion(),
	})
	if err != nil {
		return nil, err
	}

	type cand struct {
		info     host.PaneInfo
		scopeOrd int // 0 global, 1 project
		pluginID string
		paneID   string
	}
	var cands []cand
	seenIDs := map[string][]string{} // pane id → plugin ids

	for _, dp := range report.Plugins {
		if !dp.Enabled || dp.Root == "" {
			continue
		}
		m, _, err := plugin.ReadManifest(dp.Root)
		if err != nil || m.ID == "" {
			continue
		}

		// Process panes require a matching trust grant (doctor TrustState).
		processTrusted := dp.TrustState == host.PluginTrustTrusted || dp.TrustState == "trusted"

		scopeOrd := 1
		if dp.Scope == plugin.ScopeGlobal {
			scopeOrd = 0
		}

		for _, pref := range plugin.PluginPaneRefs(m, dp.Root) {
			def, _, err := plugin.ReadPaneDefinition(dp.Root, pref.RelPath)
			if err != nil {
				continue
			}
			paneID := def.ID
			if pref.EntryID != "" {
				if def.ID != pref.EntryID {
					continue
				}
				paneID = pref.EntryID
			}
			net := strings.TrimSpace(def.Permissions.Network)
			if net == "" {
				net = "none"
			}
			loadErr := ""
			if net == "host-mediated" {
				loadErr = "permissions.network host-mediated not implemented in v1"
			}
			mode := def.Mode
			paneTrusted := true
			if mode == plugin.PaneModeProcess {
				paneTrusted = processTrusted
				if !paneTrusted && loadErr == "" {
					loadErr = "process pane blocked until plugin trust is granted"
				}
				// Resolve secret refs at host boundary (TUI must not import secret).
				if resolved, err := resolvePaneEnv(def.Env); err != nil {
					if loadErr == "" {
						loadErr = err.Error()
					}
					paneTrusted = false
				} else {
					def.Env = resolved
				}
			}
			defJSON, err := json.Marshal(def)
			if err != nil {
				continue
			}
			info := host.PaneInfo{
				ID:             paneID,
				PluginID:       m.ID,
				PluginVersion:  m.Version,
				Scope:          pluginScopeString(dp.Scope),
				Title:          strings.TrimSpace(def.Title),
				Mode:           mode,
				Trusted:        paneTrusted && loadErr == "",
				PluginRoot:     dp.Root,
				DefinitionJSON: defJSON,
				LoadError:      loadErr,
			}
			seenIDs[paneID] = append(seenIDs[paneID], m.ID)
			cands = append(cands, cand{
				info:     info,
				scopeOrd: scopeOrd,
				pluginID: m.ID,
				paneID:   paneID,
			})
		}
	}

	// Fail closed on collisions: drop all claimants for a colliding pane id.
	colliding := map[string]string{} // pane id → other plugin
	for id, plugins := range seenIDs {
		uniq := uniqueSorted(plugins)
		if len(uniq) < 2 {
			continue
		}
		colliding[id] = uniq[0] + "," + uniq[1]
	}

	out := make([]host.PaneInfo, 0, len(cands))
	for _, c := range cands {
		if other, hit := colliding[c.info.ID]; hit {
			_ = other
			// Omit from active registry (fail closed). Callers that need
			// diagnostics can re-run doctor; List stays mount-ready only.
			continue
		}
		if c.info.LoadError != "" && c.info.Mode == plugin.PaneModeProcess {
			// Still register so the TUI can show an error empty-state with
			// provenance and a path to /plugin trust.
		}
		out = append(out, c.info)
	}

	sort.SliceStable(out, func(i, j int) bool {
		// Reconstruct order keys from fields.
		si, sj := 1, 1
		if out[i].Scope == host.PluginScopeGlobal {
			si = 0
		}
		if out[j].Scope == host.PluginScopeGlobal {
			sj = 0
		}
		if si != sj {
			return si < sj
		}
		if out[i].PluginID != out[j].PluginID {
			return out[i].PluginID < out[j].PluginID
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// resolvePaneEnv expands secret:// refs in process pane env maps. Literals pass
// through. Fail closed on unresolved refs so panes never start with a ref string.
func resolvePaneEnv(in map[string]string) (map[string]string, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if ref, ok := secret.ParseRef(v); ok {
			val, err := secret.Resolve(ref)
			if err != nil {
				return nil, fmt.Errorf("pane env %q: %w", k, err)
			}
			out[k] = val
			continue
		}
		out[k] = v
	}
	return out, nil
}
