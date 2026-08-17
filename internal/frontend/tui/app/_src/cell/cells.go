package tui

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/frontend/tui/common"
	"github.com/jonathanung/strike-cli/internal/frontend/tui/theme"
	"github.com/jonathanung/strike-cli/internal/frontend/tui/ui"
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
	// copiedFlash is set after y-to-copy until clearCellCopiedFlashMsg.
	copiedFlash bool
}

func (c *userCell) copyText() string {
	if c == nil {
		return ""
	}
	return strings.TrimRight(c.text, "\n")
}

func (c *userCell) render(width int, th theme.Theme) string {
	th = th.Resolve()
	st := th.S()
	style := st.UserLabel
	prefix := messagePrefix(th, style)
	head := messageHead(th, style, "you")
	if c.copiedFlash {
		head += themedSpace(th.Spacing.XS) + st.Success.Render("copied")
	}
	if width > 0 && ansi.StringWidth(head) > width {
		head = ansi.Truncate(head, width, th.Icons.Ellipsis)
	}
	body := renderCellText(st.Text, c.text, max(1, width-lipgloss.Width(prefix)))
	return head + "\n" + indent(body, prefix)
}

type assistantCell struct {
	text     string
	complete bool // true after turn/tool boundary; markdown only when complete

	mdCache      string
	mdCacheKey   string
	mdCacheW     int
	mdCacheStyle string // glamour dark|light; style changes must miss cache
	mdCacheOK    bool
	// mdMisses counts full markdownRender paths (cache miss). Used by redraw
	// budget tests (#452/#495) so completed cells stay on the cache hit path.
	mdMisses int
	// copiedFlash is set after y-to-copy until clearCellCopiedFlashMsg.
	copiedFlash bool
}

func (c *assistantCell) copyText() string {
	if c == nil {
		return ""
	}
	return strings.TrimRight(c.text, "\n")
}

