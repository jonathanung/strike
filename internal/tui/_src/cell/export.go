package tui

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/jonathanung/strike-cli/pkg/redact"
)

const (
	exportToolOutputMaxLines = 40
	exportToolOutputMaxBytes = 8 << 10
)

// exportMeta is header identity for a markdown transcript dump.
type exportMeta struct {
	SessionID string
	Title     string
	Exported  time.Time
	Provider  string
	Model     string
	Agent     string
}

// exportFinishedMsg is delivered after async markdown export completes.
type exportFinishedMsg struct {
	path string
	err  error
	open bool
}

// redactSecrets replaces common credential shapes with placeholders via
// pkg/redact (shared with timeline export, session scrub, and #796).
func redactSecrets(s string) string {
	return redact.String(s)
}

// parseExportArgs accepts: [path] | [path] --open | --open [path] | --open
func parseExportArgs(args []string) (path string, open bool, err error) {
	var paths []string
	for _, a := range args {
		switch a {
		case "--open", "-o":
			open = true
		case "--help", "-h":
			return "", false, fmt.Errorf("usage: /export [path] [--open]")
		default:
			if strings.HasPrefix(a, "-") {
				return "", false, fmt.Errorf("usage: /export [path] [--open]")
			}
			paths = append(paths, a)
		}
	}
	if len(paths) > 1 {
		return "", false, fmt.Errorf("usage: /export [path] [--open]")
	}
	if len(paths) == 1 {
		path = paths[0]
	}
	return path, open, nil
}

// defaultExportPath picks project .strike/exports/ when workDir is set, else tmp.
func defaultExportPath(workDir, sessionID string, now time.Time) string {
	if now.IsZero() {
		now = time.Now()
	}
	stamp := now.UTC().Format("20060102-150405")
	short := shortSessionID(sessionID)
	if short == "" {
		short = "session"
	}
	name := fmt.Sprintf("strike-%s-%s.md", short, stamp)
	workDir = strings.TrimSpace(workDir)
	if workDir != "" {
		return filepath.Join(workDir, ".strike", "exports", name)
	}
	return filepath.Join(os.TempDir(), name)
}

// resolveExportPath cleans path. Relative paths stay under workDir when set.
func resolveExportPath(workDir, path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("path is empty")
	}
	if strings.IndexByte(path, 0) >= 0 {
		return "", fmt.Errorf("path contains invalid character")
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	workDir = strings.TrimSpace(workDir)
	if workDir == "" {
		abs, err := filepath.Abs(path)
		if err != nil {
			return "", fmt.Errorf("resolve path: %w", err)
		}
		return abs, nil
	}
	root := filepath.Clean(workDir)
	resolved := filepath.Clean(filepath.Join(root, path))
	sep := string(os.PathSeparator)
	if resolved != root && !strings.HasPrefix(resolved, root+sep) {
		return "", fmt.Errorf("path escapes project root")
	}
	return resolved, nil
}

