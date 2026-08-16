package tools

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jonathanung/strike-cli/internal/tool"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/lsp"
)

type fakeNav struct {
	def    []lsp.Location
	defErr error
	refs   []lsp.Location
	refErr error
	docs   []lsp.Symbol
	docErr error
	wsyms  []lsp.Symbol
	wsErr  error

	lastDefPath string
	lastDefLine int
	lastDefChar int
	lastQuery   string
}

func (f *fakeNav) Definition(_ context.Context, absPath string, line, character int) ([]lsp.Location, error) {
	f.lastDefPath = absPath
	f.lastDefLine = line
	f.lastDefChar = character
	return f.def, f.defErr
}

func (f *fakeNav) References(_ context.Context, absPath string, line, character int) ([]lsp.Location, error) {
	f.lastDefPath = absPath
	f.lastDefLine = line
	f.lastDefChar = character
	return f.refs, f.refErr
}

func (f *fakeNav) DocumentSymbols(_ context.Context, absPath string) ([]lsp.Symbol, error) {
	f.lastDefPath = absPath
	return f.docs, f.docErr
}

func (f *fakeNav) WorkspaceSymbols(_ context.Context, query string) ([]lsp.Symbol, error) {
	f.lastQuery = query
	return f.wsyms, f.wsErr
}

func TestDefinitionTool(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.go")
	if err := os.WriteFile(path, []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	nav := &fakeNav{
		def: []lsp.Location{{
			URI:   lsp.PathToURI(path),
			Range: lsp.Range{Start: lsp.Position{Line: 2, Character: 4}},
		}},
	}
	tc := allowAll(dir)
	res, err := NewDefinition(nav).Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath":  "a.go",
		"line":      3,
		"character": 4,
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if nav.lastDefLine != 2 || nav.lastDefChar != 4 {
		t.Fatalf("position conversion: line=%d char=%d", nav.lastDefLine, nav.lastDefChar)
	}
	if !strings.Contains(res.Output, "a.go:3:5") {
		t.Fatalf("output = %q", res.Output)
	}
	if res.Title != "1 definition" {
		t.Fatalf("title = %q", res.Title)
	}
}

func TestDefinitionToolValidation(t *testing.T) {
	tc := allowAll(t.TempDir())
	_, err := NewDefinition(&fakeNav{}).Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath": "a.go",
		"line":     0,
	}), tc)
	if err == nil || !strings.Contains(err.Error(), "line") {
		t.Fatalf("want line error, got %v", err)
	}
	_, err = NewDefinition(&fakeNav{}).Execute(context.Background(), mustJSON(t, map[string]any{
		"line": 1,
	}), tc)
	if err == nil || !strings.Contains(err.Error(), "filePath") {
		t.Fatalf("want filePath error, got %v", err)
	}
	_, err = NewDefinition(&fakeNav{}).Execute(context.Background(), json.RawMessage(`{`), tc)
	if err == nil {
		t.Fatal("want invalid json error")
	}
}

func TestDefinitionNilNavAndSoftError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	tc := allowAll(dir)
	res, err := NewDefinition(nil).Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath": "a.go",
		"line":     1,
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "not configured") {
		t.Fatalf("output = %q", res.Output)
	}

	nav := &fakeNav{defErr: errors.New("no language server for .go files")}
	res, err = NewDefinition(nav).Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath": "a.go",
		"line":     1,
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "no language server") {
		t.Fatalf("soft error output = %q", res.Output)
	}
	if res.Title != "definition failed" {
		t.Fatalf("title = %q", res.Title)
	}
}

func TestReferencesTool(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.go")
	if err := os.WriteFile(path, []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	nav := &fakeNav{
		refs: []lsp.Location{
			{URI: lsp.PathToURI(path), Range: lsp.Range{Start: lsp.Position{Line: 0, Character: 0}}},
			{URI: lsp.PathToURI(path), Range: lsp.Range{Start: lsp.Position{Line: 1, Character: 0}}},
		},
	}
	res, err := NewReferences(nav).Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath": "a.go",
		"line":     1,
	}), allowAll(dir))
	if err != nil {
		t.Fatal(err)
	}
	if res.Title != "2 references" {
		t.Fatalf("title = %q", res.Title)
	}
	if !strings.Contains(res.Output, "a.go:1:1") || !strings.Contains(res.Output, "a.go:2:1") {
		t.Fatalf("output = %q", res.Output)
	}
}