func (c *assistantCell) render(width int, th theme.Theme) string {
	th = th.Resolve()
	st := th.S()
	style := st.AssistantLabel
	prefix := messagePrefix(th, style)
	head := messageHead(th, style, "strike")
	if c.copiedFlash {
		head += themedSpace(th.Spacing.XS) + st.Success.Render("copied")
	}
	if width > 0 && ansi.StringWidth(head) > width {
		head = ansi.Truncate(head, width, th.Icons.Ellipsis)
	}
	bodyWidth := max(1, width-lipgloss.Width(prefix))
	src := strings.TrimSpace(c.text)
	var body string
	switch {
	case src == "":
		body = ""
	case !c.complete:
		// Plain text while streaming — avoid glamour on incomplete fences.
		body = renderCellText(st.Text, src, bodyWidth)
	case c.mdCacheOK && c.mdCacheKey == src && c.mdCacheW == bodyWidth && c.mdCacheStyle == glamourStyle():
		body = c.mdCache
	default:
		c.mdMisses++
		out, err := markdownRender(src, bodyWidth)
		if err != nil {
			body = renderCellText(st.Text, src, bodyWidth)
		} else {
			body = out
		}
		c.mdCache = body
		c.mdCacheKey = src
		c.mdCacheW = bodyWidth
		c.mdCacheStyle = glamourStyle()
		c.mdCacheOK = true
	}
	return head + "\n" + indent(body, prefix)
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
	// errorCode is the stable protocol.ErrorCode* when isError (e.g.
	// permission_denied). Empty on success.
	errorCode string
	expanded  bool
	selected  bool // highlight while transcript selection is on this cell
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

// hideToolOutputBody reports tools whose successful result body should stay
// off the transcript (header/title only). File contents still reach the model
// via the tool result; y-to-copy can still pull output from copyText.
func hideToolOutputBody(name string) bool {
	return name == "read"
}

// collapsible reports whether this finished tool has body content that can
// grow beyond the collapsed preview.
func (c *toolCell) collapsible() bool {
	if c == nil || !c.done {
		return false
	}
	if meta, ok := parseEditMetadata(c.metadata); ok {
		// Expand when the unified hunk exceeds the collapsed MaxLines window.
		return ui.DiffExceeds(meta.OldString, meta.NewString, diffPreviewMaxLinesCell) || c.expanded
	}
	// Successful reads are header-only in the chat (issue #746).
	if hideToolOutputBody(c.name) && !c.isError {
		return false
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
	return c.renderLinked(width, th, "")
}

// renderLinked is render with an optional linkBase for file:// OSC 8 targets
// on path-like titles (session work directory).
func (c *toolCell) renderLinked(width int, th theme.Theme, linkBase string) string {
	th = th.Resolve()
	ic := iconsFor(th)
	st := th.S()
	space := themedSpace(th.Spacing.XS)
	labelStyle := st.ToolLabel
	if c.selected {
		labelStyle = st.Selected
	}
	if c.isError {
		labelStyle = st.Error
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
	// Left-accent kicker + path/URL title (OSC 8 stays on the title span).
	prefix := messagePrefix(th, labelStyle)
	toolPart := labelStyle.Render(strings.ToUpper(c.name))
	var head string
	switch {
	case c.title != "":
		titleStyled := labelStyle.Render(ic.Dot + space + c.title)
		titleStyled = withHyperlink(displayURI(c.title, linkBase), titleStyled)
		head = toolPart + space + titleStyled
	case len(c.args) > 0:
		head = labelStyle.Render(strings.ToUpper(c.name) + space + compactJSON(c.args, 60, ic.Ellipsis))
	default:
		head = toolPart
	}
	out := prefix + marker + head + space + status
	// Clamp the head row so OSC 8 path titles cannot overflow the viewport
	// and get mid-sequence truncated by Panel (terminal corruption, #692).
	if width > 0 && ansi.StringWidth(out) > width {
		out = ansi.Truncate(out, width, ic.Ellipsis)
	}
	if c.done {
		bodyWidth := max(1, width-lipgloss.Width(prefix))
		if meta, ok := parseEditMetadata(c.metadata); ok {
			maxLines := diffPreviewMaxLinesCell
			moreHint := ""
			if c.expanded {
				maxLines = diffExpandedMaxLines(meta)
			} else if ui.DiffExceeds(meta.OldString, meta.NewString, diffPreviewMaxLinesCell) {
				moreHint = "alt+enter to expand"
			}
			path := c.title
			diff := ui.DiffPreview(th, ui.DiffPreviewOpts{
				Path:      path,
				Old:       meta.OldString,
				New:       meta.NewString,
				MaxLines:  maxLines,
				Width:     bodyWidth,
				ShowStats: true,
				LinkBase:  linkBase,
				MoreHint:  moreHint,
			})
			if diff != "" {
				out += "\n" + indent(diff, prefix)
			}
		} else if c.output != "" && !(hideToolOutputBody(c.name) && !c.isError) {
			// Successful read: header/title only — do not dump file contents.
			text := c.output
			if !c.expanded {
				text = previewLines(c.output, toolPreviewLines, ic.Ellipsis, space)
			} else {
				text = strings.TrimRight(c.output, "\n")
			}
			body := renderCellText(st.Muted, text, bodyWidth)
			out += "\n" + indent(body, prefix)
		}
	} else if c.output != "" && !(hideToolOutputBody(c.name) && !c.isError) {
		// Live bash (and other streaming tools): bounded tail while running.
		// Successful read stays header-only even if output streams early.
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
	return c.renderLinked(width, th, "")
}

func (c *exploreCell) renderLinked(width int, th theme.Theme, linkBase string) string {
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
	head := strings.ToUpper(label)
	if n > 0 {
		head = displayJoin(th, ic.Dot, strings.ToUpper(label), itoa(n)+space+"tools")
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
	prefix := messagePrefix(th, labelStyle)
	out := prefix + labelStyle.Render(glyph) + space + labelStyle.Render(head) + space + status
	if width > 0 && ansi.StringWidth(out) > width {
		out = ansi.Truncate(out, width, ic.Ellipsis)
	}
	if !c.expanded {
		return out
	}
	bodyWidth := max(1, width-lipgloss.Width(prefix))
	var lines []string
	for _, tc := range c.calls {
		if tc == nil {
			continue
		}
		mark := st.Muted.Render(ic.Ellipsis)
		if tc.done {
			if tc.isError {
				mark = st.Error.Render(ic.Err)
			} else {
				mark = st.Success.Render(ic.OK)
			}
		}
		markW := lipgloss.Width(space + mark)
		namePart := st.Muted.Render(tc.name)
		var row string
		if tc.title != "" {
			titleStyled := st.Muted.Render(ic.Dot + space + tc.title)
			titleStyled = withHyperlink(displayURI(tc.title, linkBase), titleStyled)
			row = namePart + space + titleStyled
		} else if len(tc.args) > 0 {
			row = st.Muted.Render(tc.name + space + compactJSON(tc.args, 40, ic.Ellipsis))
		} else {
			row = namePart
		}
		// Hardwrap only the plain width budget; OSC 8 spans stay on one visual line.
		_ = markW
		line := ansi.Cut(row, 0, max(1, bodyWidth-markW)) + space + mark
		lines = append(lines, line)
	}
	if len(lines) > 0 {
		out += "\n" + indent(strings.Join(lines, "\n"), prefix)
	}
	return out
}

func diffExpandedMaxLines(meta editDiffMeta) int {
	// Full unified body (equal + delete + insert), floored at the collapsed window.
	n := ui.DiffBodyLen(meta.OldString, meta.NewString)
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

// reasoningCell is model chain-of-thought. Visually muted and distinct from
// assistantCell so it is never mistaken for the final answer. Visibility is
// controlled by Model.showThinking; the cell is always retained for toggle-on.
type reasoningCell struct {
	text string
}

// thinkingPlaceholderVisible reports whether the live "thinking…" chrome
// should appear after the last user message: turn in flight, no assistant
// answer text, no tools, and (when CoT is shown) no reasoning cell yet.
func thinkingPlaceholderVisible(cells []cell, showThinking bool) bool {
	for i := len(cells) - 1; i >= 0; i-- {
		switch c := cells[i].(type) {
		case *userCell:
			return true
		case *assistantCell:
			if strings.TrimSpace(c.text) != "" {
				return false
			}
		case *reasoningCell:
			if showThinking && strings.TrimSpace(c.text) != "" {
				return false
			}
		case *toolCell, *exploreCell, *subagentResultCell:
			return false
		case *errorCell:
			return false
		}
	}
	return true
}

// renderThinkingPlaceholder is muted transcript chrome while the model has not
// produced answer text yet. Complements the header working spinner.
func renderThinkingPlaceholder(width int, th theme.Theme, started time.Time) string {
	th = th.Resolve()
	st := th.S()
	label := "thinking"
	if !started.IsZero() {
		label = detailJoin(th, "thinking", formatCompactDuration(time.Since(started)))
	}
	head := messageHead(th, st.Muted, label)
	if width > 0 && ansi.StringWidth(head) > width {
		return ansi.Truncate(head, width, th.Icons.Ellipsis)
	}
	return head
}

func (c *reasoningCell) copyText() string {
	if c == nil {
		return ""
	}
	return strings.TrimRight(c.text, "\n")
}

func (c *reasoningCell) render(width int, th theme.Theme) string {
	th = th.Resolve()
	st := th.S()
	style := st.Muted
	prefix := messagePrefix(th, style)
	head := messageHead(th, style, "thinking")
	src := strings.TrimSpace(c.text)
	if src == "" {
		return head
	}
	body := renderCellText(st.Muted, src, max(1, width-lipgloss.Width(prefix)))
	return head + "\n" + indent(body, prefix)
}

// infoCell is host feedback in the transcript (login URLs, device codes) —
// kept there rather than the one-line notice so it stays visible/copyable.
type infoCell struct {
	text string
}

func (c *infoCell) render(width int, th theme.Theme) string {
	th = th.Resolve()
	st := th.S()
	prefix := messagePrefix(th, st.Warning)
	head := messageHead(th, st.Warning, "info")
	text := common.PadWideGlyphs(c.text)
	body := renderCellText(st.Warning, text, max(1, width-lipgloss.Width(prefix)))
	return head + "\n" + indent(body, prefix)
}

type errorCell struct {
	text string
}

func (c *errorCell) render(width int, th theme.Theme) string {
	th = th.Resolve()
	st := th.S()
	prefix := messagePrefix(th, st.Error)
	head := messageHead(th, st.Error, "error")
	text := common.PadWideGlyphs(c.text)
	body := renderCellText(st.Error, text, max(1, width-lipgloss.Width(prefix)))
	return head + "\n" + indent(body, prefix)
}

func indent(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}

func renderCellText(style lipgloss.Style, text string, width int) string {
	// Word-wrap (not ansi.Hardwrap) so prose does not split mid-word across
	// lines (#460). Overlong tokens still hard-break inside ui.WrapText.
	// WrapText pads wide-neutral historic scripts (#689).
	// Sanitize first: live tool tails often carry CR progress bars and raw
	// ANSI that corrupt the alt-screen frame when embedded mid-row (#692).
	return style.Render(ui.WrapText(sanitizeTranscriptText(text), width))
}

// sanitizeTranscriptText strips terminal-breaking controls from untrusted
// transcript body text (tool output, streamed assistant plain text). Keeps
// newlines and tabs; normalizes CR to LF; drops ANSI/CSI/OSC and other C0/C1.
func sanitizeTranscriptText(s string) string {
	if s == "" {
		return s
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	// Drop embedded SGR/OSC from bash etc. before width measure + paint.
	s = ansi.Strip(s)
	if !strings.ContainsFunc(s, func(r rune) bool {
		return r != '\n' && r != '\t' && (r <= 0x1f || (r >= 0x7f && r <= 0x9f))
	}) {
		return s
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case r <= 0x1f || (r >= 0x7f && r <= 0x9f):
			return -1
		default:
			return r
		}
	}, s)
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
