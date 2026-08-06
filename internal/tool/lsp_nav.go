package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/jonathanung/strike-cli/internal/lsp"
)

// LSPNavigator backs the optional definition / references / symbols tools.
// Implementations must be crash-isolated (dead server → error, never panic).
// Positions are 0-based (LSP). Nil navigator makes tools report unavailable.
type LSPNavigator interface {
	Definition(ctx context.Context, absPath string, line, character int) ([]lsp.Location, error)
	References(ctx context.Context, absPath string, line, character int) ([]lsp.Location, error)
	DocumentSymbols(ctx context.Context, absPath string) ([]lsp.Symbol, error)
	WorkspaceSymbols(ctx context.Context, query string) ([]lsp.Symbol, error)
}

// --- definition ---

type definitionTool struct {
	nav LSPNavigator
}

// NewDefinition returns the LSP go-to-definition tool. nav may be nil
// (tool reports unavailable). Deferred when deferTools is on.
func NewDefinition(nav LSPNavigator) Tool {
	return &definitionTool{nav: nav}
}

func (t *definitionTool) Name() string { return "definition" }

func (t *definitionTool) Contract() Contract {
	return staticContract(SideEffectRead, IdempotencySafeRetry)
}

func (t *definitionTool) Description() string {
	return `Go to definition of the symbol at a file position via the language server.

Use when you need the declaration site of a function, type, method, or variable.
Requires a configured language server for the file extension (see /lsp).

Usage notes:
  - filePath is absolute or relative to the working directory.
  - line is 1-based (same as read tool line numbers).
  - character is the 0-based UTF-16 column within the line (0 = start of line).
  - Returns one or more path:line:col locations. Empty when unresolved.
  - Read-only; does not modify files.
  - When deferred tool schemas are enabled, discover via toolsearch ("definition" or "lsp").`
}

func (t *definitionTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"filePath": {"type": "string", "description": "Path to the file (absolute or relative to the working directory)"},
			"line": {"type": "integer", "description": "1-based line number (same as read tool line numbers)"},
			"character": {"type": "integer", "description": "0-based character offset within the line (default 0)"}
		},
		"required": ["filePath", "line"]
	}`)
}

type navPosArgs struct {
	FilePath  string `json:"filePath"`
	Line      int    `json:"line"`
	Character int    `json:"character"`
}

func (t *definitionTool) Execute(ctx context.Context, args json.RawMessage, tc *Context) (Result, error) {
	abs, rel, line0, char, err := parseNavPosArgs(args, tc)
	if err != nil {
		return Result{}, err
	}
	if err := tc.Ask(ctx, AskRequest{
		Permission: "definition",
		Patterns:   []string{rel},
		Always:     []string{"*"},
	}); err != nil {
		return Result{}, err
	}
	if t.nav == nil {
		return navUnavailable("definition")
	}
	locs, err := t.nav.Definition(ctx, abs, line0, char)
	if err != nil {
		return navSoftError("definition", err)
	}
	workDir := ""
	if tc != nil {
		workDir = tc.WorkDir
	}
	out := lsp.FormatLocations(workDir, locs, 0, 0)
	meta, _ := json.Marshal(map[string]any{
		"filePath":  rel,
		"line":      line0 + 1,
		"character": char,
		"count":     len(locs),
	})
	title := "no definition"
	if n := len(locs); n == 1 {
		title = "1 definition"
	} else if n > 1 {
		title = fmt.Sprintf("%d definitions", n)
	}
	return Result{Title: title, Output: out, Metadata: meta}, nil
}

// --- references ---

type referencesTool struct {
	nav LSPNavigator
}

// NewReferences returns the LSP find-references tool. nav may be nil.
// Deferred when deferTools is on.
func NewReferences(nav LSPNavigator) Tool {
	return &referencesTool{nav: nav}
}

func (t *referencesTool) Name() string { return "references" }

func (t *referencesTool) Contract() Contract {
	return staticContract(SideEffectRead, IdempotencySafeRetry)
}

func (t *referencesTool) Description() string {
	return `Find references to the symbol at a file position via the language server.

