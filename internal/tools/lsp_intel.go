package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jonathanung/strike-cli/harness/tool"
	"path/filepath"
	"strings"

	"github.com/jonathanung/strike-cli/internal/integrate/lsp"
)

// LSPIntel backs call hierarchy, rename preview, and impact tools.
// Implementations must be crash-isolated. Positions are 0-based (LSP).
// Nil intel makes tools report unavailable.
type LSPIntel interface {
	Capabilities(absPath string) (lsp.ServerCaps, error)
	IncomingCalls(ctx context.Context, absPath string, line, character int) ([]lsp.Call, error)
	OutgoingCalls(ctx context.Context, absPath string, line, character int) ([]lsp.Call, error)
	RenamePreview(ctx context.Context, absPath string, line, character int, newName string) (lsp.RenamePreview, error)
	Impact(ctx context.Context, absPath string, line, character int, newName string) (lsp.ImpactSummary, error)
}

const referencesFallback = "Use the references tool as a fallback."

// --- call_hierarchy ---

type callHierarchyTool struct {
	intel LSPIntel
}

// NewCallHierarchy returns the LSP incoming/outgoing calls tool.
func NewCallHierarchy(intel LSPIntel) tool.Tool {
	return &callHierarchyTool{intel: intel}
}

func (t *callHierarchyTool) Name() string { return "call_hierarchy" }

func (t *callHierarchyTool) Contract() tool.Contract {
	return tool.StaticContract(tool.SideEffectRead, tool.IdempotencySafeRetry)
}

func (t *callHierarchyTool) Description() string {
	return `List incoming or outgoing calls for the symbol at a file position via the language server.

Use when you need callers or callees, not just a flat reference list.
Requires a language server that advertises callHierarchyProvider (see /lsp).

Usage notes:
  - filePath is absolute or relative to the working directory.
  - line is 1-based (same as read tool line numbers).
  - character is the 0-based UTF-16 column within the line (0 = start of line).
  - direction is incoming, outgoing, or both (default both).
  - Unsupported servers return a non-fatal result and suggest the references tool.
  - Results are capped and include path:line:col locations.
  - Read-only; does not modify files.
  - When deferred tool schemas are enabled, discover via toolsearch ("call_hierarchy" or "lsp").`
}

