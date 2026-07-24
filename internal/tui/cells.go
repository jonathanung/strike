package tui

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

// cell is one transcript block. The transcript is an ordered list of cells,
// each responsible for its own rendering (codex "history cell" pattern).
type cell interface {
	render(width int, th theme.Theme) string
}

type userCell struct {
	text string
}

func (c *userCell) render(width int, th theme.Theme) string {
	label := lipgloss.NewStyle().Foreground(th.UserLabel).Bold(true).Render("❯ you")
	body := lipgloss.NewStyle().Foreground(th.Text).Width(width - 2).Render(c.text)
	return label + "\n" + indent(body, "  ")
}

type assistantCell struct {
	text string
}

func (c *assistantCell) render(width int, th theme.Theme) string {
	label := lipgloss.NewStyle().Foreground(th.Accent).Bold(true).Render("● strike")
	// Markdown rendering (glamour) lands in Phase 1; raw text for now.
	body := lipgloss.NewStyle().Foreground(th.Text).Width(width - 2).Render(strings.TrimSpace(c.text))
	return label + "\n" + indent(body, "  ")
}

type toolCell struct {
	callID  string
	name    string
	args    json.RawMessage
	title   string
	output  string
	done    bool
	isError bool
}

const toolPreviewLines = 6

func (c *toolCell) render(width int, th theme.Theme) string {
	labelStyle := lipgloss.NewStyle().Foreground(th.ToolLabel).Bold(true)
	head := c.name
	if c.title != "" {
		head += " · " + c.title
	} else if len(c.args) > 0 {
		head += " " + compactJSON(c.args, 60)
	}
	status := "…"
	if c.done {
		if c.isError {
			status = lipgloss.NewStyle().Foreground(th.Error).Render("✗")
		} else {
			status = lipgloss.NewStyle().Foreground(th.Success).Render("✓")
		}
	}
	out := labelStyle.Render("⚙ "+head) + " " + status
	if c.done && c.output != "" {
		preview := previewLines(c.output, toolPreviewLines)
		body := lipgloss.NewStyle().Foreground(th.TextMuted).Width(width - 4).Render(preview)
		out += "\n" + indent(body, "  │ ")
	}
	return out
}

// infoCell is host feedback in the transcript (login URLs, device codes) —
// kept there rather than the one-line notice so it stays visible/copyable.
type infoCell struct {
	text string
}

func (c *infoCell) render(width int, th theme.Theme) string {
	return lipgloss.NewStyle().Foreground(th.Warning).Width(width).Render("◦ " + c.text)
}

type errorCell struct {
	text string
}

func (c *errorCell) render(width int, th theme.Theme) string {
	return lipgloss.NewStyle().Foreground(th.Error).Width(width).Render("✗ " + c.text)
}

func indent(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}

func previewLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) <= n {
		return strings.Join(lines, "\n")
	}
	shown := strings.Join(lines[:n], "\n")
	return shown + "\n… (" + itoa(len(lines)-n) + " more lines)"
}

func compactJSON(raw json.RawMessage, maxLen int) string {
	s := string(raw)
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err == nil {
		s = buf.String()
	}
	if len(s) > maxLen {
		s = s[:maxLen] + "…"
	}
	return s
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
