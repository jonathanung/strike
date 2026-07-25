package tui

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"

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
	expanded bool
	selected bool // highlight while transcript selection is on this cell
	// copiedFlash is set after y-to-copy until clearCellCopiedFlashMsg.
	copiedFlash bool
}

const (
	toolPreviewLines  = 6
	toolLiveTailLines = 5
	// cellCopiedFlash is how long the "copied" label stays on a cell after y.
	cellCopiedFlash = 900 * time.Millisecond
)

// isExploreTool reports tools that group into an "exploring…" cell when
// consecutive (codex ExecCell reads/searches pattern).
func isExploreTool(name string) bool {
	switch name {
	case "read", "glob", "grep":
		return true
	default:
		return false
	}
}

// collapsible reports whether this finished tool has body content that can
// grow beyond the collapsed preview.
func (c *toolCell) collapsible() bool {
	if c == nil || !c.done {
		return false
	}
	if meta, ok := parseEditMetadata(c.metadata); ok {
		// Diff body can always expand past the cell MaxLines window when large.
		lines := strings.Count(meta.OldString, "\n") + strings.Count(meta.NewString, "\n") + 2
		return lines > diffPreviewMaxLinesCell || c.expanded
	}
	if c.output == "" {
		return false
	}
	return countLines(c.output) > toolPreviewLines || c.expanded
}

func (c *toolCell) toggleExpanded() bool {
	if c == nil || !c.collapsible() {
		return false
	}
	c.expanded = !c.expanded
	return true
}

// copyText returns clipboard payload for y-to-copy: edit diff, full output,
// command title, or compact args — empty when nothing useful is available.
func (c *toolCell) copyText() string {
	if c == nil {
		return ""
	}
	if meta, ok := parseEditMetadata(c.metadata); ok {
		return formatEditDiffCopy(meta)
	}
	if out := strings.TrimRight(c.output, "\n"); out != "" {
		return out
	}
	if c.title != "" {
		return c.title
	}
	if cmd := toolCommandArg(c.args); cmd != "" {
		return cmd
	}
	if len(c.args) > 0 {
		var buf bytes.Buffer
		if err := json.Compact(&buf, c.args); err == nil {
			return buf.String()
		}
		return string(c.args)
	}
	return ""
}

