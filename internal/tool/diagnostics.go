package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jonathanung/strike-cli/internal/lsp"
)

// DefaultDiagnosticsMaxResults caps model-facing diagnostics tool output.
const DefaultDiagnosticsMaxResults = 100

// MaxDiagnosticsMaxResults is the hard upper bound for maxResults.
const MaxDiagnosticsMaxResults = 500

// DiagnosticsSource backs the read-only diagnostics tool.
// Implementations must be crash-isolated (dead server → empty/status, never panic).
// Nil source makes the tool report unavailable without failing the turn.
type DiagnosticsSource interface {
	AllDiagnostics() map[string][]lsp.Diagnostic
	Statuses() []lsp.Status
}

// Ensure *lsp.Manager satisfies DiagnosticsSource.
var _ DiagnosticsSource = (*lsp.Manager)(nil)

type diagnosticsTool struct {
	src DiagnosticsSource
}

// NewDiagnostics returns the LSP workspace diagnostics query tool.
// src may be nil (tool reports unavailable). Deferred when deferTools is on.
func NewDiagnostics(src DiagnosticsSource) Tool {
	return &diagnosticsTool{src: src}
}

func (t *diagnosticsTool) Name() string { return "diagnostics" }

func (t *diagnosticsTool) Contract() Contract {
	return staticContract(SideEffectRead, IdempotencySafeRetry)
}

func (t *diagnosticsTool) Description() string {
	return `Query current language-server diagnostics for the workspace, a directory, or a file.

Use when you need compile/typecheck findings without editing a file or running a full compiler via bash.
Backed by live language servers (see /lsp). Read-only.

Usage notes:
  - Omit path for the whole workspace (all cached diagnostics under the working directory).
  - path may be a file (exact match) or directory (prefix scope). Absolute or relative to the working directory.
  - severity filters by minimum importance: error (default), warning, info, or hint.
    error = errors only; warning = error+warning; info adds info; hint includes all.
  - maxResults bounds the returned list (default 100, max 500). Excess is truncated with a stable flag.
  - Results are deterministic: sorted by file, then range start, severity, message.
  - Each item includes file, range (1-based line/character), severity, source, code, and message.
  - Missing or crashed language servers return structured status (empty diagnostics) — never hang or fail the turn.
  - When deferred tool schemas are enabled, discover via toolsearch ("diagnostics" or "lsp").`
}

