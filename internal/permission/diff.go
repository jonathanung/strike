package permission

import (
	"fmt"
	"sort"
	"strings"
)

// DiffKind classifies one ruleset delta entry.
type DiffKind string

const (
	DiffAdded   DiffKind = "added"
	DiffRemoved DiffKind = "removed"
	DiffChanged DiffKind = "changed"
)

// RuleRef is a rule with its layer label for diff/explain output.
type RuleRef struct {
	Layer      string `json:"layer"`
	Permission string `json:"permission"`
	Pattern    string `json:"pattern"`
	Action     Action `json:"action"`
}

// RuleDelta is one added/removed/changed rule between two effective views.
type RuleDelta struct {
	Kind   DiffKind `json:"kind"`
	Layer  string   `json:"layer"`
	Before *RuleRef `json:"before,omitempty"`
	After  *RuleRef `json:"after,omitempty"`
}

// DiffResult is the structured diff between two labeled ruleset stacks.
type DiffResult struct {
	LeftLabel  string      `json:"leftLabel"`
	RightLabel string      `json:"rightLabel"`
	Changes    []RuleDelta `json:"changes"`
}

// ruleKey uniquely identifies a rule slot within a layer for diffing.
func ruleKey(layer, permission, pattern string) string {
	if pattern == "" {
		pattern = "*"
	}
	return layer + "\x00" + permission + "\x00" + pattern
}

// FlattenRules returns every rule across layers with stable layer labels.
func FlattenRules(layers []LabeledLayer) []RuleRef {
	var out []RuleRef
	for li, layer := range layers {
		name := layer.Name
		if name == "" {
			name = fmt.Sprintf("layer-%d", li)
		}
		for _, r := range layer.Rules {
			pat := r.Pattern
			if pat == "" {
				pat = "*"
			}
			out = append(out, RuleRef{
				Layer:      name,
				Permission: r.Permission,
				Pattern:    pat,
				Action:     r.Action,
			})
		}
	}
	return out
}

// DiffLabeled compares two labeled layer stacks rule-by-rule.
// Matching key is (layer, permission, pattern); action differences are "changed".
// Rules only on the left are "removed"; only on the right are "added".
func DiffLabeled(left, right []LabeledLayer, leftLabel, rightLabel string) DiffResult {
	res := DiffResult{LeftLabel: leftLabel, RightLabel: rightLabel}
	leftMap := map[string]RuleRef{}
	rightMap := map[string]RuleRef{}
	for _, r := range FlattenRules(left) {
		leftMap[ruleKey(r.Layer, r.Permission, r.Pattern)] = r
	}
	for _, r := range FlattenRules(right) {
		rightMap[ruleKey(r.Layer, r.Permission, r.Pattern)] = r
	}
	keys := make(map[string]struct{}, len(leftMap)+len(rightMap))
	for k := range leftMap {
		keys[k] = struct{}{}
	}
	for k := range rightMap {
		keys[k] = struct{}{}
	}
	ordered := make([]string, 0, len(keys))
	for k := range keys {
		ordered = append(ordered, k)
	}
	sort.Strings(ordered)
	for _, k := range ordered {
		l, lok := leftMap[k]
		r, rok := rightMap[k]
		switch {
		case lok && !rok:
			cp := l
			res.Changes = append(res.Changes, RuleDelta{Kind: DiffRemoved, Layer: l.Layer, Before: &cp})
		case !lok && rok:
			cp := r
			res.Changes = append(res.Changes, RuleDelta{Kind: DiffAdded, Layer: r.Layer, After: &cp})
		case lok && rok && (l.Action != r.Action):
			lb, rb := l, r
			res.Changes = append(res.Changes, RuleDelta{Kind: DiffChanged, Layer: r.Layer, Before: &lb, After: &rb})
		}
	}
	return res
}

// DiffPresets diffs two shipped presets as single-layer stacks.
// Unknown ids return an error.
func DiffPresets(leftID, rightID string) (DiffResult, error) {
	lp, ok := PresetByID(leftID)
	if !ok {
		return DiffResult{}, fmt.Errorf("unknown preset %q", leftID)
	}
	rp, ok := PresetByID(rightID)
	if !ok {
		return DiffResult{}, fmt.Errorf("unknown preset %q", rightID)
	}
	left := []LabeledLayer{{Name: LayerPreset + ":" + lp.ID, Rules: lp.Rules}}
	right := []LabeledLayer{{Name: LayerPreset + ":" + rp.ID, Rules: rp.Rules}}
	return DiffLabeled(left, right, "preset:"+lp.ID, "preset:"+rp.ID), nil
}

