package local

import (
	"fmt"
	"strings"
	"sync"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/permission"
)

// Permissions adapts permission explain/presets/diff onto host.Permissions.
type Permissions struct {
	mu      sync.Mutex
	layers  []permission.LabeledLayer
	live    func(permission, pattern string) permission.DetailedExplanation
	sandbox func() permission.SandboxExplainBits
}

// NewPermissions builds a host adapter from base evaluation layers.
// names, when non-empty, label layers[i]; missing names use layer-N.
func NewPermissions(layers []permission.Ruleset, names []string) *Permissions {
	labeled := make([]permission.LabeledLayer, len(layers))
	for i, rs := range layers {
		name := ""
		if i < len(names) {
			name = names[i]
		}
		labeled[i] = permission.LabeledLayer{Name: name, Rules: rs}
	}
	return &Permissions{layers: labeled}
}

// SetLive installs a callback for live service explain (agent/phase/session).
// nil clears (falls back to base layers only).
func (p *Permissions) SetLive(fn func(permission, pattern string) permission.DetailedExplanation) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.live = fn
	p.mu.Unlock()
}

// SetSandboxBits installs a callback that supplies sandbox dial + network.allow
// for the explain surface. nil clears.
func (p *Permissions) SetSandboxBits(fn func() permission.SandboxExplainBits) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.sandbox = fn
	p.mu.Unlock()
}

// SetLayers replaces the base labeled layers (e.g. after config reload).
func (p *Permissions) SetLayers(layers []permission.LabeledLayer) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.layers = append([]permission.LabeledLayer(nil), layers...)
	p.mu.Unlock()
}

// Explain implements host.Permissions.
func (p *Permissions) Explain(perm, pattern string) host.PermissionExplanation {
	return p.explain(perm, pattern, "", false)
}

// ExplainPreset implements host.Permissions (dry-run alternate preset).
func (p *Permissions) ExplainPreset(perm, pattern, presetID string) host.PermissionExplanation {
	return p.explain(perm, pattern, presetID, true)
}

func (p *Permissions) explain(perm, pattern, presetID string, dryRun bool) host.PermissionExplanation {
	if p == nil {
		return toHost(permission.ExplainDetailed(perm, pattern, nil), "", nil, nil)
	}
	p.mu.Lock()
	live := p.live
	layers := append([]permission.LabeledLayer(nil), p.layers...)
	sandboxFn := p.sandbox
	p.mu.Unlock()

	var sb *permission.SandboxExplainBits
	if sandboxFn != nil {
		bits := sandboxFn()
		sb = &bits
	}

	if dryRun {
		// Dry-run always uses static layers + preset swap (not live session
		// grants) so operators see preset impact without session noise.
		ex, err := permission.ExplainWithPreset(layers, presetID, perm, pattern)
		if err != nil {
			return host.PermissionExplanation{
				Permission:   perm,
				Pattern:      pattern,
				Summary:      err.Error(),
				DryRunPreset: presetID,
			}
		}
		// Include managed ceiling against the dry-run stack.
		replaced, _ := permission.ReplacePresetLayer(layers, presetID)
		ceil := permission.InspectCeiling(replaced, perm, patternOrStar(pattern))
		h := toHost(ex, presetID, &ceil, sb)
		h.DryRunPreset = strings.TrimSpace(presetID)
		if h.DryRunPreset == "" {
			h.DryRunPreset = "(none)"
		}
		return h
	}

	if live != nil {
		ex := live(perm, pattern)
		// Ceiling from base layers + any managed layer present in base.
		ceil := permission.InspectCeiling(layers, perm, patternOrStar(pattern))
		// If live matched managed, reflect that even when base layers lack it.
		if ex.Matched != nil && ex.Matched.Layer == permission.LayerManaged {
			ceil.ManagedBlocks = true
			if ceil.Summary == "" {
				ceil.Summary = fmt.Sprintf("managed ceiling active: %s %s → %s",
					ex.Permission, patternOrStar(pattern), ex.Action)
			}
		}
		return toHost(ex, "", &ceil, sb)
	}
	ex := permission.ExplainDetailed(perm, pattern, layers)
	ceil := permission.InspectCeiling(layers, perm, patternOrStar(pattern))
	return toHost(ex, "", &ceil, sb)
}

