package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

// Intel result caps so a chatty language server cannot blow the context.
const (
	// DefaultIntelMaxCalls caps incoming or outgoing call rows.
	DefaultIntelMaxCalls = 50
	// DefaultIntelMaxEdits caps rename-preview edit rows.
	DefaultIntelMaxEdits = 100
	// DefaultIntelMaxImpact caps grouped impact items.
	DefaultIntelMaxImpact = 100
)

// ServerCaps reports advertised language-server capabilities for a file.
type ServerCaps struct {
	Definition        bool `json:"definition"`
	References        bool `json:"references"`
	DocumentSymbol    bool `json:"documentSymbol"`
	WorkspaceSymbol   bool `json:"workspaceSymbol"`
	DocumentHighlight bool `json:"documentHighlight"`
	CallHierarchy     bool `json:"callHierarchy"`
	Rename            bool `json:"rename"`
	PrepareRename     bool `json:"prepareRename"`
}

// Call is one incoming or outgoing call-hierarchy edge.
type Call struct {
	Name       string
	Kind       int
	Detail     string
	URI        string
	Range      Range
	FromRanges []Range
}

// Highlight is one document highlight (read/write/text).
type Highlight struct {
	Range Range
	Kind  int
}

// TextEdit is one normalized, unapplied workspace edit.
type TextEdit struct {
	Path    string
	Range   Range
	NewText string
	Kind    string // "edit", "create", "rename", "delete"
}

// RenamePreview is a reviewable workspace edit that was not applied.
type RenamePreview struct {
	NewName   string
	Edits     []TextEdit
	Files     int
	Truncated bool
}

// ImpactKind classifies one usage in an impact summary.
const (
	ImpactDefinition = "definition"
	ImpactReference  = "reference"
	ImpactRead       = "read"
	ImpactWrite      = "write"
	ImpactCaller     = "caller"
	ImpactCallee     = "callee"
	ImpactRename     = "rename"
)

// ImpactItem is one path:line usage in an impact summary.
type ImpactItem struct {
	Kind      string
	Path      string
	Line      int // 1-based
	Character int // 1-based
	Name      string
}

// ImpactGroup collects items for one file (and inferred package).
type ImpactGroup struct {
	File    string
	Package string
	Items   []ImpactItem
}

// ImpactSummary is a bounded, grouped view of a symbol's blast radius.
type ImpactSummary struct {
	Capabilities ServerCaps
	Groups       []ImpactGroup
	Counts       map[string]int
	Truncated    bool
	Notes        []string
}

// UnsupportedError is a soft capability miss. Tools should not fail the turn.
type UnsupportedError struct {
	Capability string
	Fallback   string
}

func (e *UnsupportedError) Error() string {
	if e == nil {
		return "language server does not support this operation"
	}
	msg := "language server does not support " + e.Capability
	if e.Fallback != "" {
		msg += "; use " + e.Fallback + " as a fallback"
	}
	return msg
}

// IsUnsupported reports whether err is a capability miss.
func IsUnsupported(err error) bool {
	var u *UnsupportedError
	return errors.As(err, &u)
}

func unsupported(capability, fallback string) error {
	return &UnsupportedError{Capability: capability, Fallback: fallback}
}

func providerEnabled(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return s != "" && s != "null" && s != "false"
}

func renamePrepareEnabled(raw json.RawMessage) bool {
	if !providerEnabled(raw) {
		return false
	}
	var obj struct {
		PrepareProvider bool `json:"prepareProvider"`
	}
	if json.Unmarshal(raw, &obj) != nil {
		return false
	}
	return obj.PrepareProvider
}

func capsFromServer(s serverCapabilities) ServerCaps {
	return ServerCaps{
		Definition:        providerEnabled(s.DefinitionProvider),
		References:        providerEnabled(s.ReferencesProvider),
		DocumentSymbol:    providerEnabled(s.DocumentSymbolProvider),
		WorkspaceSymbol:   providerEnabled(s.WorkspaceSymbolProvider),
		DocumentHighlight: providerEnabled(s.DocumentHighlightProvider),
		CallHierarchy:     providerEnabled(s.CallHierarchyProvider),
		Rename:            providerEnabled(s.RenameProvider),
		PrepareRename:     renamePrepareEnabled(s.RenameProvider),
	}
}

