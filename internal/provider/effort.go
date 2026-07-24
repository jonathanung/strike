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
	// EffortOff asks for as little reasoning as the vendor allows, rather
	// than leaving it to the provider default (which, on current Claude
	// models, is on). How far "off" actually goes is vendor-dependent:
	// Anthropic disables thinking outright, while the OpenAI family has no
	// zero setting and floors at "minimal".
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

// User-facing descriptions of these levels deliberately live on
// protocol.Effort, not here: they are frontend copy, and one owner keeps the
// picker, the notice line, and the help text from drifting apart.
