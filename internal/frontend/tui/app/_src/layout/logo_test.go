package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/jonathanung/strike-cli/internal/frontend/tui/theme"
)

func TestLogoIsCompactMultiLineWordmark(t *testing.T) {
	out := Logo(theme.Default())
	lines := strings.Split(out, "\n")
	if len(lines) != 1 {
		t.Errorf("logo has %d lines, want 1", len(lines))
	}
	if w := lipgloss.Width(out); w > 28 {
		t.Errorf("logo width %d exceeds 28 columns", w)
	}
	if !strings.Contains(out, "S T R I K E") {
		t.Error("logo missing the letter-spaced wordmark")
	}
}

func TestLogoCompactIsSingleLine(t *testing.T) {
	out := LogoCompact(theme.Default())
	if strings.Contains(out, "\n") {
		t.Errorf("compact logo should be one line: %q", out)
	}
	if !strings.Contains(out, "S") || !strings.Contains(out, "STRIKE") {
		t.Errorf("compact logo content = %q", out)
	}
	if w := lipgloss.Width(out); w > 28 {
		t.Errorf("compact logo width %d exceeds 28", w)
	}
}

func TestLogoZeroThemeFallsBackToDefaultIcons(t *testing.T) {
	if out := Logo(theme.Theme{}); !strings.Contains(out, "S T R I K E") {
		t.Errorf("zero-theme logo lost the wordmark: %q", out)
	}
}

func TestLogoCompactUsesTitleToken(t *testing.T) {
	th := theme.Default()
	out := LogoCompact(th)
	if !strings.Contains(out, "STRIKE") {
		t.Errorf("compact logo missing STRIKE: %q", out)
	}
}

func TestLogoUsesResolvedSpacing(t *testing.T) {
	th := theme.Default()
	th.Spacing = th.Spacing.WithXS(2)
	out := Logo(th)
	if !strings.Contains(out, "S  T  R  I  K  E") {
		t.Errorf("logo did not honor XS letter-spacing: %q", out)
	}
}