func (t *callHierarchyTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"filePath": {"type": "string", "description": "Path to the file (absolute or relative to the working directory)"},
			"line": {"type": "integer", "description": "1-based line number (same as read tool line numbers)"},
			"character": {"type": "integer", "description": "0-based character offset within the line (default 0)"},
			"direction": {"type": "string", "description": "incoming, outgoing, or both (default both)"},
			"maxResults": {"type": "integer", "description": "Maximum calls to return per direction (default 50, max 200)"}
		},
		"required": ["filePath", "line"]
	}`)
}

type callHierarchyArgs struct {
	FilePath   string `json:"filePath"`
	Line       int    `json:"line"`
	Character  int    `json:"character"`
	Direction  string `json:"direction"`
	MaxResults int    `json:"maxResults"`
}

type callHierarchyPayload struct {
	FilePath     string         `json:"filePath"`
	Line         int            `json:"line"`
	Character    int            `json:"character"`
	Direction    string         `json:"direction"`
	Capabilities lsp.ServerCaps `json:"capabilities"`
	Incoming     []callRow      `json:"incoming"`
	Outgoing     []callRow      `json:"outgoing"`
	Truncated    bool           `json:"truncated,omitempty"`
	Note         string         `json:"note,omitempty"`
}

type callRow struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	File string `json:"file"`
	Line int    `json:"line"`
	Col  int    `json:"col"`
}

func (t *callHierarchyTool) Execute(ctx context.Context, args json.RawMessage, tc *tool.Context) (tool.Result, error) {
	var a callHierarchyArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return tool.Result{}, fmt.Errorf("invalid arguments: %w", err)
	}
	abs, rel, line0, char, err := parseNavPosArgs(mustJSONRaw(a.FilePath, a.Line, a.Character), tc)
	if err != nil {
		return tool.Result{}, err
	}
	dir := strings.ToLower(strings.TrimSpace(a.Direction))
	if dir == "" {
		dir = "both"
	}
	switch dir {
	case "incoming", "outgoing", "both":
	default:
		return tool.Result{}, fmt.Errorf("direction must be incoming, outgoing, or both")
	}
	max := a.MaxResults
	if max <= 0 {
		max = lsp.DefaultIntelMaxCalls
	}
	if max > 200 {
		max = 200
	}
	if err := tc.Ask(ctx, tool.AskRequest{
		Permission: "call_hierarchy",
		Patterns:   []string{rel},
		Always:     []string{"*"},
	}); err != nil {
		return tool.Result{}, err
	}
	if t.intel == nil {
		return navUnavailable("call_hierarchy")
	}
	caps, capErr := t.intel.Capabilities(abs)
	payload := callHierarchyPayload{
		FilePath:     rel,
		Line:         line0 + 1,
		Character:    char,
		Direction:    dir,
		Capabilities: caps,
		Incoming:     []callRow{},
		Outgoing:     []callRow{},
	}
	if capErr != nil {
		return intelSoftJSON("call_hierarchy", payload, capErr)
	}
	if !caps.CallHierarchy {
		payload.Note = "Language server does not support call hierarchy. " + referencesFallback
		return intelJSONResult("call hierarchy unsupported", payload)
	}
	workDir := ""
	if tc != nil {
		workDir = tc.WorkDir
	}
	wantIn := dir == "incoming" || dir == "both"
	wantOut := dir == "outgoing" || dir == "both"
	if wantIn {
		calls, err := t.intel.IncomingCalls(ctx, abs, line0, char)
		if err != nil {
			return intelSoftJSON("call_hierarchy", payload, err)
		}
		payload.Incoming, payload.Truncated = boundCallRows(workDir, calls, max, payload.Truncated)
	}
	if wantOut {
		calls, err := t.intel.OutgoingCalls(ctx, abs, line0, char)
		if err != nil {
			return intelSoftJSON("call_hierarchy", payload, err)
		}
		payload.Outgoing, payload.Truncated = boundCallRows(workDir, calls, max, payload.Truncated)
	}
	return intelJSONResult(callHierarchyTitle(payload), payload)
}

func callHierarchyTitle(p callHierarchyPayload) string {
	n := len(p.Incoming) + len(p.Outgoing)
	if n == 0 {
		if p.Note != "" {
			return "call hierarchy unsupported"
		}
		return "no calls"
	}
	title := fmt.Sprintf("%d calls", n)
	if n == 1 {
		title = "1 call"
	}
	if p.Truncated {
		title += " (truncated)"
	}
	return title
}

func boundCallRows(workDir string, calls []lsp.Call, max int, already bool) ([]callRow, bool) {
	truncated := already || len(calls) > max
	if len(calls) > max {
		calls = calls[:max]
	}
	out := make([]callRow, 0, len(calls))
	for _, c := range calls {
		out = append(out, callRow{
			Name: c.Name,
			Kind: lsp.SymbolKindName(c.Kind),
			File: displayRel(workDir, lsp.URIToPath(c.URI)),
			Line: c.Range.Start.Line + 1,
			Col:  c.Range.Start.Character + 1,
		})
	}
	return out, truncated
}

// --- rename_preview ---

type renamePreviewTool struct {
	intel LSPIntel
}

// NewRenamePreview returns the LSP rename preview tool (never applies).
func NewRenamePreview(intel LSPIntel) tool.Tool {
	return &renamePreviewTool{intel: intel}
}

func (t *renamePreviewTool) Name() string { return "rename_preview" }

func (t *renamePreviewTool) Contract() tool.Contract {
	return tool.StaticContract(tool.SideEffectRead, tool.IdempotencySafeRetry)
}

func (t *renamePreviewTool) Description() string {
	return `Preview a symbol rename as normalized file edits without modifying the workspace.

Use before a rename to review every planned edit. Requires a language server that
advertises renameProvider (see /lsp).

