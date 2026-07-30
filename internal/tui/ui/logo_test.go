package ui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

func TestLogoIsCompactMultiLineWordmark(t *testing.T) {
	out := Logo(theme.Default())
	lines := strings.Split(out, "\n")
	if len(lines) < 1 || len(lines) > 5 {
		t.Errorf("logo has %d lines, want 1..5", len(lines))
	}
	if w := lipgloss.Width(out); w > 28 {
		t.Errorf("logo width %d exceeds 28 columns", w)
	}
	if !strings.Contains(out, "⚡") {
		t.Error("logo missing the bolt motif")
	}
	if !strings.Contains(out, "S T R I K E") {
		t.Error("logo missing the wordmark")
	}
}

func TestLogoCompactIsSingleLine(t *testing.T) {
	out := LogoCompact(theme.Default())
	if strings.Contains(out, "\n") {
		t.Errorf("compact logo should be one line: %q", out)
	}
	if !strings.Contains(out, "⚡") || !strings.Contains(out, "strike") {
		t.Errorf("compact logo content = %q", out)
	}
	if w := lipgloss.Width(out); w > 28 {
		t.Errorf("compact logo width %d exceeds 28", w)
	}
}

func TestLogoZeroThemeFallsBackToDefaultIcons(t *testing.T) {
	if out := Logo(theme.Theme{}); !strings.Contains(out, "⚡") {
		t.Errorf("zero-theme logo lost the bolt; icon fallback failed: %q", out)
	}
}

func TestLogoUsesCustomBoltIcon(t *testing.T) {
	th := theme.Default()
	th.Icons.Bolt = "*"
	for name, out := range map[string]string{"full": Logo(th), "compact": LogoCompact(th)} {
		if !strings.Contains(out, "*") {
			t.Errorf("%s logo omitted custom bolt: %q", name, out)
		}
		if strings.Contains(out, "⚡") {
			t.Errorf("%s logo retained default bolt: %q", name, out)
		}
	}
}

func TestLogoUsesCustomRuleGlyphs(t *testing.T) {
	th := theme.Default()
	th.Icons.LogoTopRule = "-"
	th.Icons.LogoBottomRule = "="
	lines := strings.Split(Logo(th), "\n")
	if !strings.Contains(lines[0], "-") || !strings.Contains(lines[len(lines)-1], "=") {
		t.Errorf("logo did not use custom rules: %q", Logo(th))
	}
}
