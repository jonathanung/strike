package tools

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jonathanung/strike-cli/harness/tool"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/lsp"
)

type fakeIntel struct {
	caps    lsp.ServerCaps
	capsErr error
	in      []lsp.Call
	inErr   error
	out     []lsp.Call
	outErr  error
	preview lsp.RenamePreview
	renErr  error
	impact  lsp.ImpactSummary
	impErr  error

	lastPath string
	lastLine int
	lastChar int
	lastName string
}

func (f *fakeIntel) Capabilities(absPath string) (lsp.ServerCaps, error) {
	f.lastPath = absPath
	return f.caps, f.capsErr
}

func (f *fakeIntel) IncomingCalls(_ context.Context, absPath string, line, character int) ([]lsp.Call, error) {
	f.lastPath, f.lastLine, f.lastChar = absPath, line, character
	return f.in, f.inErr
}

func (f *fakeIntel) OutgoingCalls(_ context.Context, absPath string, line, character int) ([]lsp.Call, error) {
	f.lastPath, f.lastLine, f.lastChar = absPath, line, character
	return f.out, f.outErr
}

func (f *fakeIntel) RenamePreview(_ context.Context, absPath string, line, character int, newName string) (lsp.RenamePreview, error) {
	f.lastPath, f.lastLine, f.lastChar, f.lastName = absPath, line, character, newName
	return f.preview, f.renErr
}

func (f *fakeIntel) Impact(_ context.Context, absPath string, line, character int, newName string) (lsp.ImpactSummary, error) {
	f.lastPath, f.lastLine, f.lastChar, f.lastName = absPath, line, character, newName
	return f.impact, f.impErr
}

func writeGo(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "a.go")
	if err := os.WriteFile(path, []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCallHierarchyTool(t *testing.T) {
	dir := t.TempDir()
	path := writeGo(t, dir)
	intel := &fakeIntel{
		caps: lsp.ServerCaps{CallHierarchy: true},
		in: []lsp.Call{{
			Name: "Caller", Kind: lsp.SymbolKindFunction,
			URI:   lsp.PathToURI(path),
			Range: lsp.Range{Start: lsp.Position{Line: 3, Character: 1}},
		}},
		out: []lsp.Call{{
			Name: "Callee", Kind: lsp.SymbolKindMethod,
			URI:   lsp.PathToURI(filepath.Join(dir, "b.go")),
			Range: lsp.Range{Start: lsp.Position{Line: 8, Character: 0}},
		}},
	}
	res, err := NewCallHierarchy(intel).Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath":  "a.go",
		"line":      2,
		"character": 4,
		"direction": "both",
	}), allowAll(dir))
	if err != nil {
		t.Fatal(err)
	}
	if intel.lastLine != 1 || intel.lastChar != 4 {
		t.Fatalf("position: line=%d char=%d", intel.lastLine, intel.lastChar)
	}
	if res.Title != "2 calls" {
		t.Fatalf("title = %q", res.Title)
	}
	if !strings.Contains(res.Output, `"incoming"`) || !strings.Contains(res.Output, "Caller") {
		t.Fatalf("output = %q", res.Output)
	}
	if !strings.Contains(res.Output, "a.go") || !strings.Contains(res.Output, `"line": 4`) {
		t.Fatalf("location missing: %q", res.Output)
	}
}

func TestCallHierarchyUnsupportedAndMalformed(t *testing.T) {
	dir := t.TempDir()
	writeGo(t, dir)
	tc := allowAll(dir)
	res, err := NewCallHierarchy(&fakeIntel{}).Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath": "a.go",
		"line":     1,
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if res.Title != "call hierarchy unsupported" || !strings.Contains(res.Output, "references") {
		t.Fatalf("unsupported = %q %q", res.Title, res.Output)
	}

	_, err = NewCallHierarchy(&fakeIntel{}).Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath":  "a.go",
		"line":      1,
		"direction": "sideways",
	}), tc)
	if err == nil || !strings.Contains(err.Error(), "direction") {
		t.Fatalf("want direction error, got %v", err)
	}
	_, err = NewCallHierarchy(&fakeIntel{}).Execute(context.Background(), json.RawMessage(`{`), tc)
	if err == nil {
		t.Fatal("want invalid json")
	}
	res, err = NewCallHierarchy(nil).Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath": "a.go",
		"line":     1,
	}), tc)
	if err != nil || !strings.Contains(res.Output, "not configured") {
		t.Fatalf("nil intel: %v %q", err, res.Output)
	}
}

