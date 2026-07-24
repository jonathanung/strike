package provider_test

import (
	"testing"

	"github.com/jonathanung/strike-cli/internal/provider"
)

func TestParseEffortAcceptsEveryLevelCaseInsensitively(t *testing.T) {
	for _, level := range provider.Efforts() {
		for _, spelling := range []string{string(level), "  " + string(level) + "  ", upper(string(level))} {
			got, ok := provider.ParseEffort(spelling)
			if !ok {
				t.Errorf("ParseEffort(%q) rejected", spelling)
				continue
			}
			if got != level {
				t.Errorf("ParseEffort(%q) = %q, want %q", spelling, got, level)
			}
		}
	}
}

func TestParseEffortEmptyIsDefaultAndUnknownIsRejected(t *testing.T) {
	if got, ok := provider.ParseEffort(""); !ok || got != provider.EffortDefault {
		t.Errorf("ParseEffort(\"\") = (%q, %v), want (\"\", true)", got, ok)
	}
	for _, bad := range []string{"none", "highest", "xxhigh", "1", "default"} {
		if got, ok := provider.ParseEffort(bad); ok {
			t.Errorf("ParseEffort(%q) = (%q, true), want rejected", bad, got)
		}
	}
}

// TestEffortsExcludesTheUnsetSentinel keeps the picker from offering "" as a
// choice — it means "leave the provider alone", not a level.
func TestEffortsExcludesTheUnsetSentinel(t *testing.T) {
	for _, level := range provider.Efforts() {
		if level == provider.EffortDefault {
			t.Fatal("Efforts() contains the unset sentinel")
		}
		if level.Describe() == "" {
			t.Errorf("%q has no description", level)
		}
	}
}

func upper(s string) string {
	out := []rune(s)
	for i, r := range out {
		if r >= 'a' && r <= 'z' {
			out[i] = r - 32
		}
	}
	return string(out)
}
