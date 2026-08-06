package local

import (
	"sync"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/permission"
)

// Permissions adapts permission explain/presets onto host.Permissions.
type Permissions struct {
	mu     sync.Mutex
	layers []permission.LabeledLayer
	live   func(permission, pattern string) permission.Explanation
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
func (p *Permissions) SetLive(fn func(permission, pattern string) permission.Explanation) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.live = fn
	p.mu.Unlock()
}

// Explain implements host.Permissions.
func (p *Permissions) Explain(perm, pattern string) host.PermissionExplanation {
	if p == nil {
		return toHost(permission.Explain(perm, pattern))
	}
	p.mu.Lock()
	live := p.live
	layers := p.layers
	p.mu.Unlock()
	if live != nil {
		return toHost(live(perm, pattern))
	}
	return toHost(permission.ExplainLabeled(perm, pattern, layers))
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

func toHost(ex permission.Explanation) host.PermissionExplanation {
	h := host.PermissionExplanation{
		Permission: ex.Permission,
		Pattern:    ex.Pattern,
		Action:     string(ex.Action),
		Summary:    permission.FormatExplanation(ex),
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
	return h
}