func isMethodNotFound(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "rpc error -32601")
}

// ServerCaps returns advertised capabilities from initialize.
func (c *Client) ServerCaps() ServerCaps {
	if c == nil {
		return ServerCaps{}
	}
	c.capsMu.Lock()
	defer c.capsMu.Unlock()
	return capsFromServer(c.caps)
}

// Capabilities reports advertised capabilities for the language server
// that owns absPath. Does not open the document.
func (m *Manager) Capabilities(absPath string) (ServerCaps, error) {
	if m == nil {
		return ServerCaps{}, fmt.Errorf("lsp: no manager")
	}
	if absPath == "" {
		return ServerCaps{}, fmt.Errorf("path is empty")
	}
	client := m.clientForPath(absPath)
	if client == nil {
		ext := filepath.Ext(absPath)
		if ext == "" {
			return ServerCaps{}, fmt.Errorf("no language server for %q (unknown extension)", filepath.Base(absPath))
		}
		return ServerCaps{}, fmt.Errorf("no language server for %s files", ext)
	}
	if client.Closed() {
		return ServerCaps{}, fmt.Errorf("lsp server unavailable")
	}
	return client.ServerCaps(), nil
}

// IncomingCalls resolves callHierarchy incoming calls at path (0-based).
func (m *Manager) IncomingCalls(ctx context.Context, absPath string, line, character int) ([]Call, error) {
	return m.callHierarchy(ctx, absPath, line, character, true)
}

// OutgoingCalls resolves callHierarchy outgoing calls at path (0-based).
func (m *Manager) OutgoingCalls(ctx context.Context, absPath string, line, character int) ([]Call, error) {
	return m.callHierarchy(ctx, absPath, line, character, false)
}

func (m *Manager) callHierarchy(ctx context.Context, absPath string, line, character int, incoming bool) ([]Call, error) {
	if m == nil {
		return nil, fmt.Errorf("lsp: no manager")
	}
	client, err := m.ensureOpen(ctx, absPath)
	if err != nil {
		return nil, err
	}
	caps := client.ServerCaps()
	if !caps.CallHierarchy {
		return nil, unsupported("call hierarchy", "the references tool")
	}
	var calls []Call
	if incoming {
		calls, err = client.IncomingCalls(ctx, absPath, line, character)
	} else {
		calls, err = client.OutgoingCalls(ctx, absPath, line, character)
	}
	if err != nil {
		if isMethodNotFound(err) {
			return nil, unsupported("call hierarchy", "the references tool")
		}
		if client.Closed() {
			return nil, fmt.Errorf("lsp server unavailable")
		}
		return nil, err
	}
	return calls, nil
}

// RenamePreview requests textDocument/rename and returns normalized edits
// without writing any files.
func (m *Manager) RenamePreview(ctx context.Context, absPath string, line, character int, newName string) (RenamePreview, error) {
	if m == nil {
		return RenamePreview{}, fmt.Errorf("lsp: no manager")
	}
	newName = strings.TrimSpace(newName)
	if newName == "" {
		return RenamePreview{}, fmt.Errorf("newName is required")
	}
	client, err := m.ensureOpen(ctx, absPath)
	if err != nil {
		return RenamePreview{}, err
	}
	if !client.ServerCaps().Rename {
		return RenamePreview{}, unsupported("rename", "the references tool")
	}
	preview, err := client.RenamePreview(ctx, absPath, line, character, newName)
	if err != nil {
		if isMethodNotFound(err) {
			return RenamePreview{}, unsupported("rename", "the references tool")
		}
		if client.Closed() {
			return RenamePreview{}, fmt.Errorf("lsp server unavailable")
		}
		return RenamePreview{}, err
	}
	return preview, nil
}