Usage notes:
  - filePath is absolute or relative to the working directory.
  - line is 1-based; character is the 0-based UTF-16 column (default 0).
  - newName is the proposed identifier.
  - Returns reviewable path:line edits. Does not write files.
  - Unsupported servers return a non-fatal result and suggest the references tool.
  - When deferred tool schemas are enabled, discover via toolsearch ("rename_preview" or "lsp").`
}

func (t *renamePreviewTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"filePath": {"type": "string", "description": "Path to the file (absolute or relative to the working directory)"},
			"line": {"type": "integer", "description": "1-based line number (same as read tool line numbers)"},
			"character": {"type": "integer", "description": "0-based character offset within the line (default 0)"},
			"newName": {"type": "string", "description": "Proposed new identifier"},
			"maxResults": {"type": "integer", "description": "Maximum edits to return (default 100, max 500)"}
		},
		"required": ["filePath", "line", "newName"]
	}`)
}

type renamePreviewArgs struct {
	FilePath   string `json:"filePath"`
	Line       int    `json:"line"`
	Character  int    `json:"character"`
	NewName    string `json:"newName"`
	MaxResults int    `json:"maxResults"`
}

type renamePreviewPayload struct {
	FilePath     string         `json:"filePath"`
	Line         int            `json:"line"`
	Character    int            `json:"character"`
	NewName      string         `json:"newName"`
	Applied      bool           `json:"applied"`
	Capabilities lsp.ServerCaps `json:"capabilities"`
	Files        int            `json:"files"`
	Edits        []renameEdit   `json:"edits"`
	Truncated    bool           `json:"truncated,omitempty"`
	Note         string         `json:"note,omitempty"`
}

type renameEdit struct {
	Kind    string `json:"kind"`
	File    string `json:"file"`
	Line    int    `json:"line,omitempty"`
	Col     int    `json:"col,omitempty"`
	NewText string `json:"newText,omitempty"`
}

func (t *renamePreviewTool) Execute(ctx context.Context, args json.RawMessage, tc *tool.Context) (tool.Result, error) {
	var a renamePreviewArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return tool.Result{}, fmt.Errorf("invalid arguments: %w", err)
	}
	if strings.TrimSpace(a.NewName) == "" {
		return tool.Result{}, fmt.Errorf("newName is required")
	}
	abs, rel, line0, char, err := parseNavPosArgs(mustJSONRaw(a.FilePath, a.Line, a.Character), tc)
	if err != nil {
		return tool.Result{}, err
	}
	max := a.MaxResults
	if max <= 0 {
		max = lsp.DefaultIntelMaxEdits
	}
	if max > 500 {
		max = 500
	}
	if err := tc.Ask(ctx, tool.AskRequest{
		Permission: "rename_preview",
		Patterns:   []string{rel},
		Always:     []string{"*"},
	}); err != nil {
		return tool.Result{}, err
	}
	if t.intel == nil {
		return navUnavailable("rename_preview")
	}
	caps, capErr := t.intel.Capabilities(abs)
	payload := renamePreviewPayload{
		FilePath:     rel,
		Line:         line0 + 1,
		Character:    char,
		NewName:      strings.TrimSpace(a.NewName),
		Applied:      false,
		Capabilities: caps,
		Edits:        []renameEdit{},
	}
	if capErr != nil {
		return intelSoftJSON("rename_preview", payload, capErr)
	}
	if !caps.Rename {
		payload.Note = "Language server does not support rename. " + referencesFallback
		return intelJSONResult("rename preview unsupported", payload)
	}
	preview, err := t.intel.RenamePreview(ctx, abs, line0, char, payload.NewName)
	if err != nil {
		return intelSoftJSON("rename_preview", payload, err)
	}
	workDir := ""
	if tc != nil {
		workDir = tc.WorkDir
	}
	payload.Files = preview.Files
	payload.Truncated = preview.Truncated
	edits := preview.Edits
	if len(edits) > max {
		edits = edits[:max]
		payload.Truncated = true
	}
	for _, e := range edits {
		payload.Edits = append(payload.Edits, renameEdit{
			Kind:    e.Kind,
			File:    displayRel(workDir, e.Path),
			Line:    e.Range.Start.Line + 1,
			Col:     e.Range.Start.Character + 1,
			NewText: e.NewText,
		})
	}
	if payload.Note == "" {
		payload.Note = "Workspace was not modified."
	}
	return intelJSONResult(renamePreviewTitle(payload), payload)
}