// writeExportMarkdown streams a redacted markdown transcript to path, creating
// parent directories as needed. Uses a temp file + rename for atomic replace.
func writeExportMarkdown(path string, cells []cell, meta exportMeta) error {
	path = filepath.Clean(path)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create export directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".strike-export-*.tmp")
	if err != nil {
		return fmt.Errorf("create export temp: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()
	bw := bufio.NewWriterSize(tmp, 64*1024)
	if err := writeTranscriptMarkdown(bw, cells, meta); err != nil {
		return err
	}
	if err := bw.Flush(); err != nil {
		return fmt.Errorf("write export: %w", err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		return fmt.Errorf("chmod export temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync export temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close export temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace export file: %w", err)
	}
	cleanup = false
	return nil
}

// writeTranscriptMarkdown writes ordered turns as markdown to w.
func writeTranscriptMarkdown(w io.Writer, cells []cell, meta exportMeta) error {
	var b strings.Builder
	b.WriteString("# Strike session export\n\n")
	if id := strings.TrimSpace(meta.SessionID); id != "" {
		fmt.Fprintf(&b, "- **Session:** `%s`\n", id)
	}
	if title := strings.TrimSpace(meta.Title); title != "" {
		fmt.Fprintf(&b, "- **Title:** %s\n", redactSecrets(escapeMDOneLine(title)))
	}
	if meta.Provider != "" || meta.Model != "" {
		pm := strings.TrimSpace(meta.Provider)
		if meta.Model != "" {
			if pm != "" {
				pm += " / "
			}
			pm += meta.Model
		}
		fmt.Fprintf(&b, "- **Model:** %s\n", escapeMDOneLine(pm))
	}
	if agent := strings.TrimSpace(meta.Agent); agent != "" {
		fmt.Fprintf(&b, "- **Agent:** %s\n", escapeMDOneLine(agent))
	}
	exported := meta.Exported
	if exported.IsZero() {
		exported = time.Now().UTC()
	}
	fmt.Fprintf(&b, "- **Exported:** %s\n", exported.UTC().Format(time.RFC3339))
	b.WriteString("\n---\n\n")
	if _, err := io.WriteString(w, b.String()); err != nil {
		return fmt.Errorf("write export header: %w", err)
	}

	if len(cells) == 0 {
		if _, err := io.WriteString(w, "_Empty transcript._\n"); err != nil {
			return fmt.Errorf("write export body: %w", err)
		}
		return nil
	}

	for i, c := range cells {
		section := cellMarkdown(c)
		if section == "" {
			continue
		}
		if i > 0 {
			if _, err := io.WriteString(w, "\n"); err != nil {
				return fmt.Errorf("write export body: %w", err)
			}
		}
		if _, err := io.WriteString(w, section); err != nil {
			return fmt.Errorf("write export body: %w", err)
		}
		if !strings.HasSuffix(section, "\n") {
			if _, err := io.WriteString(w, "\n"); err != nil {
				return fmt.Errorf("write export body: %w", err)
			}
		}
	}
	return nil
}

func cellMarkdown(c cell) string {
	switch cell := c.(type) {
	case *userCell:
		body := strings.TrimRight(redactSecrets(cell.text), "\n")
		if body == "" {
			return "## You\n\n_Empty message._\n"
		}
		return "## You\n\n" + body + "\n"
	case *assistantCell:
		body := strings.TrimRight(redactSecrets(cell.text), "\n")
		if body == "" {
			return "## Strike\n\n_Empty response._\n"
		}
		return "## Strike\n\n" + body + "\n"
	case *reasoningCell:
		body := strings.TrimRight(redactSecrets(cell.text), "\n")
		if body == "" {
			return ""
		}
		return "### Thinking\n\n" + fencedBlock(body) + "\n"
	case *toolCell:
		return toolCellMarkdown(cell)
	case *exploreCell:
		return exploreCellMarkdown(cell)
	case *subagentResultCell:
		return subagentResultCellMarkdown(cell)
	case *infoCell:
		body := strings.TrimSpace(redactSecrets(cell.text))
		if body == "" {
			return ""
		}
		return "### Info\n\n" + body + "\n"
	case *errorCell:
		body := strings.TrimSpace(redactSecrets(cell.text))
		if body == "" {
			return ""
		}
		return "### Error\n\n" + body + "\n"
	default:
		return ""
	}
}

func toolCellMarkdown(c *toolCell) string {
	if c == nil {
		return ""
	}
	status := "running"
	if c.done {
		if c.isError {
			status = "error"
		} else {
			status = "ok"
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "### Tool: `%s` (%s)\n\n", sanitizeMDIdent(c.name), status)
	if title := strings.TrimSpace(redactSecrets(c.title)); title != "" {
		fmt.Fprintf(&b, "- **Detail:** %s\n", escapeMDOneLine(title))
	} else if cmd := redactSecrets(toolCommandArg(c.args)); cmd != "" {
		fmt.Fprintf(&b, "- **Command:** %s\n", inlineCode(cmd))
	} else if summary := toolArgsSummary(c.args); summary != "" {
		fmt.Fprintf(&b, "- **Args:** %s\n", escapeMDOneLine(redactSecrets(summary)))
	}
	if out := summarizeToolOutput(c.output); out != "" {
		b.WriteString("\n")
		b.WriteString(fencedBlock(out))
		b.WriteByte('\n')
	} else if b.Len() > 0 && !strings.HasSuffix(b.String(), "\n\n") {
		// ensure trailing newline after bullet-only tools
	}
	return b.String()
}

func subagentResultCellMarkdown(c *subagentResultCell) string {
	if c == nil {
		return ""
	}
	agent := strings.TrimSpace(c.agent)
	if agent == "" {
		agent = "subagent"
	}
	status := strings.TrimSpace(c.status)
	if status == "" {
		status = "completed"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "### Subagent: `%s` (%s)\n\n", sanitizeMDIdent(agent), status)
	if short := shortSessionID(c.sessionID); short != "" {
		fmt.Fprintf(&b, "- **Session:** `%s`\n", sanitizeMDIdent(short))
	}
	if c.elapsed > 0 {
		fmt.Fprintf(&b, "- **Elapsed:** %s\n", formatCompactDuration(c.elapsed))
	}
	if out := summarizeToolOutput(c.summary); out != "" {
		b.WriteString("\n")
		b.WriteString(fencedBlock(out))
		b.WriteByte('\n')
	}
	return b.String()
}

func exploreCellMarkdown(c *exploreCell) string {
	if c == nil || len(c.calls) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "### Exploring (%d)\n\n", len(c.calls))
	for _, tc := range c.calls {
		if tc == nil {
			continue
		}
		line := tc.name
		if title := strings.TrimSpace(redactSecrets(tc.title)); title != "" {
			line += " - " + title
		} else if cmd := redactSecrets(toolCommandArg(tc.args)); cmd != "" {
			line += " - " + cmd
		}
		fmt.Fprintf(&b, "- %s\n", escapeMDOneLine(line))
	}
	return b.String()
}

func toolArgsSummary(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		s := string(raw)
		return truncateRunes(s, 120)
	}
	return truncateRunes(buf.String(), 120)
}

func summarizeToolOutput(out string) string {
	out = strings.TrimRight(redactSecrets(out), "\n")
	if out == "" {
		return ""
	}
	if len(out) > exportToolOutputMaxBytes {
		// Keep head; note truncation in runes-safe way via bytes then clean.
		cut := out[:exportToolOutputMaxBytes]
		if i := strings.LastIndexByte(cut, '\n'); i > exportToolOutputMaxBytes/2 {
			cut = cut[:i]
		}
		// Avoid splitting a multibyte rune at the cut.
		for len(cut) > 0 && !utf8.ValidString(cut) {
			cut = cut[:len(cut)-1]
		}
		out = cut + "\n... (truncated)"
	}
	lines := strings.Split(out, "\n")
	if len(lines) > exportToolOutputMaxLines {
		out = strings.Join(lines[:exportToolOutputMaxLines], "\n") + "\n... (truncated)"
	}
	return out
}

func fencedBlock(body string) string {
	body = strings.TrimRight(body, "\n")
	ticks := "```"
	for strings.Contains(body, ticks) {
		ticks += "`"
	}
	return ticks + "\n" + body + "\n" + ticks
}

func inlineCode(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.TrimSpace(s)
	if s == "" {
		return "` `"
	}
	if !strings.Contains(s, "`") {
		return "`" + s + "`"
	}
	return "`` " + s + " ``"
}

func escapeMDOneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	return strings.TrimSpace(s)
}