// DocumentHighlights resolves textDocument/documentHighlight (0-based).
func (m *Manager) DocumentHighlights(ctx context.Context, absPath string, line, character int) ([]Highlight, error) {
	if m == nil {
		return nil, fmt.Errorf("lsp: no manager")
	}
	client, err := m.ensureOpen(ctx, absPath)
	if err != nil {
		return nil, err
	}
	if !client.ServerCaps().DocumentHighlight {
		return nil, unsupported("document highlight", "the references tool")
	}
	hs, err := client.DocumentHighlights(ctx, absPath, line, character)
	if err != nil {
		if isMethodNotFound(err) {
			return nil, unsupported("document highlight", "the references tool")
		}
		if client.Closed() {
			return nil, fmt.Errorf("lsp server unavailable")
		}
		return nil, err
	}
	return hs, nil
}

// Impact builds a grouped usage summary for the symbol at path (0-based).
// newName, when set, includes a rename preview (still unapplied).
func (m *Manager) Impact(ctx context.Context, absPath string, line, character int, newName string) (ImpactSummary, error) {
	if m == nil {
		return ImpactSummary{}, fmt.Errorf("lsp: no manager")
	}
	client, err := m.ensureOpen(ctx, absPath)
	if err != nil {
		return ImpactSummary{}, err
	}
	caps := client.ServerCaps()
	sum := ImpactSummary{
		Capabilities: caps,
		Counts:       map[string]int{},
	}
	if !caps.Definition && !caps.References && !caps.DocumentHighlight && !caps.CallHierarchy && !caps.Rename {
		sum.Notes = append(sum.Notes, "language server advertised no impact capabilities; use the references tool as a fallback")
		return sum, nil
	}

	addNote := func(err error) {
		if err == nil {
			return
		}
		sum.Notes = append(sum.Notes, err.Error())
	}

	var items []ImpactItem
	if caps.Definition {
		locs, err := client.Definition(ctx, absPath, line, character)
		if err != nil && !client.Closed() {
			addNote(fmt.Errorf("definition: %w", err))
		} else if err == nil {
			items = append(items, locationsToImpact(locs, ImpactDefinition, "")...)
		}
	}
	if caps.DocumentHighlight {
		hs, err := client.DocumentHighlights(ctx, absPath, line, character)
		if err != nil && !IsUnsupported(err) && !client.Closed() {
			addNote(fmt.Errorf("highlights: %w", err))
		} else if err == nil {
			items = append(items, highlightsToImpact(absPath, hs)...)
		}
	}
	if caps.References {
		locs, err := client.References(ctx, absPath, line, character, true)
		if err != nil && !client.Closed() {
			addNote(fmt.Errorf("references: %w", err))
		} else if err == nil {
			// Prefer read/write from highlights for this file; keep other files
			// (and this file when highlights were unavailable) as references.
			haveLocalRW := caps.DocumentHighlight
			for _, loc := range locs {
				path := URIToPath(loc.URI)
				if haveLocalRW && samePath(path, absPath) {
					continue
				}
				items = append(items, locationToImpact(loc, ImpactReference, ""))
			}
		}
	} else if !caps.DocumentHighlight {
		sum.Notes = append(sum.Notes, "references are unsupported; usage list may be incomplete")
	}
	if caps.CallHierarchy {
		incoming, err := client.IncomingCalls(ctx, absPath, line, character)
		if err != nil && !IsUnsupported(err) && !client.Closed() {
			addNote(fmt.Errorf("incoming calls: %w", err))
		} else if err == nil {
			items = append(items, callsToImpact(incoming, ImpactCaller)...)
		}
		outgoing, err := client.OutgoingCalls(ctx, absPath, line, character)
		if err != nil && !IsUnsupported(err) && !client.Closed() {
			addNote(fmt.Errorf("outgoing calls: %w", err))
		} else if err == nil {
			items = append(items, callsToImpact(outgoing, ImpactCallee)...)
		}
	} else {
		sum.Notes = append(sum.Notes, "call hierarchy is unsupported; use the references tool for callers")
	}
	if name := strings.TrimSpace(newName); name != "" {
		if !caps.Rename {
			sum.Notes = append(sum.Notes, "rename is unsupported; use the references tool as a fallback")
		} else {
			preview, err := client.RenamePreview(ctx, absPath, line, character, name)
			if err != nil && !IsUnsupported(err) && !client.Closed() {
				addNote(fmt.Errorf("rename preview: %w", err))
			} else if err == nil {
				items = append(items, renameToImpact(preview)...)
			}
		}
	}

	sum.Groups, sum.Counts, sum.Truncated = groupImpact(absPath, items, DefaultIntelMaxImpact)
	return sum, nil
}

