package tui

import (
	"time"

	"github.com/jonathanung/strike-cli/internal/frontend/tui/common"
	"github.com/jonathanung/strike-cli/internal/frontend/tui/theme"
)

func themedSpace(width int) string { return common.ThemedSpace(width) }

func displayJoin(th theme.Theme, separator string, fields ...string) string {
	return common.DisplayJoin(th, separator, fields...)
}

func dotJoin(th theme.Theme, fields ...string) string { return common.DotJoin(th, fields...) }

func detailJoin(th theme.Theme, label, detail string) string {
	return common.DetailJoin(th, label, detail)
}

func formatCompactDuration(d time.Duration) string { return common.FormatCompactDuration(d) }
