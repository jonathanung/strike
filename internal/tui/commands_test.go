package tui

import (
	"testing"

	"github.com/jonathanung/strike-cli/internal/config"
)

func TestCommandCatalogContainsBuiltinsAndSkillsOnceWithMetadata(t *testing.T) {
	skills := []config.Skill{
		{Name: "review", Description: "review a change", Template: "Review $ARGUMENTS"},
		{Name: "explain", Description: "explain code", Template: "Explain this"},
	}
	catalog := commandCatalog(skills)

	want := map[string]struct {
		description string
		argsHint    string
		source      commandSource
	}{
		"/provider": {"select a provider and model", "[name [model]]", commandSourceBuiltin},
		"/model":    {"select a model for the current provider", "[model]", commandSourceBuiltin},
		"/auth":     {"manage provider authentication", "[provider]", commandSourceBuiltin},
		"/agent":    {"select an agent", "[name]", commandSourceBuiltin},
		"/help":     {"show available commands", "", commandSourceBuiltin},
		"/review":   {"review a change", "$ARGUMENTS", commandSourceSkill},
		"/explain":  {"explain code", "", commandSourceSkill},
	}
	counts := make(map[string]int)
	for _, spec := range catalog {
		counts[spec.Name]++
		metadata, ok := want[spec.Name]
		if !ok {
			t.Errorf("unexpected command %q", spec.Name)
			continue
		}
		if spec.Description != metadata.description || spec.ArgsHint != metadata.argsHint || spec.Source != metadata.source {
			t.Errorf("%s metadata = (%q, %q, %q), want (%q, %q, %q)", spec.Name, spec.Description, spec.ArgsHint, spec.Source, metadata.description, metadata.argsHint, metadata.source)
		}
	}
	if len(catalog) != len(want) {
		t.Fatalf("catalog length = %d, want %d", len(catalog), len(want))
	}
	for name := range want {
		if counts[name] != 1 {
			t.Errorf("catalog contains %s %d times, want exactly once", name, counts[name])
		}
	}
}