// IncomingCalls calls prepareCallHierarchy + incomingCalls (0-based).
func (c *Client) IncomingCalls(ctx context.Context, path string, line, character int) ([]Call, error) {
	return c.hierarchyCalls(ctx, path, line, character, true)
}

// OutgoingCalls calls prepareCallHierarchy + outgoingCalls (0-based).
func (c *Client) OutgoingCalls(ctx context.Context, path string, line, character int) ([]Call, error) {
	return c.hierarchyCalls(ctx, path, line, character, false)
}

func (c *Client) hierarchyCalls(ctx context.Context, path string, line, character int, incoming bool) ([]Call, error) {
	if c == nil || c.Closed() {
		return nil, clientUnavailable(c)
	}
	items, err := c.prepareCallHierarchy(ctx, path, line, character)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, nil
	}
	var out []Call
	for i, item := range items {
		if i >= DefaultIntelMaxCalls {
			break
		}
		var raw json.RawMessage
		if incoming {
			err = c.call(ctx, "callHierarchy/incomingCalls", incomingCallsParams{Item: item}, &raw)
		} else {
			err = c.call(ctx, "callHierarchy/outgoingCalls", outgoingCallsParams{Item: item}, &raw)
		}
		if err != nil {
			return nil, err
		}
		calls, err := decodeHierarchyCalls(raw, incoming)
		if err != nil {
			return nil, err
		}
		out = append(out, calls...)
	}
	return boundCalls(dedupeCalls(out), DefaultIntelMaxCalls), nil
}

func (c *Client) prepareCallHierarchy(ctx context.Context, path string, line, character int) ([]callHierarchyItem, error) {
	var raw json.RawMessage
	if err := c.call(ctx, "textDocument/prepareCallHierarchy", prepareCallHierarchyParams{
		TextDocument: textDocumentIdentifier{URI: PathToURI(path)},
		Position:     Position{Line: line, Character: character},
	}, &raw); err != nil {
		return nil, err
	}
	return decodeCallHierarchyItems(raw)
}

// RenamePreview calls textDocument/rename (and prepareRename when advertised)
// and normalizes the workspace edit. Never writes files.
func (c *Client) RenamePreview(ctx context.Context, path string, line, character int, newName string) (RenamePreview, error) {
	if c == nil || c.Closed() {
		return RenamePreview{}, clientUnavailable(c)
	}
	if c.ServerCaps().PrepareRename {
		ok, err := c.prepareRename(ctx, path, line, character)
		if err != nil && !isMethodNotFound(err) {
			return RenamePreview{}, err
		}
		if err == nil && !ok {
			return RenamePreview{NewName: newName}, nil
		}
	}
	var raw json.RawMessage
	if err := c.call(ctx, "textDocument/rename", renameParams{
		TextDocument: textDocumentIdentifier{URI: PathToURI(path)},
		Position:     Position{Line: line, Character: character},
		NewName:      newName,
	}, &raw); err != nil {
		return RenamePreview{}, err
	}
	edits, err := decodeWorkspaceEdit(raw)
	if err != nil {
		return RenamePreview{}, err
	}
	return boundRename(RenamePreview{NewName: newName, Edits: edits}, DefaultIntelMaxEdits), nil
}

func (c *Client) prepareRename(ctx context.Context, path string, line, character int) (bool, error) {
	var raw json.RawMessage
	if err := c.call(ctx, "textDocument/prepareRename", textDocumentPositionParams{
		TextDocument: textDocumentIdentifier{URI: PathToURI(path)},
		Position:     Position{Line: line, Character: character},
	}, &raw); err != nil {
		return false, err
	}
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || string(raw) == "null" {
		return false, nil
	}
	return true, nil
}