func renamePreviewTitle(p renamePreviewPayload) string {
	if p.Note != "" && strings.Contains(strings.ToLower(p.Note), "does not support") {
		return "rename preview unsupported"
	}
	n := len(p.Edits)
	if n == 0 {
		return "no rename edits"
	}
	title := fmt.Sprintf("%d rename edits", n)
	if n == 1 {
		title = "1 rename edit"
	}
	if p.Truncated {
		title += " (truncated)"
	}
	return title
}

// --- impact ---

type impactTool struct {
	intel LSPIntel
}

// NewImpact returns the LSP impact-summary tool.
func NewImpact(intel LSPIntel) tool.Tool {
	return &impactTool{intel: intel}
}

func (t *impactTool) Name() string { return "impact" }

func (t *impactTool) Contract() tool.Contract {
	return tool.StaticContract(tool.SideEffectRead, tool.IdempotencySafeRetry)
}

func (t *impactTool) Description() string {
	return `Summarize the impact of a symbol (and optional rename) via the language server.

Groups definitions, reads/writes or references, callers/callees, and rename
edits by file and package. Requires a configured language server (see /lsp).

Usage notes:
  - filePath is absolute or relative to the working directory.
  - line is 1-based; character is the 0-based UTF-16 column (default 0).
  - Optional newName includes a rename preview (not applied).
  - Unsupported capabilities are omitted with a note; use references as fallback.
  - Results are capped and include path:line locations.
  - Read-only; does not modify files.
  - When deferred tool schemas are enabled, discover via toolsearch ("impact" or "lsp").`
}