// FormatDiff renders a human-readable multi-line diff.
func FormatDiff(d DiffResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "permission diff %s → %s", d.LeftLabel, d.RightLabel)
	if len(d.Changes) == 0 {
		b.WriteString("\n  (no rule changes)")
		return b.String()
	}
	for _, c := range d.Changes {
		switch c.Kind {
		case DiffAdded:
			if c.After != nil {
				fmt.Fprintf(&b, "\n  + [%s] %s %s → %s", c.Layer, c.After.Permission, quotePat(c.After.Pattern), c.After.Action)
			}
		case DiffRemoved:
			if c.Before != nil {
				fmt.Fprintf(&b, "\n  - [%s] %s %s → %s", c.Layer, c.Before.Permission, quotePat(c.Before.Pattern), c.Before.Action)
			}
		case DiffChanged:
			if c.Before != nil && c.After != nil {
				fmt.Fprintf(&b, "\n  ~ [%s] %s %s: %s → %s", c.Layer, c.After.Permission, quotePat(c.After.Pattern), c.Before.Action, c.After.Action)
			}
		}
	}
	return b.String()
}

// ReplacePresetLayer returns a copy of layers where the preset-named layer
// is replaced with the shipped preset rules (or inserted after defaults when
// no preset layer exists). Empty presetID removes the preset layer.
// Does not mutate the session service — dry-run only.
func ReplacePresetLayer(layers []LabeledLayer, presetID string) ([]LabeledLayer, error) {
	presetID = strings.TrimSpace(presetID)
	var presetRules Ruleset
	if presetID != "" {
		p, ok := PresetByID(presetID)
		if !ok {
			return nil, fmt.Errorf("unknown preset %q (want read-only|dev|yolo-with-sandbox)", presetID)
		}
		presetRules = append(Ruleset(nil), p.Rules...)
	}

	// Drop existing preset layer; re-insert after defaults (last-match-wins:
	// preset must sit after defaults and before config).
	stripped := make([]LabeledLayer, 0, len(layers)+1)
	for _, layer := range layers {
		if layer.Name == LayerPreset {
			continue
		}
		stripped = append(stripped, LabeledLayer{Name: layer.Name, Rules: append(Ruleset(nil), layer.Rules...)})
	}
	if presetID == "" {
		return stripped, nil
	}
	out := make([]LabeledLayer, 0, len(stripped)+1)
	inserted := false
	for i, layer := range stripped {
		out = append(out, layer)
		if layer.Name == LayerDefaults {
			// Insert immediately after defaults (or after the last defaults if duplicated).
			if i+1 == len(stripped) || stripped[i+1].Name != LayerDefaults {
				out = append(out, LabeledLayer{Name: LayerPreset, Rules: presetRules})
				inserted = true
			}
		}
	}
	if !inserted {
		// No defaults layer — put preset first so config/session still win.
		out = append([]LabeledLayer{{Name: LayerPreset, Rules: presetRules}}, out...)
	}
	return out, nil
}

// ExplainWithPreset dry-runs ExplainDetailed after swapping the preset layer.
// Session grants and live service state are not mutated.
func ExplainWithPreset(layers []LabeledLayer, presetID, permission, pattern string) (DetailedExplanation, error) {
	replaced, err := ReplacePresetLayer(layers, presetID)
	if err != nil {
		return DetailedExplanation{}, err
	}
	return ExplainDetailed(permission, pattern, replaced), nil
}

// CeilingInfo describes whether the managed deny ceiling blocks a widen
// relative to evaluation without the managed layer.
type CeilingInfo struct {
	Permission     string `json:"permission"`
	Pattern        string `json:"pattern"`
	WithoutManaged Action `json:"withoutManaged"`
	WithManaged    Action `json:"withManaged"`
	ManagedBlocks  bool   `json:"managedBlocks"`
	ManagedRule    *Match `json:"managedRule,omitempty"`
	Summary        string `json:"summary,omitempty"`
}

