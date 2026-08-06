package lsp

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Default inject knobs for tool-result diagnostics (E2.2).
const (
	// DefaultInjectMaxChars caps model-facing diagnostic text so a broken
	// project cannot blow the context window.
	DefaultInjectMaxChars = 4000
	// DefaultInjectWait is how long CollectForPaths waits for
	// publishDiagnostics after a file mutation before snapshotting.
	DefaultInjectWait = 400 * time.Millisecond
	// DefaultInjectMinSeverity is errors-only (warnings opt-in via config).
	DefaultInjectMinSeverity = SeverityError
)

// InjectOptions controls CollectForPaths formatting and wait behavior.
type InjectOptions struct {
	// MinSeverity is the maximum LSP severity value to include (1=error … 4=hint).
	// Lower numbers are more severe. Zero means DefaultInjectMinSeverity (error).
	// Diagnostics with omitted severity are treated as error.
	MinSeverity int
	// MaxChars caps the formatted block (runes). Zero means DefaultInjectMaxChars.
	MaxChars int
	// Wait is how long to wait for pending publishDiagnostics. Zero means
	// DefaultInjectWait. Negative skips the wait (snapshot immediately).
	Wait time.Duration
	// WorkDir, when set, makes paths relative in the formatted output.
	WorkDir string
}

// Normalize fills defaults for zero-valued fields.
func (o InjectOptions) Normalize() InjectOptions {
	if o.MinSeverity <= 0 {
		o.MinSeverity = DefaultInjectMinSeverity
	}
	if o.MinSeverity > SeverityHint {
		o.MinSeverity = SeverityHint
	}
	if o.MaxChars <= 0 {
		o.MaxChars = DefaultInjectMaxChars
	}
	if o.Wait == 0 {
		o.Wait = DefaultInjectWait
	}
	return o
}

// ParseSeverityName maps config strings to LSP severity constants.
// Accepts error|err, warning|warn, info|information, hint. Empty → error.
// Unknown values return an error.
func ParseSeverityName(s string) (int, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "error", "err", "errors":
		return SeverityError, nil
	case "warning", "warn", "warnings":
		return SeverityWarning, nil
	case "info", "information":
		return SeverityInformation, nil
	case "hint", "hints":
		return SeverityHint, nil
	default:
		return 0, fmt.Errorf("lsp diagnostics severity %q: want error|warning|info|hint", s)
	}
}

// SeverityName returns a short label for an LSP severity value.
func SeverityName(sev int) string {
	switch effectiveSeverity(sev) {
	case SeverityError:
		return "error"
	case SeverityWarning:
		return "warning"
	case SeverityInformation:
		return "info"
	case SeverityHint:
		return "hint"
	default:
		return "error"
	}
}

func effectiveSeverity(sev int) int {
	if sev <= 0 {
		return SeverityError
	}
	return sev
}

// CollectForPaths waits (once) for publishDiagnostics on the given absolute
// paths, then returns a single formatted diagnostics block for the model.
// Empty string when nothing to report. Multi-file callers pass all paths
// together so one tool result gets one block (debounce). Safe on nil manager.
// Never panics; failures degrade to empty.
func (m *Manager) CollectForPaths(ctx context.Context, absPaths []string, opts InjectOptions) string {
	if m == nil {
		return ""
	}
	defer func() { _ = recover() }()

	opts = opts.Normalize()
	paths := uniqueAbsPaths(absPaths)
	if len(paths) == 0 {
		return ""
	}

	m.waitPending(ctx, paths, opts.Wait)
	return m.formatPaths(paths, opts)
}

