package permission

import (
	"fmt"
	"strings"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

// Layer names for explain output. Stable strings for docs, slash commands,
// and audit events.
const (
	LayerDefaults      = "defaults"
	LayerPreset        = "preset"
	LayerConfig        = "config"
	LayerDangerous     = "dangerous-allow-all"
	LayerProject       = "project"
	LayerAgent         = "agent"
	LayerSession       = "session"
	LayerScopedGrant   = "scoped-grant"
	LayerModeLate      = "mode-late"
	LayerPhase         = "phase"
	LayerManaged       = "managed"
	LayerModeUpgrade   = "mode-upgrade"
	LayerDefaultAction = "default"
)

// LabeledLayer is one evaluation layer with a stable source name.
type LabeledLayer struct {
	Name  string
	Rules Ruleset
}

// Match is one rule that matched during evaluation.
type Match struct {
	Layer      string `json:"layer"`
	LayerIndex int    `json:"layerIndex"`
	RuleIndex  int    `json:"ruleIndex"`
	Permission string `json:"permission"`
	Pattern    string `json:"pattern"`
	Action     Action `json:"action"`
}

// Explanation is the result of explaining a single permission/pattern pair.
type Explanation struct {
	Permission string  `json:"permission"`
	Pattern    string  `json:"pattern"`
	Action     Action  `json:"action"`
	Matched    *Match  `json:"matched,omitempty"`
	Trail      []Match `json:"trail,omitempty"`
	// ModeApplied is true when yolo/accept-edits upgraded Ask→Allow.
	ModeApplied bool                    `json:"modeApplied,omitempty"`
	Mode        protocol.PermissionMode `json:"mode,omitempty"`
}

// Explain returns last-match-wins detail for permission/pattern across sets.
// Layer names default to "layer-N". Empty pattern is treated as "*".
func Explain(permission, pattern string, sets ...Ruleset) Explanation {
	layers := make([]LabeledLayer, len(sets))
	for i, set := range sets {
		layers[i] = LabeledLayer{Name: fmt.Sprintf("layer-%d", i), Rules: set}
	}
	return ExplainLabeled(permission, pattern, layers)
}

// ExplainLabeled is Explain with explicit layer names (defaults, config, …).
// Uses action facts for bash/selected tools when enforcement-eligible (#888).
func ExplainLabeled(permission, pattern string, layers []LabeledLayer) Explanation {
	return ExplainDetailed(permission, pattern, layers).Explanation
}

// Explain applies live service layers (including scoped grants and mode).
// Empty patterns default to "*". When multiple patterns are given, the
// worst-case action is reported. Uses action facts for bash/selected tools
// when the parse is enforcement-eligible (#888).
func (s *Service) Explain(permission string, patterns ...string) Explanation {
	return s.ExplainDetailed(permission, patterns...).Explanation
}

// ExplainDetailed is Explain with fact-path diagnostics for /permission explain.
func (s *Service) ExplainDetailed(permission string, patterns ...string) DetailedExplanation {
	if s == nil {
		return ExplainDetailed(permission, firstPattern(patterns), nil)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneExpiredLocked(s.now())
	return s.evaluateDetailedLocked(permission, patterns)
}

// explainLocked assumes mu held and expired grants pruned.
// Prefer evaluateDetailedLocked for new call sites.
func (s *Service) explainLocked(permission, pattern string) Explanation {
	return s.evaluateDetailedLocked(permission, []string{pattern}).Explanation
}

// labeledLayersLocked returns evaluation layers in last-match-wins order.
func (s *Service) labeledLayersLocked() []LabeledLayer {
	out := make([]LabeledLayer, 0, len(s.base)+6)
	for i, layer := range s.base {
		name := LayerConfig
		switch {
		case i == 0:
			name = LayerDefaults
		case i == 1 && len(s.base) >= 2:
			// Common assembly: defaults, [preset], config, [dangerous].
			// Heuristic names; callers that need precision pass LabeledLayer.
			if len(s.base) == 2 {
				name = LayerConfig
			} else if i == len(s.base)-1 {
				name = LayerConfig
			} else {
				name = fmt.Sprintf("%s-%d", LayerConfig, i)
			}
		default:
			name = fmt.Sprintf("%s-%d", LayerConfig, i)
		}
		// Prefer stable names when base was installed via New with known shape.
		if i < len(s.baseNames) && s.baseNames[i] != "" {
			name = s.baseNames[i]
		}
		out = append(out, LabeledLayer{Name: name, Rules: layer})
	}
	out = append(out,
		LabeledLayer{Name: LayerProject, Rules: s.project},
		LabeledLayer{Name: LayerAgent, Rules: s.agent},
		LabeledLayer{Name: LayerSession, Rules: s.granted},
		LabeledLayer{Name: LayerScopedGrant, Rules: s.scopedRulesLocked()},
		LabeledLayer{Name: LayerModeLate, Rules: s.modeLate},
		LabeledLayer{Name: LayerPhase, Rules: s.phase},
		LabeledLayer{Name: LayerManaged, Rules: s.managed},
	)
	return out
}

// FormatExplanation renders a single-line then detail block for notices/CLI.
func FormatExplanation(ex Explanation) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s → %s", ex.Permission, quotePat(ex.Pattern), ex.Action)
	if ex.Matched != nil {
		fmt.Fprintf(&b, "\n  matched: %s %s %s (layer %s",
			ex.Matched.Permission, quotePat(ex.Matched.Pattern), ex.Matched.Action, ex.Matched.Layer)
		if ex.Matched.LayerIndex >= 0 {
			fmt.Fprintf(&b, "[%d].rule[%d]", ex.Matched.LayerIndex, ex.Matched.RuleIndex)
		}
		b.WriteByte(')')
	}
	if ex.ModeApplied {
		fmt.Fprintf(&b, "\n  mode: %s upgraded ask→allow", ex.Mode.Normalize())
	} else if ex.Mode != "" && ex.Mode.Normalize() != protocol.PermissionModeDefault {
		fmt.Fprintf(&b, "\n  mode: %s", ex.Mode.Normalize())
	}
	if len(ex.Trail) > 1 {
		b.WriteString("\n  trail:")
		for _, m := range ex.Trail {
			fmt.Fprintf(&b, "\n    - %s: %s %s → %s", m.Layer, m.Permission, quotePat(m.Pattern), m.Action)
		}
	}
	return b.String()
}

func quotePat(p string) string {
	if p == "" {
		return `"*"`
	}
	if strings.ContainsAny(p, " \t\"") {
		return fmt.Sprintf("%q", p)
	}
	return p
}

func normalizePattern(p string) string {
	if p == "" {
		return "*"
	}
	return p
}

func firstPattern(patterns []string) string {
	if len(patterns) == 0 || patterns[0] == "" {
		return "*"
	}
	return patterns[0]
}