func TestCallHierarchyBounded(t *testing.T) {
	dir := t.TempDir()
	path := writeGo(t, dir)
	calls := make([]lsp.Call, 0, 8)
	for i := 0; i < 8; i++ {
		calls = append(calls, lsp.Call{
			Name: "C", URI: lsp.PathToURI(path),
			Range: lsp.Range{Start: lsp.Position{Line: i}},
		})
	}
	res, err := NewCallHierarchy(&fakeIntel{
		caps: lsp.ServerCaps{CallHierarchy: true},
		in:   calls,
	}).Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath":   "a.go",
		"line":       1,
		"direction":  "incoming",
		"maxResults": 3,
	}), allowAll(dir))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Title, "truncated") || !strings.Contains(res.Output, `"truncated": true`) {
		t.Fatalf("bounded = %q %q", res.Title, res.Output)
	}
}

func TestRenamePreviewTool(t *testing.T) {
	dir := t.TempDir()
	path := writeGo(t, dir)
	other := filepath.Join(dir, "b.go")
	if err := os.WriteFile(other, []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	intel := &fakeIntel{
		caps: lsp.ServerCaps{Rename: true},
		preview: lsp.RenamePreview{
			NewName: "Bar",
			Files:   2,
			Edits: []lsp.TextEdit{
				{Path: path, Range: lsp.Range{Start: lsp.Position{Line: 0, Character: 8}}, NewText: "Bar", Kind: "edit"},
				{Path: other, Range: lsp.Range{Start: lsp.Position{Line: 2, Character: 0}}, NewText: "Bar", Kind: "edit"},
			},
		},
	}
	res, err := NewRenamePreview(intel).Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath": "a.go",
		"line":     1,
		"newName":  "Bar",
	}), allowAll(dir))
	if err != nil {
		t.Fatal(err)
	}
	if intel.lastName != "Bar" {
		t.Fatalf("newName = %q", intel.lastName)
	}
	if res.Title != "2 rename edits" {
		t.Fatalf("title = %q", res.Title)
	}
	if !strings.Contains(res.Output, `"applied": false`) || !strings.Contains(res.Output, "Workspace was not modified") {
		t.Fatalf("output = %q", res.Output)
	}
	if !strings.Contains(res.Output, "b.go") {
		t.Fatalf("multi-file missing: %q", res.Output)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "Bar") {
		t.Fatal("preview wrote the file")
	}
}

func TestRenamePreviewUnsupportedAndValidation(t *testing.T) {
	dir := t.TempDir()
	writeGo(t, dir)
	tc := allowAll(dir)
	res, err := NewRenamePreview(&fakeIntel{}).Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath": "a.go",
		"line":     1,
		"newName":  "X",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if res.Title != "rename preview unsupported" || !strings.Contains(res.Output, "references") {
		t.Fatalf("unsupported = %q %q", res.Title, res.Output)
	}
	_, err = NewRenamePreview(&fakeIntel{caps: lsp.ServerCaps{Rename: true}}).Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath": "a.go",
		"line":     1,
	}), tc)
	if err == nil || !strings.Contains(err.Error(), "newName") {
		t.Fatalf("want newName error, got %v", err)
	}
}

