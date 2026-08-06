package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

// Navigation result caps so a chatty language server cannot blow the context.
const (
	// DefaultNavMaxLocations caps definition/references result rows.
	DefaultNavMaxLocations = 100
	// DefaultNavMaxSymbols caps document/workspace symbol rows.
	DefaultNavMaxSymbols = 200
	// DefaultNavMaxChars caps formatted model-facing navigation output (runes).
	DefaultNavMaxChars = 8000
)

// Definition resolves textDocument/definition at path (0-based line/character).
// Ensures the document is open. Empty slice when none; soft error when no
// server or the server is dead (crash isolation — never panics).
func (m *Manager) Definition(ctx context.Context, absPath string, line, character int) ([]Location, error) {
	if m == nil {
		return nil, fmt.Errorf("lsp: no manager")
	}
	client, err := m.ensureOpen(ctx, absPath)
	if err != nil {
		return nil, err
	}
	locs, err := client.Definition(ctx, absPath, line, character)
	if err != nil {
		if client.Closed() {
			return nil, fmt.Errorf("lsp server unavailable")
		}
		return nil, err
	}
	return locs, nil
}

// References resolves textDocument/references at path (0-based line/character).
// Includes the declaration. Same crash isolation as Definition.
func (m *Manager) References(ctx context.Context, absPath string, line, character int) ([]Location, error) {
	if m == nil {
		return nil, fmt.Errorf("lsp: no manager")
	}
	client, err := m.ensureOpen(ctx, absPath)
	if err != nil {
		return nil, err
	}
	locs, err := client.References(ctx, absPath, line, character, true)
	if err != nil {
		if client.Closed() {
			return nil, fmt.Errorf("lsp server unavailable")
		}
		return nil, err
	}
	return locs, nil
}

// DocumentSymbols resolves textDocument/documentSymbol for path.
func (m *Manager) DocumentSymbols(ctx context.Context, absPath string) ([]Symbol, error) {
	if m == nil {
		return nil, fmt.Errorf("lsp: no manager")
	}
	client, err := m.ensureOpen(ctx, absPath)
	if err != nil {
		return nil, err
	}
	syms, err := client.DocumentSymbols(ctx, absPath)
	if err != nil {
		if client.Closed() {
			return nil, fmt.Errorf("lsp server unavailable")
		}
		return nil, err
	}
	return syms, nil
}