func (t *impactTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"filePath": {"type": "string", "description": "Path to the file (absolute or relative to the working directory)"},
			"line": {"type": "integer", "description": "1-based line number (same as read tool line numbers)"},
			"character": {"type": "integer", "description": "0-based character offset within the line (default 0)"},
			"newName": {"type": "string", "description": "Optional proposed rename to include preview edits"},
			"maxResults": {"type": "integer", "description": "Maximum impact items to return (default 100, max 500)"}
		},
		"required": ["filePath", "line"]
	}`)
}

type impactArgs struct {
	FilePath   string `json:"filePath"`
	Line       int    `json:"line"`
	Character  int    `json:"character"`
	NewName    string `json:"newName"`
	MaxResults int    `json:"maxResults"`
}

type impactPayload struct {
	FilePath     string            `json:"filePath"`
	Line         int               `json:"line"`
	Character    int               `json:"character"`
	NewName      string            `json:"newName,omitempty"`
	Capabilities lsp.ServerCaps    `json:"capabilities"`
	Counts       map[string]int    `json:"counts"`
	Groups       []impactGroupJSON `json:"groups"`
	Truncated    bool              `json:"truncated,omitempty"`
	Notes        []string          `json:"notes,omitempty"`
}

type impactGroupJSON struct {
	File    string           `json:"file"`
	Package string           `json:"package,omitempty"`
	Items   []impactItemJSON `json:"items"`
}

type impactItemJSON struct {
	Kind string `json:"kind"`
	Name string `json:"name,omitempty"`
	File string `json:"file"`
	Line int    `json:"line"`
	Col  int    `json:"col"`
}

func (t *impactTool) Execute(ctx context.Context, args json.RawMessage, tc *tool.Context) (tool.Result, error) {
	var a impactArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return tool.Result{}, fmt.Errorf("invalid arguments: %w", err)
	}
	abs, rel, line0, char, err := parseNavPosArgs(mustJSONRaw(a.FilePath, a.Line, a.Character), tc)
	if err != nil {
		return tool.Result{}, err
	}
	max := a.MaxResults
	if max <= 0 {
		max = lsp.DefaultIntelMaxImpact
	}
	if max > 500 {
		max = 500
	}
	if err := tc.Ask(ctx, tool.AskRequest{
		Permission: "impact",
		Patterns:   []string{rel},
		Always:     []string{"*"},
	}); err != nil {
		return tool.Result{}, err
	}
	if t.intel == nil {
		return navUnavailable("impact")
	}
	sum, err := t.intel.Impact(ctx, abs, line0, char, strings.TrimSpace(a.NewName))
	payload := impactPayload{
		FilePath:     rel,
		Line:         line0 + 1,
		Character:    char,
		NewName:      strings.TrimSpace(a.NewName),
		Capabilities: sum.Capabilities,
		Counts:       sum.Counts,
		Groups:       []impactGroupJSON{},
		Truncated:    sum.Truncated,
		Notes:        sum.Notes,
	}
	if err != nil {
		return intelSoftJSON("impact", payload, err)
	}
	if payload.Counts == nil {
		payload.Counts = map[string]int{}
	}
	workDir := ""
	if tc != nil {
		workDir = tc.WorkDir
	}
	remaining := max
	for _, g := range sum.Groups {
		if remaining <= 0 {
			payload.Truncated = true
			break
		}
		items := g.Items
		if len(items) > remaining {
			items = items[:remaining]
			payload.Truncated = true
		}
		jg := impactGroupJSON{
			File:    displayRel(workDir, g.File),
			Package: g.Package,
			Items:   make([]impactItemJSON, 0, len(items)),
		}
		for _, it := range items {
			jg.Items = append(jg.Items, impactItemJSON{
				Kind: it.Kind,
				Name: it.Name,
				File: displayRel(workDir, it.Path),
				Line: it.Line,
				Col:  it.Character,
			})
		}
		payload.Groups = append(payload.Groups, jg)
		remaining -= len(items)
	}
	return intelJSONResult(impactTitle(payload), payload)
}

func impactTitle(p impactPayload) string {
	n := 0
	for _, c := range p.Counts {
		n += c
	}
	if n == 0 {
		if len(p.Notes) > 0 {
			return "impact unavailable"
		}
		return "no impact"
	}
	title := fmt.Sprintf("%d impact items", n)
	if n == 1 {
		title = "1 impact item"
	}
	if p.Truncated {
		title += " (truncated)"
	}
	return title
}

func mustJSONRaw(filePath string, line, character int) json.RawMessage {
	raw, _ := json.Marshal(navPosArgs{FilePath: filePath, Line: line, Character: character})
	return raw
}

func displayRel(workDir, abs string) string {
	if workDir == "" || abs == "" {
		return abs
	}
	rel, err := filepath.Rel(workDir, abs)
	if err != nil || rel == "" || strings.HasPrefix(rel, "..") {
		return abs
	}
	return filepath.ToSlash(rel)
}

func intelSoftJSON(name string, payload any, err error) (tool.Result, error) {
	if lsp.IsUnsupported(err) {
		switch p := payload.(type) {
		case callHierarchyPayload:
			p.Note = err.Error()
			if !strings.Contains(p.Note, "references") {
				p.Note += ". " + referencesFallback
			}
			return intelJSONResult(name+" unsupported", p)
		case renamePreviewPayload:
			p.Note = err.Error()
			if !strings.Contains(p.Note, "references") {
				p.Note += ". " + referencesFallback
			}
			return intelJSONResult(name+" unsupported", p)
		case impactPayload:
			p.Notes = append(p.Notes, err.Error())
			return intelJSONResult(name+" unsupported", p)
		}
	}
	return navSoftError(name, err)
}

func intelJSONResult(title string, payload any) (tool.Result, error) {
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return tool.Result{}, fmt.Errorf("encode %s: %w", title, err)
	}
	meta, _ := json.Marshal(payload)
	return tool.Result{Title: title, Output: string(raw) + "\n", Metadata: meta}, nil
}
