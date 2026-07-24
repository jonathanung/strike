package provider

import "strings"

// Effort is the reasoning-effort dial: how much internal reasoning a model
// spends before it answers. Vendors expose this differently — Anthropic as
// output_config.effort alongside adaptive thinking, OpenAI as a
// reasoning-effort string — so the ladder is normalized here and each adapter
// maps it onto its own wire fields in toAPIRequest. The zero value means
// "send nothing", leaving the provider's own default in place.
type Effort string

const (
	// EffortDefault sends no reasoning fields at all.
	EffortDefault Effort = ""
	// EffortOff explicitly disables reasoning rather than leaving it to the
	// provider default (which, on current Claude models, is on).
	EffortOff    Effort = "off"
	EffortLow    Effort = "low"
	EffortMedium Effort = "medium"
	EffortHigh   Effort = "high"
	// EffortXHigh and EffortMax exist on Anthropic's ladder; adapters whose
	// vendor tops out lower clamp them down rather than erroring.
	EffortXHigh Effort = "xhigh"
	EffortMax   Effort = "max"
)

// Efforts lists the selectable levels from least to most reasoning. It
// excludes EffortDefault, which is a "leave it alone" sentinel rather than a
// level a user picks.
func Efforts() []Effort {
	return []Effort{EffortOff, EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax}
}

// ParseEffort resolves a user-typed level, case- and space-insensitively. An
// empty string parses to EffortDefault so callers can round-trip an unset
// value; anything unrecognized reports false.
func ParseEffort(value string) (Effort, bool) {
	normalized := Effort(strings.ToLower(strings.TrimSpace(value)))
	if normalized == EffortDefault {
		return EffortDefault, true
	}
	for _, level := range Efforts() {
		if normalized == level {
			return level, true
		}
	}
	return EffortDefault, false
}

// Describe returns the one-line rationale shown in pickers and help text.
func (e Effort) Describe() string {
	switch e {
	case EffortOff:
		return "no reasoning — fastest and cheapest"
	case EffortLow:
		return "minimal reasoning for short, scoped tasks"
	case EffortMedium:
		return "balanced reasoning for routine work"
	case EffortHigh:
		return "thorough reasoning — the provider default"
	case EffortXHigh:
		return "deeper reasoning, best for coding and agentic work"
	case EffortMax:
		return "maximum reasoning when correctness beats cost"
	default:
		return "provider default"
	}
}