func (c *toolCell) render(width int, th theme.Theme) string {
	th = th.Resolve()
	ic := iconsFor(th)
	st := th.S()
	space := themedSpace(th.Spacing.XS)
	labelStyle := st.ToolLabel
	if c.selected {
		labelStyle = st.Selected
	}
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
	if c.copiedFlash {
		status = st.Success.Render("copied")
	}
	marker := ""
	if c.collapsible() {
		glyph := ic.TreeCollapsed
		if c.expanded {
			glyph = ic.TreeExpanded
		}
		marker = labelStyle.Render(glyph) + space
	}
	out := marker + labelStyle.Render(ic.Tool+space+head) + space + status
	if c.done {
		prefix := themedSpace(th.Spacing.SM) + st.BorderMuted.Render(ic.ToolGuide) + space
		bodyWidth := max(1, width-lipgloss.Width(prefix))
		if meta, ok := parseEditMetadata(c.metadata); ok {
			maxLines := diffPreviewMaxLinesCell
			if c.expanded {
				maxLines = diffExpandedMaxLines(meta)
			}
			diff := ui.DiffPreview(th, ui.DiffPreviewOpts{
				Path:      "",
				Old:       meta.OldString,
				New:       meta.NewString,
				MaxLines:  maxLines,
				Width:     bodyWidth,
				ShowStats: true,
			})
			if diff != "" {
				out += "\n" + indent(diff, prefix)
			}
		} else if c.output != "" {
			text := c.output
			if !c.expanded {
				text = previewLines(c.output, toolPreviewLines, ic.Ellipsis, space)
			} else {
				text = strings.TrimRight(c.output, "\n")
			}
			body := renderCellText(st.Muted, text, bodyWidth)
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

// exploreCell groups consecutive read/glob/grep tool calls into one transcript
// block ("exploring…" / "explored · N") that expands to list each call.
type exploreCell struct {
	calls     []*toolCell
	accepting bool // still absorbing consecutive explore tools
	expanded  bool
	selected  bool
	// copiedFlash is set after y-to-copy until clearCellCopiedFlashMsg.
	copiedFlash bool
}

func (c *exploreCell) collapsible() bool {
	return c != nil && len(c.calls) > 0
}

func (c *exploreCell) toggleExpanded() bool {
	if !c.collapsible() {
		return false
	}
	c.expanded = !c.expanded
	return true
}

// copyText lists each grouped tool call (name · title) for the clipboard.
func (c *exploreCell) copyText() string {
	if c == nil || len(c.calls) == 0 {
		return ""
	}
	var lines []string
	for _, tc := range c.calls {
		if tc == nil {
			continue
		}
		line := tc.name
		if tc.title != "" {
			line += " " + tc.title
		} else if out := strings.TrimSpace(tc.output); out != "" {
			if i := strings.IndexByte(out, '\n'); i >= 0 {
				out = out[:i]
			}
			line += " " + out
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func (c *exploreCell) allDone() bool {
	if len(c.calls) == 0 {
		return false
	}
	for _, tc := range c.calls {
		if tc == nil || !tc.done {
			return false
		}
	}
	return true
}

func (c *exploreCell) anyError() bool {
	for _, tc := range c.calls {
		if tc != nil && tc.done && tc.isError {
			return true
		}
	}
	return false
}

func (c *exploreCell) render(width int, th theme.Theme) string {
	th = th.Resolve()
	ic := iconsFor(th)
	st := th.S()
	space := themedSpace(th.Spacing.XS)
	labelStyle := st.ToolLabel
	if c.selected {
		labelStyle = st.Selected
	}
	n := len(c.calls)
	label := "exploring"
	if c.allDone() {
		label = "explored"
	}
	head := label
	if n > 0 {
		head = displayJoin(th, ic.Dot, label, itoa(n)+space+"tools")
	}
	status := st.Muted.Render(ic.Ellipsis)
	if c.allDone() {
		if c.anyError() {
			status = st.Error.Render(ic.Err)
		} else {
			status = st.Success.Render(ic.OK)
		}
	}
	if c.copiedFlash {
		status = st.Success.Render("copied")
	}
	glyph := ic.TreeCollapsed
	if c.expanded {
		glyph = ic.TreeExpanded
	}
	out := labelStyle.Render(glyph) + space + labelStyle.Render(ic.Tool+space+head) + space + status
	if !c.expanded {
		return out
	}
	prefix := themedSpace(th.Spacing.SM) + st.BorderMuted.Render(ic.ToolGuide) + space
	bodyWidth := max(1, width-lipgloss.Width(prefix))
	var lines []string
	for _, tc := range c.calls {
		if tc == nil {
			continue
		}
		row := tc.name
		if tc.title != "" {
			row = displayJoin(th, ic.Dot, tc.name, tc.title)
		} else if len(tc.args) > 0 {
			row += space + compactJSON(tc.args, 40, ic.Ellipsis)
		}
		mark := st.Muted.Render(ic.Ellipsis)
		if tc.done {
			if tc.isError {
				mark = st.Error.Render(ic.Err)
			} else {
				mark = st.Success.Render(ic.OK)
			}
		}
		line := st.Muted.Render(ansi.Hardwrap(row, max(1, bodyWidth-lipgloss.Width(space+mark)), false)) + space + mark
		lines = append(lines, line)
	}
	if len(lines) > 0 {
		out += "\n" + indent(strings.Join(lines, "\n"), prefix)
	}
	return out
}

func diffExpandedMaxLines(meta editDiffMeta) int {
	// Enough for every old/new line as a change row, plus headroom.
	n := strings.Count(meta.OldString, "\n") + strings.Count(meta.NewString, "\n") + 4
	if n < diffPreviewMaxLinesCell {
		return diffPreviewMaxLinesCell
	}
	return n
}

// formatEditDiffCopy builds a plain unified-style diff for the system clipboard.
func formatEditDiffCopy(meta editDiffMeta) string {
	var b strings.Builder
	writePrefixed := func(prefix, body string) {
		if body == "" {
			b.WriteString(prefix)
			b.WriteByte('\n')
			return
		}
		lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
		for _, line := range lines {
			b.WriteString(prefix)
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	writePrefixed("-", meta.OldString)
	writePrefixed("+", meta.NewString)
	return strings.TrimRight(b.String(), "\n")
}

// toolCommandArg extracts a bash-style "command" string from tool args JSON.
func toolCommandArg(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var parsed struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return ""
	}
	return strings.TrimSpace(parsed.Command)
}

func countLines(s string) int {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
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