func TestImpactTool(t *testing.T) {
	dir := t.TempDir()
	path := writeGo(t, dir)
	other := filepath.Join(dir, "pkg", "b.go")
	if err := os.MkdirAll(filepath.Dir(other), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte("package pkg\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	intel := &fakeIntel{
		impact: lsp.ImpactSummary{
			Capabilities: lsp.ServerCaps{Definition: true, References: true, CallHierarchy: true},
			Counts:       map[string]int{lsp.ImpactDefinition: 1, lsp.ImpactCaller: 1, lsp.ImpactRead: 1},
			Groups: []lsp.ImpactGroup{
				{File: path, Package: dir, Items: []lsp.ImpactItem{
					{Kind: lsp.ImpactDefinition, Path: path, Line: 2, Character: 6, Name: "Foo"},
					{Kind: lsp.ImpactRead, Path: path, Line: 4, Character: 1},
				}},
				{File: other, Package: "pkg", Items: []lsp.ImpactItem{
					{Kind: lsp.ImpactCaller, Path: other, Line: 10, Character: 2, Name: "Use"},
				}},
			},
		},
	}
	res, err := NewImpact(intel).Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath": "a.go",
		"line":     2,
		"newName":  "Bar",
	}), allowAll(dir))
	if err != nil {
		t.Fatal(err)
	}
	if intel.lastName != "Bar" || intel.lastLine != 1 {
		t.Fatalf("args name=%q line=%d", intel.lastName, intel.lastLine)
	}
	if !strings.Contains(res.Title, "impact") {
		t.Fatalf("title = %q", res.Title)
	}
	if !strings.Contains(res.Output, `"kind": "definition"`) || !strings.Contains(res.Output, `"kind": "caller"`) {
		t.Fatalf("kinds missing: %q", res.Output)
	}
	if !strings.Contains(res.Output, `"package": "pkg"`) {
		t.Fatalf("package grouping missing: %q", res.Output)
	}
}

func TestImpactBoundedAndSoftError(t *testing.T) {
	dir := t.TempDir()
	path := writeGo(t, dir)
	items := make([]lsp.ImpactItem, 0, 6)
	for i := 0; i < 6; i++ {
		items = append(items, lsp.ImpactItem{Kind: lsp.ImpactReference, Path: path, Line: i + 1, Character: 1})
	}
	res, err := NewImpact(&fakeIntel{
		impact: lsp.ImpactSummary{
			Counts: map[string]int{lsp.ImpactReference: 6},
			Groups: []lsp.ImpactGroup{{File: path, Items: items}},
		},
	}).Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath":   "a.go",
		"line":       1,
		"maxResults": 2,
	}), allowAll(dir))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Title, "truncated") {
		t.Fatalf("title = %q", res.Title)
	}

	res, err = NewImpact(&fakeIntel{impErr: errors.New("no language server for .go files")}).
		Execute(context.Background(), mustJSON(t, map[string]any{"filePath": "a.go", "line": 1}), allowAll(dir))
	if err != nil {
		t.Fatal(err)
	}
	if res.Title != "impact failed" {
		t.Fatalf("title = %q", res.Title)
	}
}

func TestIntelToolContractsAndDefer(t *testing.T) {
	for _, tl := range []tool.Tool{NewCallHierarchy(nil), NewRenamePreview(nil), NewImpact(nil)} {
		c := tool.LookupContract(tl)
		if err := c.Validate(); err != nil {
			t.Errorf("%s: %v", tl.Name(), err)
		}
		if c.SideEffect != tool.SideEffectRead || c.Idempotency != tool.IdempotencySafeRetry {
			t.Errorf("%s contract = %+v", tl.Name(), c)
		}
	}
	reg := tool.NewRegistry(tool.NewRead(), NewCallHierarchy(nil), NewRenamePreview(nil), NewImpact(nil))
	reg.Register(tool.NewToolSearch(reg))
	reg.SetDeferLoading(true)
	names := map[string]bool{}
	for _, s := range reg.SchemasForProvider() {
		names[s.Name] = true
	}
	if names["call_hierarchy"] || names["rename_preview"] || names["impact"] {
		t.Fatalf("intel tools should be deferred: %v", names)
	}
	res, err := tool.NewToolSearch(reg).Execute(context.Background(), mustJSON(t, map[string]any{
		"query": "lsp",
	}), allowAll(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "- call_hierarchy:") {
		t.Fatalf("search = %q", res.Output)
	}
}

func TestIntelPermissionAndEscape(t *testing.T) {
	dir := t.TempDir()
	writeGo(t, dir)
	tc := &tool.Context{WorkDir: dir, Ask: func(context.Context, tool.AskRequest) error { return errors.New("denied") }}
	_, err := NewImpact(&fakeIntel{}).Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath": "a.go", "line": 1,
	}), tc)
	if err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("got %v", err)
	}
	_, err = NewCallHierarchy(&fakeIntel{}).Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath": "../outside.go", "line": 1,
	}), allowAll(dir))
	var esc *tool.WorkspaceEscapeError
	if !errors.As(err, &esc) {
		t.Fatalf("want WorkspaceEscapeError, got %v", err)
	}
}