// DocumentHighlights calls textDocument/documentHighlight (0-based).
func (c *Client) DocumentHighlights(ctx context.Context, path string, line, character int) ([]Highlight, error) {
	if c == nil || c.Closed() {
		return nil, clientUnavailable(c)
	}
	var raw json.RawMessage
	if err := c.call(ctx, "textDocument/documentHighlight", documentHighlightParams{
		TextDocument: textDocumentIdentifier{URI: PathToURI(path)},
		Position:     Position{Line: line, Character: character},
	}, &raw); err != nil {
		return nil, err
	}
	return decodeHighlights(raw)
}

func decodeCallHierarchyItems(raw json.RawMessage) ([]callHierarchyItem, error) {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var items []callHierarchyItem
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("decode call hierarchy: %w", err)
	}
	out := items[:0]
	for _, it := range items {
		if strings.TrimSpace(it.URI) == "" || strings.TrimSpace(it.Name) == "" {
			continue
		}
		out = append(out, it)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func decodeHierarchyCalls(raw json.RawMessage, incoming bool) ([]Call, error) {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	if incoming {
		var rows []incomingCall
		if err := json.Unmarshal(raw, &rows); err != nil {
			return nil, fmt.Errorf("decode incoming calls: %w", err)
		}
		out := make([]Call, 0, len(rows))
		for _, row := range rows {
			if strings.TrimSpace(row.From.URI) == "" {
				continue
			}
			out = append(out, Call{
				Name:       row.From.Name,
				Kind:       row.From.Kind,
				Detail:     row.From.Detail,
				URI:        row.From.URI,
				Range:      pickCallRange(row.From, row.FromRanges),
				FromRanges: row.FromRanges,
			})
		}
		return out, nil
	}
	var rows []outgoingCall
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("decode outgoing calls: %w", err)
	}
	out := make([]Call, 0, len(rows))
	for _, row := range rows {
		if strings.TrimSpace(row.To.URI) == "" {
			continue
		}
		out = append(out, Call{
			Name:       row.To.Name,
			Kind:       row.To.Kind,
			Detail:     row.To.Detail,
			URI:        row.To.URI,
			Range:      pickCallRange(row.To, row.FromRanges),
			FromRanges: row.FromRanges,
		})
	}
	return out, nil
}

func pickCallRange(item callHierarchyItem, from []Range) Range {
	if len(from) > 0 {
		return from[0]
	}
	if item.SelectionRange != (Range{}) {
		return item.SelectionRange
	}
	return item.Range
}

func decodeHighlights(raw json.RawMessage) ([]Highlight, error) {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var rows []documentHighlight
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("decode document highlight: %w", err)
	}
	out := make([]Highlight, 0, len(rows))
	for _, row := range rows {
		kind := row.Kind
		if kind == 0 {
			kind = HighlightText
		}
		out = append(out, Highlight{Range: row.Range, Kind: kind})
	}
	return out, nil
}

func decodeWorkspaceEdit(raw json.RawMessage) ([]TextEdit, error) {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var edit workspaceEdit
	if err := json.Unmarshal(raw, &edit); err != nil {
		return nil, fmt.Errorf("decode workspace edit: %w", err)
	}
	var out []TextEdit
	if len(edit.Changes) > 0 {
		uris := make([]string, 0, len(edit.Changes))
		for uri := range edit.Changes {
			uris = append(uris, uri)
		}
		sort.Strings(uris)
		for _, uri := range uris {
			path := URIToPath(uri)
			for _, te := range edit.Changes[uri] {
				out = append(out, TextEdit{Path: path, Range: te.Range, NewText: te.NewText, Kind: "edit"})
			}
		}
	}
	for _, rawChange := range edit.DocumentChanges {
		rawChange = json.RawMessage(strings.TrimSpace(string(rawChange)))
		if len(rawChange) == 0 {
			continue
		}
		var probe struct {
			Kind string `json:"kind"`
		}
		_ = json.Unmarshal(rawChange, &probe)
		switch probe.Kind {
		case "create":
			var op struct {
				URI string `json:"uri"`
			}
			if json.Unmarshal(rawChange, &op) == nil && op.URI != "" {
				out = append(out, TextEdit{Path: URIToPath(op.URI), Kind: "create"})
			}
		case "rename":
			var op struct {
				OldURI string `json:"oldUri"`
				NewURI string `json:"newUri"`
			}
			if json.Unmarshal(rawChange, &op) == nil {
				out = append(out, TextEdit{
					Path:    URIToPath(op.OldURI),
					NewText: URIToPath(op.NewURI),
					Kind:    "rename",
				})
			}
		case "delete":
			var op struct {
				URI string `json:"uri"`
			}
			if json.Unmarshal(rawChange, &op) == nil && op.URI != "" {
				out = append(out, TextEdit{Path: URIToPath(op.URI), Kind: "delete"})
			}
		default:
			var doc textDocumentEdit
			if err := json.Unmarshal(rawChange, &doc); err != nil || doc.TextDocument.URI == "" {
				return nil, fmt.Errorf("decode workspace edit: unexpected documentChange")
			}
			path := URIToPath(doc.TextDocument.URI)
			for _, te := range doc.Edits {
				out = append(out, TextEdit{Path: path, Range: te.Range, NewText: te.NewText, Kind: "edit"})
			}
		}
	}
	return out, nil
}

