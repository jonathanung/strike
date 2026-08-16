package base_test

import (
	"testing"

	"github.com/jonathanung/strike-cli/harness/provider"
	"github.com/jonathanung/strike-cli/providers/base"
)

// TestOpenAIEffortClampsAtBothEnds documents the deliberate lossiness: the
// OpenAI family's ladder is shorter at both ends, so the two rungs above
// "high" clamp down, and "off" floors at "minimal" — this vendor has no zero
// setting, so it cannot honor "off" as literally as Anthropic does.
func TestOpenAIEffortClampsAtBothEnds(t *testing.T) {
	cases := map[provider.Effort]string{
		provider.EffortDefault: "",
		provider.EffortOff:     "minimal",
		provider.EffortLow:     "low",
		provider.EffortMedium:  "medium",
		provider.EffortHigh:    "high",
		provider.EffortXHigh:   "high",
		provider.EffortMax:     "high",
	}
	for effort, want := range cases {
		if got := base.OpenAIEffort(effort); got != want {
			t.Errorf("OpenAIEffort(%q) = %q, want %q", effort, got, want)
		}
	}
}

// TestOpenAIEffortCoversEveryLevel fails when a new rung is added to the
// ladder without deciding how the OpenAI family should spell it — the default
// branch would otherwise silently omit the field.
func TestOpenAIEffortCoversEveryLevel(t *testing.T) {
	for _, level := range provider.Efforts() {
		if base.OpenAIEffort(level) == "" {
			t.Errorf("OpenAIEffort(%q) is empty; add an explicit mapping", level)
		}
	}
}
