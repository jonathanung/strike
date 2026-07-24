package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/config"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
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

func TestCommandCatalogSanitizesUntrustedSkillDescription(t *testing.T) {
	description := "Résumé 世界\x1b]52;c;secret\x07 tail\x1b[31m red\u009b31m line\nnext\tcell " + string([]byte{0xff})
	catalog := commandCatalog([]config.Skill{{Name: "inspect", Description: description}})
	spec := catalog[len(catalog)-1]
	want := "Résumé 世界�]52;c;secret� tail�[31m red�31m line�next�cell �"
	if spec.Description != want {
		t.Errorf("sanitized description = %q, want %q", spec.Description, want)
	}
}

func TestSlashCompletionViewDoesNotRenderDescriptionControlPayloadAsTerminalMetadataOrRows(t *testing.T) {
	description := "ordinary 世界\x1b]52;c;copied\x07\x1b[31m\u009b31m\ninjected-row\tcell"
	catalog := commandCatalog([]config.Skill{{Name: "inspect", Description: description}})
	assertNoUntrustedTerminalControls(t, catalog[len(catalog)-1].Description)
	completion := leadingSlashCompletion("/inspect", 0, len([]rune("/inspect")), catalog)
	if completion == nil {
		t.Fatal("completion did not open for valid skill")
	}
	completion.rows = 1
	rendered := completion.view(120, theme.Default())
	if strings.Contains(rendered, "\x1b]52;c;copied\x07") {
		t.Fatalf("completion rendered attacker OSC52 sequence: %q", rendered)
	}
	plain := ansi.Strip(rendered)
	assertNoUntrustedTerminalControls(t, plain)
	assertPayloadRemainsOnDescriptionRow(t, plain, "ordinary 世界", "injected-row")
}

func assertNoUntrustedTerminalControls(t *testing.T, value string) {
	t.Helper()
	for _, r := range value {
		if r == '\n' {
			continue
		}
		if r <= '\u001f' || r >= '\u007f' && r <= '\u009f' {
			t.Errorf("rendered output contains untrusted control U+%04X: %q", r, value)
		}
	}
}

func assertPayloadRemainsOnDescriptionRow(t *testing.T, value, prefix, payload string) {
	t.Helper()
	matchingRows := 0
	for _, line := range strings.Split(value, "\n") {
		if strings.Contains(line, payload) {
			matchingRows++
			if !strings.Contains(line, prefix) {
				t.Errorf("description newline created an injected row %q", line)
			}
		}
	}
	if matchingRows != 1 {
		t.Errorf("payload appeared on %d rows, want exactly one sanitized description row: %q", matchingRows, value)
	}
}
