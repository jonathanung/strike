package lsp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func startIntelManager(t *testing.T, mode string) (context.Context, context.CancelFunc, *Manager, string, string) {
	t.Helper()
	cmd, args, env := helperCommand(t, mode)
	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	if err := os.WriteFile(src, []byte("package main\nfunc Foo() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "other.go")
	if err := os.WriteFile(other, []byte("package main\nfunc Bar() { Foo() }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	m := NewManager(dir)
	m.StartAll(ctx, []ServerConfig{{
		Name: "go", Command: cmd, Args: args, Env: env,
		RootDir: dir, WorkDir: dir, Extensions: []string{".go"},
	}})
	t.Cleanup(func() { m.Close() })
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		sts := m.Statuses()
		if len(sts) == 1 && sts[0].State == "up" {
			return ctx, cancel, m, dir, src
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("server not up: %#v", m.Statuses())
	return ctx, cancel, m, dir, src
}

func TestManagerCallHierarchyAndRename(t *testing.T) {
	ctx, cancel, m, dir, src := startIntelManager(t, "intel")
	defer cancel()

	caps, err := m.Capabilities(src)
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if !caps.CallHierarchy || !caps.Rename || !caps.PrepareRename || !caps.DocumentHighlight {
		t.Fatalf("caps = %+v", caps)
	}

	incoming, err := m.IncomingCalls(ctx, src, 1, 5)
	if err != nil {
		t.Fatalf("IncomingCalls: %v", err)
	}
	if len(incoming) != 2 {
		t.Fatalf("incoming = %#v", incoming)
	}
	seenOther := false
	for _, c := range incoming {
		if strings.Contains(URIToPath(c.URI), "other.go") {
			seenOther = true
		}
		if c.Range.Start.Line < 0 {
			t.Fatalf("missing location: %#v", c)
		}
	}
	if !seenOther {
		t.Fatalf("want multi-file incoming, got %#v", incoming)
	}

	outgoing, err := m.OutgoingCalls(ctx, src, 1, 5)
	if err != nil {
		t.Fatalf("OutgoingCalls: %v", err)
	}
	if len(outgoing) != 1 || outgoing[0].Name != "helper" {
		t.Fatalf("outgoing = %#v", outgoing)
	}

	preview, err := m.RenamePreview(ctx, src, 1, 5, "Bar")
	if err != nil {
		t.Fatalf("RenamePreview: %v", err)
	}
	if preview.Files != 2 || len(preview.Edits) < 2 {
		t.Fatalf("preview = %#v", preview)
	}
	// Must not write the workspace.
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "Bar") {
		t.Fatalf("rename preview wrote the workspace: %q", data)
	}
	_ = dir

	sum, err := m.Impact(ctx, src, 1, 5, "Bar")
	if err != nil {
		t.Fatalf("Impact: %v", err)
	}
	if sum.Counts[ImpactDefinition] < 1 || sum.Counts[ImpactCaller] < 1 || sum.Counts[ImpactCallee] < 1 {
		t.Fatalf("counts = %#v notes=%v", sum.Counts, sum.Notes)
	}
	if sum.Counts[ImpactRead]+sum.Counts[ImpactWrite]+sum.Counts[ImpactReference] < 1 {
		t.Fatalf("missing usage kinds: %#v", sum.Counts)
	}
	if sum.Counts[ImpactRename] < 1 {
		t.Fatalf("missing rename impact: %#v", sum.Counts)
	}
	if len(sum.Groups) < 2 {
		t.Fatalf("want multi-file groups, got %#v", sum.Groups)
	}
}

func TestManagerIntelUnsupported(t *testing.T) {
	ctx, cancel, m, _, src := startIntelManager(t, "nav")
	defer cancel()

	caps, err := m.Capabilities(src)
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if caps.CallHierarchy || caps.Rename {
		t.Fatalf("nav mode should not advertise intel: %+v", caps)
	}
	_, err = m.IncomingCalls(ctx, src, 0, 0)
	if !IsUnsupported(err) {
		t.Fatalf("incoming want unsupported, got %v", err)
	}
	if err == nil || !strings.Contains(err.Error(), "references") {
		t.Fatalf("fallback missing: %v", err)
	}
	_, err = m.RenamePreview(ctx, src, 0, 0, "X")
	if !IsUnsupported(err) {
		t.Fatalf("rename want unsupported, got %v", err)
	}
}

func TestManagerIntelMalformed(t *testing.T) {
	ctx, cancel, m, _, src := startIntelManager(t, "intel-bad")
	defer cancel()
	_, err := m.RenamePreview(ctx, src, 1, 0, "X")
	if err == nil {
		t.Fatal("want decode error")
	}
	if IsUnsupported(err) {
		t.Fatalf("malformed should not be unsupported: %v", err)
	}
}

func TestManagerIntelBounded(t *testing.T) {
	ctx, cancel, m, _, src := startIntelManager(t, "intel-many")
	defer cancel()
	incoming, err := m.IncomingCalls(ctx, src, 1, 0)
	if err != nil {
		t.Fatalf("IncomingCalls: %v", err)
	}
	if len(incoming) != DefaultIntelMaxCalls {
		t.Fatalf("bounded incoming = %d, want %d", len(incoming), DefaultIntelMaxCalls)
	}
	preview, err := m.RenamePreview(ctx, src, 1, 0, "Z")
	if err != nil {
		t.Fatalf("RenamePreview: %v", err)
	}
	if !preview.Truncated || len(preview.Edits) != DefaultIntelMaxEdits {
		t.Fatalf("bounded preview = %#v", preview)
	}
}

func TestDecodeWorkspaceEditAndCalls(t *testing.T) {
	edits, err := decodeWorkspaceEdit([]byte("null"))
	if err != nil || edits != nil {
		t.Fatalf("null: %v %#v", err, edits)
	}
	edits, err = decodeWorkspaceEdit([]byte(`{"changes":{"file:///a.go":[{"range":{"start":{"line":1,"character":2},"end":{"line":1,"character":5}},"newText":"Bar"}]}}`))
	if err != nil || len(edits) != 1 || edits[0].NewText != "Bar" {
		t.Fatalf("changes: %v %#v", err, edits)
	}
	edits, err = decodeWorkspaceEdit([]byte(`{"documentChanges":[{"textDocument":{"uri":"file:///b.go","version":1},"edits":[{"range":{"start":{"line":0,"character":0},"end":{"line":0,"character":1}},"newText":"X"}]}]}`))
	if err != nil || len(edits) != 1 || !strings.Contains(edits[0].Path, "b.go") {
		t.Fatalf("documentChanges: %v %#v", err, edits)
	}
	_, err = decodeWorkspaceEdit([]byte(`{"documentChanges":[{"nope":true}]}`))
	if err == nil {
		t.Fatal("want malformed documentChange error")
	}

	items, err := decodeCallHierarchyItems([]byte("null"))
	if err != nil || items != nil {
		t.Fatalf("items null: %v %#v", err, items)
	}
	_, err = decodeHierarchyCalls([]byte(`{`), true)
	if err == nil {
		t.Fatal("want malformed incoming")
	}
}

func TestFormatIntelHelpers(t *testing.T) {
	calls := []Call{{
		Name: "Foo", Kind: SymbolKindFunction, URI: PathToURI("/proj/a.go"),
		Range: Range{Start: Position{Line: 2, Character: 0}},
	}}
	out := FormatCalls("/proj", calls, 0, 0)
	if !strings.Contains(out, "function Foo") || !strings.Contains(out, "a.go:3:1") {
		t.Fatalf("FormatCalls = %q", out)
	}
	if FormatCalls("/proj", nil, 0, 0) != "No calls." {
		t.Fatal("empty calls")
	}
	preview := RenamePreview{NewName: "Bar", Files: 1, Edits: []TextEdit{{
		Path: "/proj/a.go", Range: Range{Start: Position{Line: 0, Character: 0}}, NewText: "Bar", Kind: "edit",
	}}}
	rout := FormatRenamePreview("/proj", preview, 0, 0)
	if !strings.Contains(rout, "not applied") || !strings.Contains(rout, "a.go:1:1") {
		t.Fatalf("FormatRenamePreview = %q", rout)
	}
	many := RenamePreview{NewName: "Z", Files: 1}
	for i := 0; i < 5; i++ {
		many.Edits = append(many.Edits, TextEdit{Path: "/proj/a.go", Range: Range{Start: Position{Line: i}}, NewText: "Z", Kind: "edit"})
	}
	capped := FormatRenamePreview("/proj", many, 2, 0)
	if !strings.Contains(capped, "more truncated") {
		t.Fatalf("cap note missing: %q", capped)
	}
	sum := ImpactSummary{
		Counts: map[string]int{ImpactCaller: 1},
		Groups: []ImpactGroup{{
			File: "/proj/a.go", Package: "proj",
			Items: []ImpactItem{{Kind: ImpactCaller, Path: "/proj/a.go", Line: 4, Character: 1, Name: "Bar"}},
		}},
	}
	iout := FormatImpact("/proj", sum, 0, 0)
	if !strings.Contains(iout, "caller Bar") || !strings.Contains(iout, "a.go:4:1") {
		t.Fatalf("FormatImpact = %q", iout)
	}
}

func TestUnsupportedError(t *testing.T) {
	err := unsupported("call hierarchy", "the references tool")
	if !IsUnsupported(err) {
		t.Fatal("IsUnsupported")
	}
	if !strings.Contains(err.Error(), "references") {
		t.Fatal(err)
	}
	if IsUnsupported(nil) || IsUnsupported(context.Canceled) {
		t.Fatal("false positive")
	}
}

func TestProviderEnabled(t *testing.T) {
	if providerEnabled(nil) || providerEnabled(json.RawMessage("false")) || providerEnabled(json.RawMessage("null")) {
		t.Fatal("false should be disabled")
	}
	if !providerEnabled(json.RawMessage("true")) || !providerEnabled(json.RawMessage(`{"prepareProvider":true}`)) {
		t.Fatal("true / object should be enabled")
	}
	if !renamePrepareEnabled(json.RawMessage(`{"prepareProvider":true}`)) {
		t.Fatal("prepare")
	}
	if renamePrepareEnabled(json.RawMessage("true")) {
		t.Fatal("boolean rename has no prepare")
	}
}

func TestClientIntelNilReceiver(t *testing.T) {
	var c *Client
	ctx := context.Background()
	if _, err := c.IncomingCalls(ctx, "/x.go", 0, 0); err == nil {
		t.Fatal("nil IncomingCalls")
	}
	if _, err := c.RenamePreview(ctx, "/x.go", 0, 0, "Y"); err == nil {
		t.Fatal("nil RenamePreview")
	}
	if _, err := c.DocumentHighlights(ctx, "/x.go", 0, 0); err == nil {
		t.Fatal("nil DocumentHighlights")
	}
	if c.ServerCaps().CallHierarchy {
		t.Fatal("nil caps")
	}
}