func dedupeCalls(in []Call) []Call {
	if len(in) <= 1 {
		return in
	}
	type key struct {
		name, uri string
		line, col int
	}
	seen := make(map[key]struct{}, len(in))
	out := make([]Call, 0, len(in))
	for _, c := range in {
		k := key{c.Name, c.URI, c.Range.Start.Line, c.Range.Start.Character}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, c)
	}
	return out
}

func boundCalls(in []Call, max int) []Call {
	if max <= 0 || len(in) <= max {
		return in
	}
	return in[:max]
}

func boundRename(p RenamePreview, max int) RenamePreview {
	files := map[string]struct{}{}
	for _, e := range p.Edits {
		if e.Path != "" {
			files[e.Path] = struct{}{}
		}
	}
	p.Files = len(files)
	if max > 0 && len(p.Edits) > max {
		p.Edits = p.Edits[:max]
		p.Truncated = true
	}
	return p
}

func locationToImpact(loc Location, kind, name string) ImpactItem {
	return ImpactItem{
		Kind:      kind,
		Path:      URIToPath(loc.URI),
		Line:      loc.Range.Start.Line + 1,
		Character: loc.Range.Start.Character + 1,
		Name:      name,
	}
}

func locationsToImpact(locs []Location, kind, name string) []ImpactItem {
	out := make([]ImpactItem, 0, len(locs))
	for _, loc := range locs {
		if strings.TrimSpace(loc.URI) == "" {
			continue
		}
		out = append(out, locationToImpact(loc, kind, name))
	}
	return out
}

func highlightsToImpact(path string, hs []Highlight) []ImpactItem {
	out := make([]ImpactItem, 0, len(hs))
	for _, h := range hs {
		kind := ImpactReference
		switch h.Kind {
		case HighlightRead:
			kind = ImpactRead
		case HighlightWrite:
			kind = ImpactWrite
		}
		out = append(out, ImpactItem{
			Kind:      kind,
			Path:      path,
			Line:      h.Range.Start.Line + 1,
			Character: h.Range.Start.Character + 1,
		})
	}
	return out
}

func callsToImpact(calls []Call, kind string) []ImpactItem {
	out := make([]ImpactItem, 0, len(calls))
	for _, c := range calls {
		out = append(out, ImpactItem{
			Kind:      kind,
			Path:      URIToPath(c.URI),
			Line:      c.Range.Start.Line + 1,
			Character: c.Range.Start.Character + 1,
			Name:      c.Name,
		})
	}
	return out
}

func renameToImpact(p RenamePreview) []ImpactItem {
	out := make([]ImpactItem, 0, len(p.Edits))
	for _, e := range p.Edits {
		out = append(out, ImpactItem{
			Kind:      ImpactRename,
			Path:      e.Path,
			Line:      e.Range.Start.Line + 1,
			Character: e.Range.Start.Character + 1,
			Name:      p.NewName,
		})
	}
	return out
}

