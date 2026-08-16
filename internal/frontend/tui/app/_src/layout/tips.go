package tui

import (
	"strings"
	"time"

	"github.com/jonathanung/strike-cli/internal/frontend/tui/theme"
)

// strikeTipParts are Strike-specific affordance segments joined with the theme
// Dot at render time (#664). No embedded glyph constants (style boundary SB006).
var strikeTipParts = [][]string{
	{"/ commands", "! shell", "@ file mentions"},
	{"/agent personas", "/context layers", "/diag export"},
	{"task / agent_roster for multi-agent work"},
	{"ctrl+e external editor", "paste images into the prompt"},
	{"/mode permission dial", "Shift+Tab cycles modes"},
	{"wait on task.stale", "don't busy-poll child status"},
}

// tipDayOverride, when >0, pins tip rotation for tests and frame goldens so
// day-of-year changes do not flake snapshots.
var tipDayOverride int

func tipDayOfYear() int {
	if tipDayOverride > 0 {
		return tipDayOverride
	}
	return time.Now().YearDay()
}

// pickStrikeTipParts returns tip segments for the given day-of-year so the strip
// rotates across days without flickering every frame.
func pickStrikeTipParts(doy int) []string {
	if len(strikeTipParts) == 0 {
		return nil
	}
	if doy < 1 {
		doy = 1
	}
	parts := strikeTipParts[(doy-1)%len(strikeTipParts)]
	out := make([]string, len(parts))
	copy(out, parts)
	return out
}

// formatStrikeTip joins tip segments with the theme Dot separator.
func formatStrikeTip(th theme.Theme, parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return dotJoin(th, parts...)
}

// showComposerTip is true when the tip strip should be considered for layout.
func (m Model) showComposerTip() bool {
	if m.modal != nil {
		return false
	}
	if strings.TrimSpace(m.composer.Value()) != "" {
		return false
	}
	// Error/info notices own the row above the composer — don't collide.
	if strings.TrimSpace(m.notice) != "" {
		return false
	}
	return true
}

// tipRowsFor returns 1 when a tip should be budgeted, else 0.
func (m Model) tipRowsFor() int {
	if !m.showComposerTip() {
		return 0
	}
	return 1
}

// tipView renders a single muted tip line above the prompt box.
func (m Model) tipView(width int) string {
	if width <= 0 || !m.showComposerTip() {
		return ""
	}
	th := m.th.Resolve()
	text := formatStrikeTip(th, pickStrikeTipParts(tipDayOfYear()))
	if text == "" {
		return ""
	}
	prefix := th.Icons.Info
	line := text
	if prefix != "" {
		line = prefix + themedSpace(th.Spacing.XS) + text
	}
	line = welcomeTruncate(line, width, th.Icons.Ellipsis)
	return th.S().Muted.Render(line)
}
