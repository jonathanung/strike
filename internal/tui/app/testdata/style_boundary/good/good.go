package fixture

import (
	"context"

	"charm.land/lipgloss/v2"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

type local struct{}

func (local) Bold(bool) {}

func invoke(fn func(bool)) { fn(true) }

func f(th theme.Theme) string {
	resolved := th.Resolve()
	resolvedLocal := resolved
	accent := resolvedLocal.Accent
	spacing := resolvedLocal.Spacing.SM
	defaultAccent := theme.Default().Accent
	styles := th.S()
	st := lipgloss.NewStyle().Foreground(defaultAccent).Foreground(accent).Padding(spacing)
	_ = context.Background()
	local{}.Bold(true)
	invoke(local{}.Bold)
	return styles.Text.Copy().Inherit(st).Width(20).Height(2).MaxWidth(30).Render("ok")
}

func structural() string { return lipgloss.JoinHorizontal(lipgloss.Top, "a", "b") }

func prose() string { return "A sentence — with ordinary prose." }