func sanitizeMDIdent(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "tool"
	}
	return strings.Map(func(r rune) rune {
		if r == '`' {
			return '\''
		}
		if r == '\n' || r == '\r' {
			return -1
		}
		return r
	}, s)
}

func (m Model) handleExportCommand(args []string) (tea.Model, tea.Cmd) {
	m.resetComposer()
	m.clearNotice()
	pathArg, open, err := parseExportArgs(args)
	if err != nil {
		m.setNotice(err.Error(), true)
		return m, nil
	}
	cells := m.displayCells()
	if len(cells) == 0 {
		m.setNotice("nothing to export — transcript is empty", true)
		return m, nil
	}
	var path string
	if pathArg == "" {
		path = defaultExportPath(m.workDir, m.currentViewID(), time.Now())
	} else {
		resolved, rerr := resolveExportPath(m.workDir, pathArg)
		if rerr != nil {
			m.setNotice("export: "+rerr.Error(), true)
			return m, nil
		}
		path = resolved
	}
	meta := exportMeta{
		SessionID: m.currentViewID(),
		Title:     m.exportTitle(),
		Exported:  time.Now().UTC(),
		Provider:  m.providerName,
		Model:     m.modelName,
		Agent:     m.agentName,
	}
	// Snapshot cells for the async writer so live updates cannot race the dump.
	snap := append([]cell(nil), cells...)
	m.setNotice("exporting…", false)
	return m, exportMarkdownCmd(path, snap, meta, open)
}

func (m Model) exportTitle() string {
	if m.viewingChild() {
		if t := strings.TrimSpace(m.viewTitle); t != "" {
			return t
		}
	}
	return strings.TrimSpace(m.titleTopic)
}

func exportMarkdownCmd(path string, cells []cell, meta exportMeta, open bool) tea.Cmd {
	return func() tea.Msg {
		if err := writeExportMarkdown(path, cells, meta); err != nil {
			return exportFinishedMsg{err: err, open: open}
		}
		return exportFinishedMsg{path: path, open: open}
	}
}

func (m Model) applyExportFinished(msg exportFinishedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.setNotice("export failed: "+msg.err.Error(), true)
		return m, nil
	}
	display := displayPath(m.workDir, msg.path)
	if display == "" {
		display = msg.path
	}
	m.setNotice("exported to "+display, false)
	if !msg.open {
		return m, nil
	}
	return m, launchEditorCmd(m.workDir, msg.path, 0)
}