func TestSymbolsToolDocumentAndWorkspace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.go")
	if err := os.WriteFile(path, []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	nav := &fakeNav{
		docs: []lsp.Symbol{
			{Name: "Foo", Kind: lsp.SymbolKindFunction, Path: path, Range: lsp.Range{Start: lsp.Position{Line: 0, Character: 0}}},
			{Name: "Bar", Kind: lsp.SymbolKindVariable, Path: path, Range: lsp.Range{Start: lsp.Position{Line: 1, Character: 0}}},
		},
		wsyms: []lsp.Symbol{
			{Name: "Baz", Kind: lsp.SymbolKindStruct, Path: path, Range: lsp.Range{Start: lsp.Position{Line: 2, Character: 0}}},
		},
	}
	tc := allowAll(dir)

	// Document outline.
	res, err := NewSymbols(nav).Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath": "a.go",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if res.Title != "2 symbols" {
		t.Fatalf("title = %q", res.Title)
	}
	if !strings.Contains(res.Output, "function Foo") || !strings.Contains(res.Output, "variable Bar") {
		t.Fatalf("output = %q", res.Output)
	}

	// Document + filter.
	res, err = NewSymbols(nav).Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath": "a.go",
		"query":    "foo",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if res.Title != "1 symbol" {
		t.Fatalf("filtered title = %q", res.Title)
	}
	if strings.Contains(res.Output, "Bar") {
		t.Fatalf("filter failed: %q", res.Output)
	}

	// Workspace.
	res, err = NewSymbols(nav).Execute(context.Background(), mustJSON(t, map[string]any{
		"query": "Baz",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if nav.lastQuery != "Baz" {
		t.Fatalf("query = %q", nav.lastQuery)
	}
	if !strings.Contains(res.Output, "struct Baz") {
		t.Fatalf("workspace output = %q", res.Output)
	}
}

func TestSymbolsToolRequiresArg(t *testing.T) {
	_, err := NewSymbols(&fakeNav{}).Execute(context.Background(), mustJSON(t, map[string]any{}), allowAll(t.TempDir()))
	if err == nil || !strings.Contains(err.Error(), "filePath or query") {
		t.Fatalf("got %v", err)
	}
}

func TestNavToolContracts(t *testing.T) {
	for _, tl := range []tool.Tool{NewDefinition(nil), NewReferences(nil), NewSymbols(nil)} {
		c := tool.LookupContract(tl)
		if err := c.Validate(); err != nil {
			t.Errorf("%s: %v", tl.Name(), err)
		}
		if c.SideEffect != tool.SideEffectRead || c.Idempotency != tool.IdempotencySafeRetry {
			t.Errorf("%s contract = %+v, want read/safe-retry", tl.Name(), c)
		}
	}
}

func TestNavToolsDeferred(t *testing.T) {
	reg := tool.NewRegistry(tool.NewRead(), NewDefinition(nil), NewReferences(nil), NewSymbols(nil))
	reg.Register(tool.NewToolSearch(reg))
	reg.SetDeferLoading(true)

	names := map[string]bool{}
	for _, s := range reg.SchemasForProvider() {
		names[s.Name] = true
	}
	if names["definition"] || names["references"] || names["symbols"] {
		t.Fatalf("nav tools should be deferred: %v", names)
	}
	if !names["read"] || !names["toolsearch"] {
		t.Fatalf("core missing: %v", names)
	}

	// toolsearch promotes them.
	res, err := tool.NewToolSearch(reg).Execute(context.Background(), mustJSON(t, map[string]any{
		"query": "definition",
	}), allowAll(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "- definition:") {
		t.Fatalf("search output = %q", res.Output)
	}
	if !reg.Discovered("definition") {
		t.Fatal("definition not discovered")
	}
	if schemaNameSet(reg.SchemasForProvider())["definition"] != true {
		t.Fatal("definition not in provider schemas after discover")
	}
}

func TestDefinitionPermissionDenied(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	tc := &tool.Context{
		WorkDir: dir,
		Ask: func(context.Context, tool.AskRequest) error {
			return errors.New("denied")
		},
	}
	_, err := NewDefinition(&fakeNav{}).Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath": "a.go",
		"line":     1,
	}), tc)
	if err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("got %v", err)
	}
}

func TestNavToolsWorkspaceEscape(t *testing.T) {
	dir := t.TempDir()
	tc := allowAll(dir)
	_, err := NewDefinition(&fakeNav{}).Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath": "../outside.go",
		"line":     1,
	}), tc)
	if err == nil {
		t.Fatal("expected workspace escape error")
	}
	var esc *tool.WorkspaceEscapeError
	if !errors.As(err, &esc) {
		t.Fatalf("want WorkspaceEscapeError, got %T %v", err, err)
	}
}