func groupImpact(origin string, items []ImpactItem, max int) ([]ImpactGroup, map[string]int, bool) {
	counts := map[string]int{}
	type key struct {
		kind, path string
		line, col  int
	}
	seen := make(map[key]struct{}, len(items))
	sorted := make([]ImpactItem, 0, len(items))
	for _, it := range items {
		if it.Path == "" {
			continue
		}
		k := key{it.Kind, it.Path, it.Line, it.Character}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		sorted = append(sorted, it)
	}
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Path != sorted[j].Path {
			return sorted[i].Path < sorted[j].Path
		}
		if sorted[i].Line != sorted[j].Line {
			return sorted[i].Line < sorted[j].Line
		}
		if sorted[i].Character != sorted[j].Character {
			return sorted[i].Character < sorted[j].Character
		}
		return sorted[i].Kind < sorted[j].Kind
	})
	truncated := false
	if max > 0 && len(sorted) > max {
		sorted = sorted[:max]
		truncated = true
	}
	byFile := map[string][]ImpactItem{}
	order := []string{}
	for _, it := range sorted {
		counts[it.Kind]++
		if _, ok := byFile[it.Path]; !ok {
			order = append(order, it.Path)
		}
		byFile[it.Path] = append(byFile[it.Path], it)
	}
	groups := make([]ImpactGroup, 0, len(order))
	for _, path := range order {
		groups = append(groups, ImpactGroup{
			File:    path,
			Package: inferPackage(origin, path),
			Items:   byFile[path],
		})
	}
	return groups, counts, truncated
}

func inferPackage(origin, path string) string {
	dir := filepath.ToSlash(filepath.Dir(path))
	if dir == "" || dir == "." {
		if origin != "" {
			return filepath.Base(filepath.Dir(origin))
		}
		return "."
	}
	return filepath.Base(dir)
}

func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// FormatCalls builds model-facing text for call-hierarchy results.
func FormatCalls(workDir string, calls []Call, maxCalls, maxChars int) string {
	if maxCalls <= 0 {
		maxCalls = DefaultIntelMaxCalls
	}
	if maxChars <= 0 {
		maxChars = DefaultNavMaxChars
	}
	if len(calls) == 0 {
		return "No calls."
	}
	sorted := append([]Call(nil), calls...)
	sort.SliceStable(sorted, func(i, j int) bool {
		pi, pj := URIToPath(sorted[i].URI), URIToPath(sorted[j].URI)
		if pi != pj {
			return pi < pj
		}
		if sorted[i].Range.Start.Line != sorted[j].Range.Start.Line {
			return sorted[i].Range.Start.Line < sorted[j].Range.Start.Line
		}
		return sorted[i].Name < sorted[j].Name
	})
	total := len(sorted)
	if len(sorted) > maxCalls {
		sorted = sorted[:maxCalls]
	}
	var b strings.Builder
	runesUsed := 0
	written := 0
	for _, c := range sorted {
		path := displayPath(workDir, URIToPath(c.URI))
		line := c.Range.Start.Line + 1
		col := c.Range.Start.Character + 1
		kind := SymbolKindName(c.Kind)
		name := strings.TrimSpace(c.Name)
		if name == "" {
			name = "?"
		}
		text := fmt.Sprintf("%s %s  %s:%d:%d", kind, name, path, line, col)
		if d := strings.TrimSpace(c.Detail); d != "" {
			text += "  (" + d + ")"
		}
		lineRunes := utf8.RuneCountInString(text)
		if written > 0 {
			lineRunes++
		}
		if runesUsed+lineRunes > maxChars {
			break
		}
		if written > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(text)
		runesUsed += lineRunes
		written++
	}
	omitted := total - written
	if omitted > 0 {
		note := fmt.Sprintf("\n… (%d more truncated)", omitted)
		if runesUsed+utf8.RuneCountInString(note) <= maxChars+64 {
			b.WriteString(note)
		}
	}
	if b.Len() == 0 {
		return "No calls."
	}
	return b.String()
}