Use when you need every use site of a function, type, method, or variable.
Requires a configured language server for the file extension (see /lsp).

Usage notes:
  - filePath is absolute or relative to the working directory.
  - line is 1-based (same as read tool line numbers).
  - character is the 0-based UTF-16 column within the line (default 0).
  - Includes the declaration. Results are capped when very large.
  - Read-only; does not modify files.
  - When deferred tool schemas are enabled, discover via toolsearch ("references" or "lsp").`
}

func (t *referencesTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"filePath": {"type": "string", "description": "Path to the file (absolute or relative to the working directory)"},
			"line": {"type": "integer", "description": "1-based line number (same as read tool line numbers)"},
			"character": {"type": "integer", "description": "0-based character offset within the line (default 0)"}
		},
		"required": ["filePath", "line"]
	}`)
}

func (t *referencesTool) Execute(ctx context.Context, args json.RawMessage, tc *Context) (Result, error) {
	abs, rel, line0, char, err := parseNavPosArgs(args, tc)
	if err != nil {
		return Result{}, err
	}
	if err := tc.Ask(ctx, AskRequest{
		Permission: "references",
		Patterns:   []string{rel},
		Always:     []string{"*"},
	}); err != nil {
		return Result{}, err
	}
	if t.nav == nil {
		return navUnavailable("references")
	}
	locs, err := t.nav.References(ctx, abs, line0, char)
	if err != nil {
		return navSoftError("references", err)
	}
	workDir := ""
	if tc != nil {
		workDir = tc.WorkDir
	}
	out := lsp.FormatLocations(workDir, locs, 0, 0)
	meta, _ := json.Marshal(map[string]any{
		"filePath":  rel,
		"line":      line0 + 1,
		"character": char,
		"count":     len(locs),
	})
	title := "no references"
	if n := len(locs); n == 1 {
		title = "1 reference"
	} else if n > 1 {
		title = fmt.Sprintf("%d references", n)
	}
	return Result{Title: title, Output: out, Metadata: meta}, nil
}

// --- symbols ---

type symbolsTool struct {
	nav LSPNavigator
}

// NewSymbols returns the LSP symbols tool (document or workspace).
// nav may be nil. Deferred when deferTools is on.
func NewSymbols(nav LSPNavigator) Tool {
	return &symbolsTool{nav: nav}
}

func (t *symbolsTool) Name() string { return "symbols" }

func (t *symbolsTool) Contract() Contract {
	return staticContract(SideEffectRead, IdempotencySafeRetry)
}

func (t *symbolsTool) Description() string {
	return `List symbols via the language server (document or workspace).

Use when you need an outline of a file or to search the workspace for a named
symbol (function, type, method, constant). Requires configured language servers
(see /lsp).

Usage notes:
  - Provide filePath for document symbols (outline of one file).
  - Provide query for workspace symbol search across live language servers.
  - filePath and query may be combined: document symbols filtered by query
    substring (case-insensitive) when both are set.
  - At least one of filePath or query is required.
  - line numbers in results are 1-based.
  - Read-only; does not modify files.
  - When deferred tool schemas are enabled, discover via toolsearch ("symbols" or "lsp").`
}