func (t *diagnosticsTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {"type": "string", "description": "File or directory to scope (absolute or relative). Omit for the whole workspace."},
			"severity": {"type": "string", "description": "Minimum severity to include: error (default), warning, info, or hint"},
			"maxResults": {"type": "integer", "description": "Maximum diagnostics to return (default 100, max 500)"}
		}
	}`)
}

type diagnosticsArgs struct {
	Path       string `json:"path"`
	Severity   string `json:"severity"`
	MaxResults int    `json:"maxResults"`
}

// diagnosticsItem is one stable model-facing finding (1-based positions).
type diagnosticsItem struct {
	File     string           `json:"file"`
	Range    diagnosticsRange `json:"range"`
	Severity string           `json:"severity"`
	Source   string           `json:"source,omitempty"`
	Code     string           `json:"code,omitempty"`
	Message  string           `json:"message"`
}

type diagnosticsRange struct {
	Start diagnosticsPos `json:"start"`
	End   diagnosticsPos `json:"end"`
}

type diagnosticsPos struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type diagnosticsServerStatus struct {
	Name  string `json:"name"`
	State string `json:"state"`
	Error string `json:"error,omitempty"`
}

// diagnosticsPayload is the stable JSON schema returned in Output + Metadata.
type diagnosticsPayload struct {
	OK          bool                      `json:"ok"`
	Scope       string                    `json:"scope"` // workspace | file | directory
	Path        string                    `json:"path,omitempty"`
	Severity    string                    `json:"severity"`
	Servers     []diagnosticsServerStatus `json:"servers,omitempty"`
	Diagnostics []diagnosticsItem         `json:"diagnostics"`
	Count       int                       `json:"count"`
	Total       int                       `json:"total"`
	Truncated   bool                      `json:"truncated"`
	Note        string                    `json:"note,omitempty"`
}

func (t *diagnosticsTool) Execute(ctx context.Context, args json.RawMessage, tc *Context) (res Result, err error) {
	// Crash isolation: panics from a bad source become structured soft output.
	defer func() {
		if rec := recover(); rec != nil {
			res, err = diagnosticsResult(diagnosticsPayload{
				OK:          true,
				Scope:       "workspace",
				Severity:    "error",
				Diagnostics: []diagnosticsItem{},
				Note:        "language server diagnostics failed",
			})
		}
	}()

	var a diagnosticsArgs
	if len(args) > 0 && string(args) != "null" {
		if err := json.Unmarshal(args, &a); err != nil {
			return Result{}, fmt.Errorf("invalid arguments: %w", err)
		}
	}

	minSev, err := lsp.ParseSeverityName(a.Severity)
	if err != nil {
		return Result{}, err
	}
	sevName := lsp.SeverityName(minSev)

	maxResults := a.MaxResults
	if maxResults <= 0 {
		maxResults = DefaultDiagnosticsMaxResults
	}
	if maxResults > MaxDiagnosticsMaxResults {
		maxResults = MaxDiagnosticsMaxResults
	}

	pathArg := strings.TrimSpace(a.Path)
	scope := "workspace"
	var (
		absPath  string
		relPath  string
		dirScope bool
	)

	if pathArg != "" {
		var rerr error
		absPath, relPath, rerr = resolveNavPath(tc, pathArg)
		if rerr != nil {
			return Result{}, rerr
		}
		if fi, sterr := os.Stat(absPath); sterr == nil && fi.IsDir() {
			scope = "directory"
			dirScope = true
		} else {
			scope = "file"
		}
	}

	permPattern := "*"
	if pathArg != "" {
		permPattern = relPath
	}
	if err := tc.Ask(ctx, AskRequest{
		Permission: "diagnostics",
		Patterns:   []string{permPattern},
		Always:     []string{"*"},
	}); err != nil {
		return Result{}, err
	}

	payload := diagnosticsPayload{
		OK:          true,
		Scope:       scope,
		Severity:    sevName,
		Diagnostics: []diagnosticsItem{},
	}
	if pathArg != "" {
		payload.Path = relPath
	}

	if t.src == nil {
		payload.Note = "Language server diagnostics are not configured for this session."
		return diagnosticsResult(payload)
	}

	statuses := t.src.Statuses()
	payload.Servers = make([]diagnosticsServerStatus, 0, len(statuses))
	live := 0
	for _, st := range statuses {
		payload.Servers = append(payload.Servers, diagnosticsServerStatus{
			Name:  st.Name,
			State: st.State,
			Error: st.Error,
		})
		if st.State == "up" {
			live++
		}
	}
	if len(statuses) == 0 {
		payload.Note = "no language servers configured (add lsp.servers in config)"
	} else if live == 0 {
		payload.Note = "no live language servers (see servers status; try /lsp retry)"
	}

	workDir := ""
	if tc != nil {
		workDir = tc.WorkDir
	}

	byPath := t.src.AllDiagnostics()
	items := collectDiagnosticsItems(byPath, workDir, absPath, dirScope, pathArg == "", minSev)
	payload.Total = len(items)
	if len(items) > maxResults {
		payload.Diagnostics = items[:maxResults]
		payload.Truncated = true
	} else {
		payload.Diagnostics = items
	}
	payload.Count = len(payload.Diagnostics)

	return diagnosticsResult(payload)
}

func collectDiagnosticsItems(
	byPath map[string][]lsp.Diagnostic,
	workDir, absPath string,
	dirScope, workspaceScope bool,
	minSev int,
) []diagnosticsItem {
	if len(byPath) == 0 {
		return nil
	}

	paths := make([]string, 0, len(byPath))
	for p := range byPath {
		if !pathInDiagnosticsScope(p, workDir, absPath, dirScope, workspaceScope) {
			continue
		}
		paths = append(paths, p)
	}
	sort.Strings(paths)

	var out []diagnosticsItem
	for _, abs := range paths {
		diags := byPath[abs]
		// Stable order within a file.
		sort.SliceStable(diags, func(i, j int) bool {
			di, dj := diags[i], diags[j]
			if di.Range.Start.Line != dj.Range.Start.Line {
				return di.Range.Start.Line < dj.Range.Start.Line
			}
			if di.Range.Start.Character != dj.Range.Start.Character {
				return di.Range.Start.Character < dj.Range.Start.Character
			}
			si, sj := effectiveDiagSeverity(di.Severity), effectiveDiagSeverity(dj.Severity)
			if si != sj {
				return si < sj
			}
			if di.Message != dj.Message {
				return di.Message < dj.Message
			}
			return formatDiagCode(di.Code) < formatDiagCode(dj.Code)
		})
		display := displayDiagPath(workDir, abs)
		for _, d := range diags {
			if !includeDiagSeverity(d.Severity, minSev) {
				continue
			}
			msg := strings.TrimSpace(d.Message)
			if msg == "" {
				msg = "(no message)"
			}
			msg = strings.ReplaceAll(msg, "\n", " ")
			out = append(out, diagnosticsItem{
				File: display,
				Range: diagnosticsRange{
					Start: diagnosticsPos{
						Line:      d.Range.Start.Line + 1,
						Character: d.Range.Start.Character + 1,
					},
					End: diagnosticsPos{
						Line:      d.Range.End.Line + 1,
						Character: d.Range.End.Character + 1,
					},
				},
				Severity: lsp.SeverityName(d.Severity),
				Source:   strings.TrimSpace(d.Source),
				Code:     formatDiagCode(d.Code),
				Message:  msg,
			})
		}
	}
	return out
}

func pathInDiagnosticsScope(abs, workDir, scopeAbs string, dirScope, workspaceScope bool) bool {
	if workspaceScope {
		if workDir == "" {
			return true
		}
		// Keep workspace results under the session root when possible.
		rel, err := filepath.Rel(workDir, abs)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return false
		}
		return true
	}
	if scopeAbs == "" {
		return false
	}
	if dirScope {
		// Directory prefix: path itself or anything under it.
		if abs == scopeAbs {
			return true
		}
		prefix := scopeAbs
		if !strings.HasSuffix(prefix, string(filepath.Separator)) {
			prefix += string(filepath.Separator)
		}
		return strings.HasPrefix(abs, prefix)
	}
	return abs == scopeAbs
}

func displayDiagPath(workDir, abs string) string {
	if workDir == "" {
		return filepath.ToSlash(abs)
	}
	rel, err := filepath.Rel(workDir, abs)
	if err != nil || rel == "" || strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(abs)
	}
	return filepath.ToSlash(rel)
}

func includeDiagSeverity(sev, minSeverity int) bool {
	// Include diagnostics at or above the configured importance:
	// minSeverity=error(1) → only errors; =warning(2) → error+warning; etc.
	return effectiveDiagSeverity(sev) <= minSeverity
}

func effectiveDiagSeverity(sev int) int {
	if sev <= 0 {
		return lsp.SeverityError
	}
	return sev
}

func formatDiagCode(code any) string {
	if code == nil {
		return ""
	}
	s := strings.TrimSpace(fmt.Sprint(code))
	if s == "" || s == "<nil>" {
		return ""
	}
	return s
}

func diagnosticsResult(payload diagnosticsPayload) (Result, error) {
	if payload.Diagnostics == nil {
		payload.Diagnostics = []diagnosticsItem{}
	}
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return Result{}, fmt.Errorf("encode diagnostics: %w", err)
	}
	meta, _ := json.Marshal(payload)

	title := diagnosticsTitle(payload)
	return Result{
		Title:    title,
		Output:   string(raw) + "\n",
		Metadata: meta,
	}, nil
}

func diagnosticsTitle(payload diagnosticsPayload) string {
	if payload.Count > 0 {
		title := "1 diagnostic"
		if payload.Count != 1 {
			title = fmt.Sprintf("%d diagnostics", payload.Count)
		}
		if payload.Truncated {
			title += " (truncated)"
		}
		return title
	}
	note := strings.ToLower(payload.Note)
	switch {
	case strings.Contains(note, "not configured"),
		strings.Contains(note, "no language servers"),
		strings.Contains(note, "no live"),
		strings.Contains(note, "failed"):
		return "diagnostics unavailable"
	default:
		return "no diagnostics"
	}
}