// FormatRenamePreview builds model-facing text for an unapplied workspace edit.
func FormatRenamePreview(workDir string, preview RenamePreview, maxEdits, maxChars int) string {
	if maxEdits <= 0 {
		maxEdits = DefaultIntelMaxEdits
	}
	if maxChars <= 0 {
		maxChars = DefaultNavMaxChars
	}
	if len(preview.Edits) == 0 {
		return "No rename edits. Workspace was not modified."
	}
	edits := append([]TextEdit(nil), preview.Edits...)
	sort.SliceStable(edits, func(i, j int) bool {
		if edits[i].Path != edits[j].Path {
			return edits[i].Path < edits[j].Path
		}
		if edits[i].Range.Start.Line != edits[j].Range.Start.Line {
			return edits[i].Range.Start.Line < edits[j].Range.Start.Line
		}
		return edits[i].Kind < edits[j].Kind
	})
	total := len(edits)
	if len(edits) > maxEdits {
		edits = edits[:maxEdits]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Rename preview %q — not applied (%d file", preview.NewName, preview.Files)
	if preview.Files != 1 {
		b.WriteByte('s')
	}
	b.WriteString("):\n")
	runesUsed := utf8.RuneCountInString(b.String())
	written := 0
	for _, e := range edits {
		path := displayPath(workDir, e.Path)
		var text string
		switch e.Kind {
		case "create":
			text = "create " + path
		case "delete":
			text = "delete " + path
		case "rename":
			text = "rename " + path + " → " + displayPath(workDir, e.NewText)
		default:
			text = fmt.Sprintf("%s:%d:%d  %q", path, e.Range.Start.Line+1, e.Range.Start.Character+1, e.NewText)
		}
		lineRunes := utf8.RuneCountInString(text) + 1
		if runesUsed+lineRunes > maxChars {
			break
		}
		b.WriteString(text)
		b.WriteByte('\n')
		runesUsed += lineRunes
		written++
	}
	omitted := total - written
	if preview.Truncated || omitted > 0 {
		if omitted < 1 {
			omitted = total - written
		}
		fmt.Fprintf(&b, "… (%d more truncated)\n", omitted)
	}
	b.WriteString("Workspace was not modified.")
	return b.String()
}

// FormatImpact builds model-facing text for a grouped impact summary.
func FormatImpact(workDir string, sum ImpactSummary, maxItems, maxChars int) string {
	if maxItems <= 0 {
		maxItems = DefaultIntelMaxImpact
	}
	if maxChars <= 0 {
		maxChars = DefaultNavMaxChars
	}
	var b strings.Builder
	total := 0
	for _, n := range sum.Counts {
		total += n
	}
	if total == 0 && len(sum.Notes) == 0 {
		return "No impact results."
	}
	if total > 0 {
		fmt.Fprintf(&b, "%d usages", total)
		if sum.Truncated {
			b.WriteString(" (truncated)")
		}
		b.WriteByte('\n')
	}
	written := 0
	for _, g := range sum.Groups {
		file := displayPath(workDir, g.File)
		header := file
		if g.Package != "" && g.Package != "." {
			header += "  (" + g.Package + ")"
		}
		header += "\n"
		if utf8.RuneCountInString(b.String())+utf8.RuneCountInString(header) > maxChars {
			break
		}
		b.WriteString(header)
		for _, it := range g.Items {
			if written >= maxItems {
				break
			}
			name := strings.TrimSpace(it.Name)
			var text string
			if name != "" {
				text = fmt.Sprintf("  %s %s  %s:%d:%d\n", it.Kind, name, file, it.Line, it.Character)
			} else {
				text = fmt.Sprintf("  %s  %s:%d:%d\n", it.Kind, file, it.Line, it.Character)
			}
			if utf8.RuneCountInString(b.String())+utf8.RuneCountInString(text) > maxChars {
				break
			}
			b.WriteString(text)
			written++
		}
	}
	for _, note := range sum.Notes {
		line := "note: " + note + "\n"
		if utf8.RuneCountInString(b.String())+utf8.RuneCountInString(line) > maxChars+128 {
			break
		}
		b.WriteString(line)
	}
	if b.Len() == 0 {
		return "No impact results."
	}
	return strings.TrimRight(b.String(), "\n")
}
