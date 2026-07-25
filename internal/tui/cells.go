package tui

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

// cell is one transcript block. The transcript is an ordered list of cells,
// each responsible for its own rendering (codex "history cell" pattern). Cells
// draw from theme.Icons for glyphs and theme.Styles for colors, never inline
// literals.
type cell interface {
	render(width int, th theme.Theme) string
}

type userCell struct {
	text string
}

func (c *userCell) render(width int, th theme.Theme) string {
	th = th.Resolve()
	ic := iconsFor(th)
	st := th.S()
	space := themedSpace(th.Spacing.XS)
	indentation := themedSpace(th.Spacing.SM)
	label := st.UserLabel.Render(ic.Prompt + space + "you")
	body := renderCellText(st.Text, c.text, max(1, width-lipgloss.Width(indentation)))
	return label + "\n" + indent(body, indentation)
}

type assistantCell struct {
	text     string
	complete bool // true after turn/tool boundary; markdown only when complete

	mdCache    string
	mdCacheKey string
	mdCacheW   int
	mdCacheOK  bool
}

func (c *assistantCell) render(width int, th theme.Theme) string {
	th = th.Resolve()
	ic := iconsFor(th)
	st := th.S()
	space := themedSpace(th.Spacing.XS)
	indentation := themedSpace(th.Spacing.SM)
	label := st.AssistantLabel.Render(ic.Assistant + space + "strike")
	bodyWidth := max(1, width-lipgloss.Width(indentation))
	src := strings.TrimSpace(c.text)
	var body string
	switch {
	case src == "":
		body = ""
	case !c.complete:
		// Plain text while streaming — avoid glamour on incomplete fences.
		body = renderCellText(st.Text, src, bodyWidth)
	case c.mdCacheOK && c.mdCacheKey == src && c.mdCacheW == bodyWidth:
		body = c.mdCache
	default:
		out, err := markdownRender(src, bodyWidth)
		if err != nil {
			body = renderCellText(st.Text, src, bodyWidth)
		} else {
			body = out
		}
		c.mdCache = body
		c.mdCacheKey = src
		c.mdCacheW = bodyWidth
		c.mdCacheOK = true
	}
	return label + "\n" + indent(body, indentation)
}

type toolCell struct {
	callID   string
	name     string
	args     json.RawMessage
	title    string
	output   string
	metadata json.RawMessage
	done     bool
	isError  bool
}

const (
	toolPreviewLines  = 6
	toolLiveTailLines = 5
)

func (c *toolCell) render(width int, th theme.Theme) string {
	th = th.Resolve()
	ic := iconsFor(th)
	st := th.S()
	space := themedSpace(th.Spacing.XS)
	labelStyle := st.ToolLabel
	head := c.name
	if c.title != "" {
		head = displayJoin(th, ic.Dot, head, c.title)
	} else if len(c.args) > 0 {
		head += space + compactJSON(c.args, 60, ic.Ellipsis)
	}
	status := st.Muted.Render(ic.Ellipsis)
	if c.done {
		if c.isError {
			status = st.Error.Render(ic.Err)
		} else {
			status = st.Success.Render(ic.OK)
		}
	}
	out := labelStyle.Render(ic.Tool+space+head) + space + status
	if c.done {
		prefix := themedSpace(th.Spacing.SM) + st.BorderMuted.Render(ic.ToolGuide) + space
		bodyWidth := max(1, width-lipgloss.Width(prefix))
		if meta, ok := parseEditMetadata(c.metadata); ok {
			diff := ui.DiffPreview(th, ui.DiffPreviewOpts{
				Path:      "",
				Old:       meta.OldString,
				New:       meta.NewString,
				MaxLines:  diffPreviewMaxLinesCell,
				Width:     bodyWidth,
				ShowStats: true,
			})
			if diff != "" {
				out += "\n" + indent(diff, prefix)
			}
		} else if c.output != "" {
			preview := previewLines(c.output, toolPreviewLines, ic.Ellipsis, space)
			body := renderCellText(st.Muted, preview, bodyWidth)
			out += "\n" + indent(body, prefix)
		}
	} else if c.output != "" {
		// Live bash (and other streaming tools): bounded tail while running.
		prefix := themedSpace(th.Spacing.SM) + st.BorderMuted.Render(ic.ToolGuide) + space
		bodyWidth := max(1, width-lipgloss.Width(prefix))
		body := renderCellText(st.Muted, tailLines(c.output, toolLiveTailLines), bodyWidth)
		out += "\n" + indent(body, prefix)
	}
	return out
}

// infoCell is host feedback in the transcript (login URLs, device codes) —
// kept there rather than the one-line notice so it stays visible/copyable.
type infoCell struct {
	text string
}

func (c *infoCell) render(width int, th theme.Theme) string {
	th = th.Resolve()
	ic := iconsFor(th)
	return th.S().Warning.Width(max(1, width)).Render(ic.Info + themedSpace(th.Spacing.XS) + c.text)
}

type errorCell struct {
	text string
}

func (c *errorCell) render(width int, th theme.Theme) string {
	th = th.Resolve()
	ic := iconsFor(th)
	return th.S().Error.Width(max(1, width)).Render(ic.Err + themedSpace(th.Spacing.XS) + c.text)
}

func indent(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}

func renderCellText(style lipgloss.Style, text string, width int) string {
	return style.Render(ansi.Hardwrap(text, width, false))
}

func previewLines(s string, n int, ellipsis, space string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) <= n {
		return strings.Join(lines, "\n")
	}
	shown := strings.Join(lines[:n], "\n")
	return shown + "\n" + ellipsis + space + "(" + itoa(len(lines)-n) + space + "more lines)"
}

// tailLines returns the last n lines of s (no ellipsis), for live tool tails.
func tailLines(s string, n int) string {
	s = strings.TrimRight(s, "\n")
	if s == "" || n <= 0 {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return strings.Join(lines, "\n")
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}

func compactJSON(raw json.RawMessage, maxLen int, ellipsis string) string {
	s := string(raw)
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err == nil {
		s = buf.String()
	}
	if len(s) > maxLen {
		s = s[:maxLen] + ellipsis
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
