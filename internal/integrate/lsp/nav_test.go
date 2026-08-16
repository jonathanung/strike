package lsp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestManagerDefinitionReferencesSymbols(t *testing.T) {
	cmd, args, env := helperCommand(t, "nav")
	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	if err := os.WriteFile(src, []byte("package main\nfunc Foo() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	m := NewManager(dir)
	m.StartAll(ctx, []ServerConfig{{
		Name: "go", Command: cmd, Args: args, Env: env,
		RootDir: dir, WorkDir: dir, Extensions: []string{".go"},
	}})
	defer m.Close()

	// Wait until up.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		sts := m.Statuses()
		if len(sts) == 1 && sts[0].State == "up" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if st := m.Statuses(); len(st) != 1 || st[0].State != "up" {
		t.Fatalf("server not up: %#v", st)
	}

	locs, err := m.Definition(ctx, src, 1, 5)
	if err != nil {
		t.Fatalf("Definition: %v", err)
	}
	if len(locs) != 1 {
		t.Fatalf("Definition locs = %#v", locs)
	}
	if URIToPath(locs[0].URI) != src {
		t.Fatalf("Definition path = %q want %q", URIToPath(locs[0].URI), src)
	}
	if locs[0].Range.Start.Line != 1 {
		t.Fatalf("Definition line = %d", locs[0].Range.Start.Line)
	}

	refs, err := m.References(ctx, src, 1, 5)
	if err != nil {
		t.Fatalf("References: %v", err)
	}
	if len(refs) != 2 {
		t.Fatalf("References = %#v", refs)
	}

	syms, err := m.DocumentSymbols(ctx, src)
	if err != nil {
		t.Fatalf("DocumentSymbols: %v", err)
	}
	if len(syms) < 2 {
		t.Fatalf("DocumentSymbols = %#v (want Foo + helper)", syms)
	}
	names := map[string]bool{}
	for _, s := range syms {
		names[s.Name] = true
		if s.Path != src {
			t.Fatalf("symbol path = %q want %q", s.Path, src)
		}
	}
	if !names["Foo"] || !names["helper"] {
		t.Fatalf("symbol names = %v", names)
	}

	wsyms, err := m.WorkspaceSymbols(ctx, "Baz")
	if err != nil {
		t.Fatalf("WorkspaceSymbols: %v", err)
	}
	if len(wsyms) != 1 || wsyms[0].Name != "Baz" {
		t.Fatalf("WorkspaceSymbols = %#v", wsyms)
	}
}

func TestManagerNavNoServer(t *testing.T) {
	m := NewManager(t.TempDir())
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "x.go")
	if _, err := m.Definition(ctx, path, 0, 0); err == nil {
		t.Fatal("expected error with no server")
	}
	if _, err := m.WorkspaceSymbols(ctx, "x"); err == nil {
		t.Fatal("expected workspace error with no servers")
	}
}

func TestManagerNavDeadServer(t *testing.T) {
	cmd, args, env := helperCommand(t, "exit-after-init")
	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	if err := os.WriteFile(src, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	m := NewManager(dir)
	m.StartAll(ctx, []ServerConfig{{
		Name: "go", Command: cmd, Args: args, Env: env,
		RootDir: dir, Extensions: []string{".go"},
	}})
	defer m.Close()

	// Wait for crash.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if c := m.clientForPath(src); c == nil || c.Closed() {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Should not panic; soft error.
	_, err := m.Definition(ctx, src, 0, 0)
	if err == nil {
		// Server may still be briefly up; try workspace which needs live clients.
		_, err = m.WorkspaceSymbols(ctx, "x")
	}
	// Either way, no panic — error or empty is fine once dead.
	_ = err
}

func TestDecodeLocationsVariants(t *testing.T) {
	// null
	locs, err := decodeLocations([]byte("null"))
	if err != nil || locs != nil {
		t.Fatalf("null: %v %#v", err, locs)
	}
	// single Location
	locs, err = decodeLocations([]byte(`{"uri":"file:///a.go","range":{"start":{"line":1,"character":2},"end":{"line":1,"character":5}}}`))
	if err != nil || len(locs) != 1 || locs[0].Range.Start.Character != 2 {
		t.Fatalf("single: %v %#v", err, locs)
	}
	// LocationLink array
	locs, err = decodeLocations([]byte(`[{"targetUri":"file:///b.go","targetRange":{"start":{"line":0,"character":0},"end":{"line":0,"character":1}},"targetSelectionRange":{"start":{"line":0,"character":0},"end":{"line":0,"character":1}}}]`))
	if err != nil || len(locs) != 1 || URIToPath(locs[0].URI) == "" {
		t.Fatalf("links: %v %#v", err, locs)
	}
}

func TestFormatLocationsAndSymbols(t *testing.T) {
	dir := "/proj"
	locs := []Location{
		{URI: PathToURI("/proj/a.go"), Range: Range{Start: Position{Line: 0, Character: 0}}},
		{URI: PathToURI("/proj/b.go"), Range: Range{Start: Position{Line: 2, Character: 4}}},
	}
	out := FormatLocations(dir, locs, 0, 0)
	if !strings.Contains(out, "a.go:1:1") || !strings.Contains(out, "b.go:3:5") {
		t.Fatalf("FormatLocations = %q", out)
	}
	if FormatLocations(dir, nil, 0, 0) != "No results." {
		t.Fatal("empty locations")
	}

	syms := []Symbol{
		{Name: "Foo", Kind: SymbolKindFunction, Path: "/proj/a.go", Range: Range{Start: Position{Line: 1, Character: 0}}, ContainerName: "pkg"},
	}
	sout := FormatSymbols(dir, syms, 0, 0)
	if !strings.Contains(sout, "function Foo") || !strings.Contains(sout, "a.go:2:1") || !strings.Contains(sout, "(pkg)") {
		t.Fatalf("FormatSymbols = %q", sout)
	}
	if FormatSymbols(dir, nil, 0, 0) != "No symbols." {
		t.Fatal("empty symbols")
	}

	// Cap
	many := make([]Location, 0, 5)
	for i := 0; i < 5; i++ {
		many = append(many, Location{
			URI:   PathToURI("/proj/a.go"),
			Range: Range{Start: Position{Line: i, Character: 0}},
		})
	}
	capped := FormatLocations(dir, many, 2, 0)
	if !strings.Contains(capped, "more truncated") {
		t.Fatalf("cap note missing: %q", capped)
	}
}

func TestSymbolKindName(t *testing.T) {
	if SymbolKindName(SymbolKindFunction) != "function" {
		t.Fatal(SymbolKindName(SymbolKindFunction))
	}
	if SymbolKindName(0) != "symbol" {
		t.Fatal(SymbolKindName(0))
	}
	if SymbolKindName(99) != "kind_99" {
		t.Fatal(SymbolKindName(99))
	}
}

func TestClientNavNilReceiver(t *testing.T) {
	var c *Client
	ctx := context.Background()
	if _, err := c.Definition(ctx, "/x.go", 0, 0); err == nil {
		t.Fatal("nil Definition want error")
	}
	if _, err := c.References(ctx, "/x.go", 0, 0, true); err == nil {
		t.Fatal("nil References want error")
	}
	if _, err := c.DocumentSymbols(ctx, "/x.go"); err == nil {
		t.Fatal("nil DocumentSymbols want error")
	}
	if _, err := c.WorkspaceSymbols(ctx, "q"); err == nil {
		t.Fatal("nil WorkspaceSymbols want error")
	}
}