// InspectCeiling evaluates permission/pattern with and without managed layers.
// ManagedBlocks is true when managed makes the effective action stricter
// (lower ActionRank) than the stack without managed.
func InspectCeiling(layers []LabeledLayer, permission, pattern string) CeilingInfo {
	if pattern == "" {
		pattern = "*"
	}
	var without, with []LabeledLayer
	var managedRules Ruleset
	for _, layer := range layers {
		cp := LabeledLayer{Name: layer.Name, Rules: append(Ruleset(nil), layer.Rules...)}
		if layer.Name == LayerManaged {
			managedRules = append(Ruleset(nil), layer.Rules...)
			with = append(with, cp)
			continue
		}
		without = append(without, cp)
		with = append(with, cp)
	}
	// If managed was not a named layer, still compute with==without.
	exWithout := ExplainLabeled(permission, pattern, without)
	exWith := ExplainLabeled(permission, pattern, with)
	info := CeilingInfo{
		Permission:     permission,
		Pattern:        pattern,
		WithoutManaged: exWithout.Action,
		WithManaged:    exWith.Action,
	}
	if ActionRank(exWith.Action) < ActionRank(exWithout.Action) {
		info.ManagedBlocks = true
	}
	// Find managed match on the with trail.
	for i := range exWith.Trail {
		m := exWith.Trail[i]
		if m.Layer == LayerManaged {
			cp := m
			info.ManagedRule = &cp
		}
	}
	if info.ManagedBlocks {
		info.Summary = fmt.Sprintf("managed ceiling blocks widen: %s %s without=%s with=%s",
			permission, quotePat(pattern), exWithout.Action, exWith.Action)
		if info.ManagedRule != nil {
			info.Summary += fmt.Sprintf(" (managed %s %s → %s)",
				info.ManagedRule.Permission, quotePat(info.ManagedRule.Pattern), info.ManagedRule.Action)
		}
	} else if len(managedRules) > 0 {
		info.Summary = fmt.Sprintf("managed ceiling inactive for %s %s (without=%s with=%s)",
			permission, quotePat(pattern), exWithout.Action, exWith.Action)
	}
	return info
}

// SandboxExplainBits is a compact sandbox/network posture for the explain surface.
type SandboxExplainBits struct {
	Mode             string   `json:"mode,omitempty"`
	NoNetwork        bool     `json:"noNetwork,omitempty"`
	NoWorkspaceWrite bool     `json:"noWorkspaceWrite,omitempty"`
	NetworkAllow     []string `json:"networkAllow,omitempty"`
	Summary          string   `json:"summary,omitempty"`
}

// FormatSandboxBits renders sandbox dial + network allow for notices.
func FormatSandboxBits(s SandboxExplainBits) string {
	if s.Summary != "" {
		return s.Summary
	}
	var parts []string
	if s.Mode != "" {
		parts = append(parts, "sandbox="+s.Mode)
	}
	if s.NoWorkspaceWrite {
		parts = append(parts, "workspace-write=off")
	}
	if s.NoNetwork {
		parts = append(parts, "network=off")
	} else if s.Mode != "" {
		parts = append(parts, "network=on")
	}
	if len(s.NetworkAllow) > 0 {
		parts = append(parts, "network.allow=["+strings.Join(s.NetworkAllow, ", ")+"]")
	}
	return strings.Join(parts, " ")
}

// FormatExplanationFull appends optional ceiling + sandbox lines to FormatExplanation.
func FormatExplanationFull(ex Explanation, ceiling *CeilingInfo, sandbox *SandboxExplainBits) string {
	return appendCeilingSandbox(FormatExplanation(ex), ceiling, sandbox)
}

// FormatDetailedExplanationFull is FormatDetailedExplanation plus ceiling/sandbox.
func FormatDetailedExplanationFull(ex DetailedExplanation, ceiling *CeilingInfo, sandbox *SandboxExplainBits) string {
	return appendCeilingSandbox(FormatDetailedExplanation(ex), ceiling, sandbox)
}

func appendCeilingSandbox(s string, ceiling *CeilingInfo, sandbox *SandboxExplainBits) string {
	if ceiling != nil && ceiling.Summary != "" {
		s += "\n  " + ceiling.Summary
	}
	if sandbox != nil {
		if line := FormatSandboxBits(*sandbox); line != "" {
			s += "\n  " + line
		}
	}
	return s
}