// WorkspaceSymbols resolves workspace/symbol for query across live servers.
// Results from all up servers are merged. Empty query is allowed (server-defined).
func (m *Manager) WorkspaceSymbols(ctx context.Context, query string) ([]Symbol, error) {
	if m == nil {
		return nil, fmt.Errorf("lsp: no manager")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	clients := m.liveClients()
	if len(clients) == 0 {
		return nil, fmt.Errorf("no language servers available")
	}
	var out []Symbol
	var lastErr error
	okAny := false
	for _, c := range clients {
		syms, err := c.WorkspaceSymbols(ctx, query)
		if err != nil {
			if c.Closed() {
				lastErr = fmt.Errorf("lsp server unavailable")
				continue
			}
			lastErr = err
			continue
		}
		okAny = true
		out = append(out, syms...)
	}
	if !okAny {
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, fmt.Errorf("no language servers available")
	}
	return dedupeSymbols(out), nil
}

// ensureOpen returns a live client for path, opening the document from disk
// when it is not already synced.
func (m *Manager) ensureOpen(ctx context.Context, absPath string) (*Client, error) {
	if absPath == "" {
		return nil, fmt.Errorf("path is empty")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	client := m.clientForPath(absPath)
	if client == nil {
		ext := filepath.Ext(absPath)
		if ext == "" {
			return nil, fmt.Errorf("no language server for %q (unknown extension)", filepath.Base(absPath))
		}
		return nil, fmt.Errorf("no language server for %s files", ext)
	}
	uri := PathToURI(absPath)
	client.docMu.Lock()
	_, open := client.openDocs[uri]
	client.docMu.Unlock()
	if open {
		return client, nil
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", absPath, err)
	}
	if err := client.DidOpenOrChange(ctx, absPath, string(data)); err != nil {
		if client.Closed() {
			return nil, fmt.Errorf("lsp server unavailable")
		}
		return nil, err
	}
	return client, nil
}

func (m *Manager) liveClients() []*Client {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Client, 0, len(m.clients))
	names := make([]string, 0, len(m.clients))
	for name := range m.clients {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if m.disabled[name] {
			continue
		}
		c := m.clients[name]
		if c == nil || c.Closed() {
			continue
		}
		out = append(out, c)
	}
	return out
}

// Definition calls textDocument/definition (0-based position).
func (c *Client) Definition(ctx context.Context, path string, line, character int) ([]Location, error) {
	if c == nil || c.Closed() {
		return nil, c.deadErr()
	}
	var raw json.RawMessage
	if err := c.call(ctx, "textDocument/definition", textDocumentPositionParams{
		TextDocument: textDocumentIdentifier{URI: PathToURI(path)},
		Position:     Position{Line: line, Character: character},
	}, &raw); err != nil {
		return nil, err
	}
	return decodeLocations(raw)
}

// References calls textDocument/references (0-based position).
func (c *Client) References(ctx context.Context, path string, line, character int, includeDeclaration bool) ([]Location, error) {
	if c == nil || c.Closed() {
		return nil, c.deadErr()
	}
	var raw json.RawMessage
	if err := c.call(ctx, "textDocument/references", referenceParams{
		TextDocument: textDocumentIdentifier{URI: PathToURI(path)},
		Position:     Position{Line: line, Character: character},
		Context:      referenceContext{IncludeDeclaration: includeDeclaration},
	}, &raw); err != nil {
		return nil, err
	}
	return decodeLocations(raw)
}

// DocumentSymbols calls textDocument/documentSymbol.
func (c *Client) DocumentSymbols(ctx context.Context, path string) ([]Symbol, error) {
	if c == nil || c.Closed() {
		return nil, c.deadErr()
	}
	var raw json.RawMessage
	if err := c.call(ctx, "textDocument/documentSymbol", documentSymbolParams{
		TextDocument: textDocumentIdentifier{URI: PathToURI(path)},
	}, &raw); err != nil {
		return nil, err
	}
	return decodeDocumentSymbols(raw, path)
}

// WorkspaceSymbols calls workspace/symbol.
func (c *Client) WorkspaceSymbols(ctx context.Context, query string) ([]Symbol, error) {
	if c == nil || c.Closed() {
		return nil, c.deadErr()
	}
	var raw json.RawMessage
	if err := c.call(ctx, "workspace/symbol", workspaceSymbolParams{Query: query}, &raw); err != nil {
		return nil, err
	}
	return decodeSymbolInformations(raw)
}

// decodeLocations accepts Location | Location[] | LocationLink[] | null.
func decodeLocations(raw json.RawMessage) ([]Location, error) {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	// Array of Location or LocationLink.
	if raw[0] == '[' {
		var locs []Location
		if err := json.Unmarshal(raw, &locs); err == nil && locationSliceOK(locs) {
			return filterLocations(locs), nil
		}
		var links []locationLink
		if err := json.Unmarshal(raw, &links); err == nil {
			out := make([]Location, 0, len(links))
			for _, l := range links {
				if l.TargetURI == "" {
					continue
				}
				r := l.TargetSelectionRange
				if r == (Range{}) {
					r = l.TargetRange
				}
				out = append(out, Location{URI: l.TargetURI, Range: r})
			}
			return filterLocations(out), nil
		}
		return nil, fmt.Errorf("decode definition/references: unexpected array")
	}
	// Single Location object.
	var loc Location
	if err := json.Unmarshal(raw, &loc); err != nil {
		return nil, fmt.Errorf("decode location: %w", err)
	}
	if loc.URI == "" {
		return nil, nil
	}
	return []Location{loc}, nil
}

func locationSliceOK(locs []Location) bool {
	// Empty array is valid. Non-empty must have at least one URI (vs LocationLink
	// which uses targetUri and would leave URI empty after Location unmarshal).
	if len(locs) == 0 {
		return true
	}
	for _, l := range locs {
		if l.URI != "" {
			return true
		}
	}
	return false
}

func filterLocations(in []Location) []Location {
	if len(in) == 0 {
		return nil
	}
	out := make([]Location, 0, len(in))
	for _, l := range in {
		if strings.TrimSpace(l.URI) == "" {
			continue
		}
		out = append(out, l)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func decodeDocumentSymbols(raw json.RawMessage, fallbackPath string) ([]Symbol, error) {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	// LSP allows DocumentSymbol[] or SymbolInformation[]. Probe the first
	// element: SymbolInformation carries location.uri.
	var elems []json.RawMessage
	if err := json.Unmarshal(raw, &elems); err != nil {
		return nil, fmt.Errorf("decode document symbols: %w", err)
	}
	if len(elems) == 0 {
		return nil, nil
	}
	var probe struct {
		Location *Location `json:"location"`
		Name     string    `json:"name"`
	}
	if err := json.Unmarshal(elems[0], &probe); err == nil && probe.Location != nil && probe.Location.URI != "" {
		return decodeSymbolInformations(raw)
	}
	var docs []documentSymbol
	if err := json.Unmarshal(raw, &docs); err != nil {
		return nil, fmt.Errorf("decode document symbols: %w", err)
	}
	var out []Symbol
	flattenDocumentSymbols(docs, fallbackPath, "", &out)
	return out, nil
}

func flattenDocumentSymbols(docs []documentSymbol, path, container string, out *[]Symbol) {
	for _, d := range docs {
		r := d.SelectionRange
		if r == (Range{}) {
			r = d.Range
		}
		*out = append(*out, Symbol{
			Name:          d.Name,
			Kind:          d.Kind,
			Path:          path,
			Range:         r,
			ContainerName: container,
		})
		if len(d.Children) > 0 {
			flattenDocumentSymbols(d.Children, path, d.Name, out)
		}
	}
}

func decodeSymbolInformations(raw json.RawMessage) ([]Symbol, error) {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var infos []symbolInformation
	if err := json.Unmarshal(raw, &infos); err != nil {
		return nil, fmt.Errorf("decode symbols: %w", err)
	}
	out := make([]Symbol, 0, len(infos))
	for _, info := range infos {
		if strings.TrimSpace(info.Name) == "" {
			continue
		}
		path := URIToPath(info.Location.URI)
		out = append(out, Symbol{
			Name:          info.Name,
			Kind:          info.Kind,
			Path:          path,
			Range:         info.Location.Range,
			ContainerName: info.ContainerName,
		})
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func dedupeSymbols(in []Symbol) []Symbol {
	if len(in) <= 1 {
		return in
	}
	type key struct {
		name, path string
		line, col  int
		kind       int
	}
	seen := make(map[key]struct{}, len(in))
	out := make([]Symbol, 0, len(in))
	for _, s := range in {
		k := key{s.Name, s.Path, s.Range.Start.Line, s.Range.Start.Character, s.Kind}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, s)
	}
	return out
}

// SymbolKindName returns a short label for an LSP SymbolKind value.
func SymbolKindName(kind int) string {
	switch kind {
	case SymbolKindFile:
		return "file"
	case SymbolKindModule:
		return "module"
	case SymbolKindNamespace:
		return "namespace"
	case SymbolKindPackage:
		return "package"
	case SymbolKindClass:
		return "class"
	case SymbolKindMethod:
		return "method"
	case SymbolKindProperty:
		return "property"
	case SymbolKindField:
		return "field"
	case SymbolKindConstructor:
		return "constructor"
	case SymbolKindEnum:
		return "enum"
	case SymbolKindInterface:
		return "interface"
	case SymbolKindFunction:
		return "function"
	case SymbolKindVariable:
		return "variable"
	case SymbolKindConstant:
		return "constant"
	case SymbolKindString:
		return "string"
	case SymbolKindNumber:
		return "number"
	case SymbolKindBoolean:
		return "boolean"
	case SymbolKindArray:
		return "array"
	case SymbolKindObject:
		return "object"
	case SymbolKindKey:
		return "key"
	case SymbolKindNull:
		return "null"
	case SymbolKindEnumMember:
		return "enum_member"
	case SymbolKindStruct:
		return "struct"
	case SymbolKindEvent:
		return "event"
	case SymbolKindOperator:
		return "operator"
	case SymbolKindTypeParameter:
		return "type_parameter"
	default:
		if kind <= 0 {
			return "symbol"
		}
		return fmt.Sprintf("kind_%d", kind)
	}
}

// FormatLocations builds model-facing text for definition/references results.
// workDir makes paths relative when set. Positions are shown 1-based.
func FormatLocations(workDir string, locs []Location, maxLocs, maxChars int) string {
	if maxLocs <= 0 {
		maxLocs = DefaultNavMaxLocations
	}
	if maxChars <= 0 {
		maxChars = DefaultNavMaxChars
	}
	if len(locs) == 0 {
		return "No results."
	}
	// Stable order by path then position.
	sorted := append([]Location(nil), locs...)
	sort.SliceStable(sorted, func(i, j int) bool {
		pi, pj := URIToPath(sorted[i].URI), URIToPath(sorted[j].URI)
		if pi != pj {
			return pi < pj
		}
		if sorted[i].Range.Start.Line != sorted[j].Range.Start.Line {
			return sorted[i].Range.Start.Line < sorted[j].Range.Start.Line
		}
		return sorted[i].Range.Start.Character < sorted[j].Range.Start.Character
	})

	total := len(sorted)
	if len(sorted) > maxLocs {
		sorted = sorted[:maxLocs]
	}
	var b strings.Builder
	runesUsed := 0
	written := 0
	for _, loc := range sorted {
		path := displayPath(workDir, URIToPath(loc.URI))
		line := loc.Range.Start.Line + 1
		col := loc.Range.Start.Character + 1
		text := fmt.Sprintf("%s:%d:%d", path, line, col)
		lineRunes := utf8.RuneCountInString(text)
		if written > 0 {
			lineRunes++ // newline
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
		return "No results."
	}
	return b.String()
}

// FormatSymbols builds model-facing text for symbol results.
func FormatSymbols(workDir string, syms []Symbol, maxSyms, maxChars int) string {
	if maxSyms <= 0 {
		maxSyms = DefaultNavMaxSymbols
	}
	if maxChars <= 0 {
		maxChars = DefaultNavMaxChars
	}
	if len(syms) == 0 {
		return "No symbols."
	}
	sorted := append([]Symbol(nil), syms...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Path != sorted[j].Path {
			return sorted[i].Path < sorted[j].Path
		}
		if sorted[i].Range.Start.Line != sorted[j].Range.Start.Line {
			return sorted[i].Range.Start.Line < sorted[j].Range.Start.Line
		}
		if sorted[i].Name != sorted[j].Name {
			return sorted[i].Name < sorted[j].Name
		}
		return sorted[i].Kind < sorted[j].Kind
	})
	total := len(sorted)
	if len(sorted) > maxSyms {
		sorted = sorted[:maxSyms]
	}
	var b strings.Builder
	runesUsed := 0
	written := 0
	for _, s := range sorted {
		path := displayPath(workDir, s.Path)
		line := s.Range.Start.Line + 1
		col := s.Range.Start.Character + 1
		kind := SymbolKindName(s.Kind)
		name := strings.TrimSpace(s.Name)
		if name == "" {
			name = "?"
		}
		var text string
		if path != "" {
			text = fmt.Sprintf("%s %s  %s:%d:%d", kind, name, path, line, col)
		} else {
			text = fmt.Sprintf("%s %s", kind, name)
		}
		if c := strings.TrimSpace(s.ContainerName); c != "" {
			text += "  (" + c + ")"
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
		return "No symbols."
	}
	return b.String()
}