// DiffPresets implements host.Permissions.
func (p *Permissions) DiffPresets(leftID, rightID string) (host.PermissionDiff, error) {
	d, err := permission.DiffPresets(leftID, rightID)
	if err != nil {
		return host.PermissionDiff{}, err
	}
	return toHostDiff(d), nil
}

// Presets implements host.Permissions.
func (p *Permissions) Presets() []host.PermissionPresetInfo {
	src := permission.Presets()
	out := make([]host.PermissionPresetInfo, len(src))
	for i, pr := range src {
		out[i] = host.PermissionPresetInfo{
			ID:          pr.ID,
			Name:        pr.Name,
			Description: pr.Description,
		}
	}
	return out
}

func patternOrStar(p string) string {
	if strings.TrimSpace(p) == "" {
		return "*"
	}
	return p
}

func toHost(ex permission.DetailedExplanation, dryPreset string, ceiling *permission.CeilingInfo, sandbox *permission.SandboxExplainBits) host.PermissionExplanation {
	summary := permission.FormatDetailedExplanationFull(ex, ceiling, sandbox)
	if dryPreset != "" {
		label := dryPreset
		if label == "" {
			label = "(none)"
		}
		summary = "dry-run preset=" + label + "\n" + summary
	}
	h := host.PermissionExplanation{
		Permission:  ex.Permission,
		Pattern:     ex.Pattern,
		Action:      string(ex.Action),
		Summary:     summary,
		EvalPath:    ex.EvalPath,
		FactSummary: ex.FactSummary,
	}
	if ex.Matched != nil {
		h.Layer = ex.Matched.Layer
		h.Matched = host.PermissionMatch{
			Layer:      ex.Matched.Layer,
			Permission: ex.Matched.Permission,
			Pattern:    ex.Matched.Pattern,
			Action:     string(ex.Matched.Action),
		}
	}
	if len(ex.Trail) > 0 {
		h.Trail = make([]host.PermissionMatch, len(ex.Trail))
		for i, m := range ex.Trail {
			h.Trail[i] = host.PermissionMatch{
				Layer:      m.Layer,
				Permission: m.Permission,
				Pattern:    m.Pattern,
				Action:     string(m.Action),
			}
		}
	}
	if ceiling != nil {
		h.ManagedBlocks = ceiling.ManagedBlocks
		h.ManagedSummary = ceiling.Summary
	}
	if sandbox != nil {
		h.SandboxSummary = permission.FormatSandboxBits(*sandbox)
	}
	return h
}

func toHostDiff(d permission.DiffResult) host.PermissionDiff {
	out := host.PermissionDiff{
		LeftLabel:  d.LeftLabel,
		RightLabel: d.RightLabel,
		Summary:    permission.FormatDiff(d),
	}
	if len(d.Changes) == 0 {
		return out
	}
	out.Changes = make([]host.PermissionRuleDelta, len(d.Changes))
	for i, c := range d.Changes {
		out.Changes[i] = host.PermissionRuleDelta{
			Kind:  string(c.Kind),
			Layer: c.Layer,
		}
		if c.Before != nil {
			out.Changes[i].Before = &host.PermissionRuleRef{
				Layer:      c.Before.Layer,
				Permission: c.Before.Permission,
				Pattern:    c.Before.Pattern,
				Action:     string(c.Before.Action),
			}
		}
		if c.After != nil {
			out.Changes[i].After = &host.PermissionRuleRef{
				Layer:      c.After.Layer,
				Permission: c.After.Permission,
				Pattern:    c.After.Pattern,
				Action:     string(c.After.Action),
			}
		}
	}
	return out
}