// waitPending blocks until none of paths are still awaiting a diagnostics
// publish, ctx is done, or wait elapses. wait < 0 skips. A single shared
// window covers multi-file patches (one debounce, not N).
func (m *Manager) waitPending(ctx context.Context, paths []string, wait time.Duration) {
	if wait < 0 || len(paths) == 0 {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	deadline := time.Now().Add(wait)
	ticker := time.NewTicker(15 * time.Millisecond)
	defer ticker.Stop()

	for {
		if !m.anyPending(paths) {
			return
		}
		if err := ctx.Err(); err != nil {
			return
		}
		if time.Now().After(deadline) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (m *Manager) anyPending(paths []string) bool {
	m.diagMu.Lock()
	defer m.diagMu.Unlock()
	if len(m.pending) == 0 {
		return false
	}
	for _, p := range paths {
		if _, ok := m.pending[p]; ok {
			return true
		}
	}
	return false
}

func (m *Manager) clearPending(absPath string) {
	if absPath == "" {
		return
	}
	m.diagMu.Lock()
	delete(m.pending, absPath)
	m.diagMu.Unlock()
}

// invalidatePath drops cached diagnostics for path so CollectForPaths does not
// surface stale errors while waiting for a fresh publishDiagnostics.
func (m *Manager) invalidatePath(absPath string) {
	if m == nil || absPath == "" {
		return
	}
	uri := PathToURI(absPath)
	if c := m.clientForPath(absPath); c != nil {
		c.clearDiagnosticsLocal(uri)
	}
	m.diagMu.Lock()
	delete(m.diagnostics, absPath)
	if m.pending == nil {
		m.pending = make(map[string]struct{})
	}
	m.pending[absPath] = struct{}{}
	m.diagMu.Unlock()
}

type pathDiags struct {
	path  string
	diags []Diagnostic
}

func (m *Manager) formatPaths(paths []string, opts InjectOptions) string {
	entries := make([]pathDiags, 0, len(paths))
	for _, abs := range paths {
		diags := m.Diagnostics(abs)
		if len(diags) == 0 {
			continue
		}
		entries = append(entries, pathDiags{
			path:  displayPath(opts.WorkDir, abs),
			diags: diags,
		})
	}
	return formatDiagnosticBlock(entries, opts)
}

// formatDiagnosticBlock builds one model-facing diagnostics section from
// pre-filtered path groups (severity + MaxChars applied here).
func formatDiagnosticBlock(entries []pathDiags, opts InjectOptions) string {
	type line struct {
		sort string
		text string
	}
	var lines []line
	for _, e := range entries {
		for _, d := range e.diags {
			if !includeSeverity(d.Severity, opts.MinSeverity) {
				continue
			}
			text := formatDiagLine(e.path, d)
			lines = append(lines, line{
				sort: fmt.Sprintf("%s:%07d:%07d:%s", e.path, d.Range.Start.Line, d.Range.Start.Character, text),
				text: text,
			})
		}
	}
	if len(lines) == 0 {
		return ""
	}
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].sort < lines[j].sort })

	const header = "--- diagnostics ---"
	var kept []string
	runesUsed := utf8.RuneCountInString(header)
	for _, ln := range lines {
		lineRunes := 1 + utf8.RuneCountInString(ln.text) // leading \n
		if runesUsed+lineRunes > opts.MaxChars {
			break
		}
		kept = append(kept, ln.text)
		runesUsed += lineRunes
	}
	omitted := len(lines) - len(kept)
	var b strings.Builder
	b.WriteString(header)
	for _, text := range kept {
		b.WriteByte('\n')
		b.WriteString(text)
	}
	if omitted > 0 {
		note := fmt.Sprintf("\n… (%d more diagnostic(s) truncated)", omitted)
		for utf8.RuneCountInString(b.String())+utf8.RuneCountInString(note) > opts.MaxChars && len(kept) > 0 {
			// Drop last kept line to make room for the note.
			kept = kept[:len(kept)-1]
			omitted++
			b.Reset()
			b.WriteString(header)
			for _, text := range kept {
				b.WriteByte('\n')
				b.WriteString(text)
			}
			note = fmt.Sprintf("\n… (%d more diagnostic(s) truncated)", omitted)
		}
		if utf8.RuneCountInString(b.String())+utf8.RuneCountInString(note) <= opts.MaxChars {
			b.WriteString(note)
		} else if len(kept) == 0 {
			// Nothing fits — still return a minimal cap notice.
			minimal := header + "\n… (diagnostics truncated)"
			if utf8.RuneCountInString(minimal) <= opts.MaxChars {
				return minimal
			}
			return ""
		}
	}
	return b.String()
}

func includeSeverity(sev, minSeverity int) bool {
	// Include diagnostics at or above the configured importance:
	// minSeverity=error(1) → only errors; =warning(2) → error+warning; etc.
	return effectiveSeverity(sev) <= minSeverity
}

func formatDiagLine(path string, d Diagnostic) string {
	// LSP positions are 0-based; show 1-based for editors/models.
	line := d.Range.Start.Line + 1
	col := d.Range.Start.Character + 1
	sev := SeverityName(d.Severity)
	msg := strings.TrimSpace(d.Message)
	msg = strings.ReplaceAll(msg, "\n", " ")
	if msg == "" {
		msg = "(no message)"
	}
	out := fmt.Sprintf("%s:%d:%d: %s: %s", path, line, col, sev, msg)
	if src := strings.TrimSpace(d.Source); src != "" {
		out += " [" + src + "]"
	}
	if d.Code != nil {
		s := strings.TrimSpace(fmt.Sprint(d.Code))
		if s != "" && s != "<nil>" {
			out += " (" + s + ")"
		}
	}
	return out
}

func displayPath(workDir, abs string) string {
	if workDir == "" {
		return abs
	}
	rel, err := filepath.Rel(workDir, abs)
	if err != nil || rel == "" || strings.HasPrefix(rel, "..") {
		return abs
	}
	return filepath.ToSlash(rel)
}

func uniqueAbsPaths(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !filepath.IsAbs(p) {
			if abs, err := filepath.Abs(p); err == nil {
				p = abs
			}
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