func (t *symbolsTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"filePath": {"type": "string", "description": "Path to a file for document symbols (absolute or relative). Omit for workspace-wide search."},
			"query": {"type": "string", "description": "Workspace symbol query, or filter substring when filePath is also set"}
		}
	}`)
}

type symbolsArgs struct {
	FilePath string `json:"filePath"`
	Query    string `json:"query"`
}

func (t *symbolsTool) Execute(ctx context.Context, args json.RawMessage, tc *Context) (Result, error) {
	var a symbolsArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{}, fmt.Errorf("invalid arguments: %w", err)
	}
	filePath := strings.TrimSpace(a.FilePath)
	query := strings.TrimSpace(a.Query)
	if filePath == "" && query == "" {
		return Result{}, fmt.Errorf("filePath or query is required")
	}

	var (
		abs string
		rel string
	)
	if filePath != "" {
		var err error
		abs, rel, err = resolveNavPath(tc, filePath)
		if err != nil {
			return Result{}, err
		}
	} else {
		rel = "*"
	}

	if err := tc.Ask(ctx, AskRequest{
		Permission: "symbols",
		Patterns:   []string{rel},
		Always:     []string{"*"},
	}); err != nil {
		return Result{}, err
	}
	if t.nav == nil {
		return navUnavailable("symbols")
	}

	var (
		syms []lsp.Symbol
		err  error
	)
	switch {
	case abs != "" && query != "":
		syms, err = t.nav.DocumentSymbols(ctx, abs)
		if err == nil {
			syms = filterSymbols(syms, query)
		}
	case abs != "":
		syms, err = t.nav.DocumentSymbols(ctx, abs)
	default:
		syms, err = t.nav.WorkspaceSymbols(ctx, query)
	}
	if err != nil {
		return navSoftError("symbols", err)
	}

	workDir := ""
	if tc != nil {
		workDir = tc.WorkDir
	}
	out := lsp.FormatSymbols(workDir, syms, 0, 0)
	meta, _ := json.Marshal(map[string]any{
		"filePath": rel,
		"query":    query,
		"count":    len(syms),
	})
	title := "no symbols"
	if n := len(syms); n == 1 {
		title = "1 symbol"
	} else if n > 1 {
		title = fmt.Sprintf("%d symbols", n)
	}
	return Result{Title: title, Output: out, Metadata: meta}, nil
}

func filterSymbols(syms []lsp.Symbol, query string) []lsp.Symbol {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return syms
	}
	out := make([]lsp.Symbol, 0, len(syms))
	for _, s := range syms {
		hay := strings.ToLower(s.Name + " " + s.ContainerName)
		if strings.Contains(hay, q) {
			out = append(out, s)
		}
	}
	return out
}

func parseNavPosArgs(args json.RawMessage, tc *Context) (abs, rel string, line0, character int, err error) {
	var a navPosArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", "", 0, 0, fmt.Errorf("invalid arguments: %w", err)
	}
	if strings.TrimSpace(a.FilePath) == "" {
		return "", "", 0, 0, fmt.Errorf("filePath is required")
	}
	if a.Line < 1 {
		return "", "", 0, 0, fmt.Errorf("line must be >= 1 (1-based)")
	}
	if a.Character < 0 {
		return "", "", 0, 0, fmt.Errorf("character must be >= 0")
	}
	abs, rel, err = resolveNavPath(tc, a.FilePath)
	if err != nil {
		return "", "", 0, 0, err
	}
	return abs, rel, a.Line - 1, a.Character, nil
}

func resolveNavPath(tc *Context, filePath string) (abs, rel string, err error) {
	workDir := ""
	if tc != nil {
		workDir = tc.WorkDir
	}
	if workDir != "" {
		resolved, relPath, rerr := resolveInWorkspace(workDir, filePath)
		if rerr != nil {
			return "", "", rerr
		}
		return resolved, relPath, nil
	}
	// No workdir (tests): still resolve absolute/relative.
	if filepath.IsAbs(filePath) {
		abs = filepath.Clean(filePath)
	} else {
		abs = filepath.Clean(filePath)
	}
	return abs, filepath.ToSlash(filePath), nil
}

func navUnavailable(name string) (Result, error) {
	return Result{
		Title:  name + " unavailable",
		Output: "Language server navigation is not configured for this session.",
	}, nil
}

// navSoftError turns LSP failures into model-facing output (not tool errors)
// so a missing/dead server does not look like a hard tool failure.
func navSoftError(name string, err error) (Result, error) {
	msg := "language server error"
	if err != nil {
		msg = err.Error()
	}
	return Result{
		Title:  name + " failed",
		Output: msg,
	}, nil
}
