package tool

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/lsp"
)

type fakeDiagSrc struct {
	byPath   map[string][]lsp.Diagnostic
	statuses []lsp.Status
	panicAll bool
}

func (f *fakeDiagSrc) AllDiagnostics() map[string][]lsp.Diagnostic {
	if f.panicAll {
		panic("boom")
	}
	if f.byPath == nil {
		return nil
	}
	out := make(map[string][]lsp.Diagnostic, len(f.byPath))
	for k, v := range f.byPath {
		cp := make([]lsp.Diagnostic, len(v))
		copy(cp, v)
		out[k] = cp
	}
	return out
}

func (f *fakeDiagSrc) Statuses() []lsp.Status {
	return append([]lsp.Status(nil), f.statuses...)
}

func parseDiagPayload(t *testing.T, res Result) diagnosticsPayload {
	t.Helper()
	var p diagnosticsPayload
	if err := json.Unmarshal([]byte(res.Output), &p); err != nil {
		t.Fatalf("unmarshal output: %v\n%s", err, res.Output)
	}
	var meta diagnosticsPayload
	if err := json.Unmarshal(res.Metadata, &meta); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}
	if p.Count != meta.Count || p.Total != meta.Total || p.Truncated != meta.Truncated {
		t.Fatalf("metadata mismatch output=%+v meta=%+v", p, meta)
	}
	return p
}

func TestDiagnosticsWorkspaceAndOrdering(t *testing.T) {
	dir := t.TempDir()
	aPath := filepath.Join(dir, "a.go")
	bPath := filepath.Join(dir, "b.go")
	src := &fakeDiagSrc{
		statuses: []lsp.Status{{Name: "go", State: "up", Command: "gopls"}},
		byPath: map[string][]lsp.Diagnostic{
			bPath: {{
				Range:    lsp.Range{Start: lsp.Position{Line: 0, Character: 0}, End: lsp.Position{Line: 0, Character: 3}},
				Severity: lsp.SeverityError,
				Source:   "compiler",
				Code:     "E1",
				Message:  "b error",
			}},
			aPath: {
				{
					Range:    lsp.Range{Start: lsp.Position{Line: 2, Character: 4}, End: lsp.Position{Line: 2, Character: 8}},
					Severity: lsp.SeverityWarning,
					Source:   "linter",
					Code:     "W2",
					Message:  "a warn",
				},
				{
					Range:    lsp.Range{Start: lsp.Position{Line: 0, Character: 1}, End: lsp.Position{Line: 0, Character: 2}},
					Severity: lsp.SeverityError,
					Message:  "a error",
				},
			},
		},
	}

	res, err := NewDiagnostics(src).Execute(context.Background(), mustJSON(t, map[string]any{
		"severity": "warning",
	}), allowAll(dir))
	if err != nil {
		t.Fatal(err)
	}
	p := parseDiagPayload(t, res)
	if p.Scope != "workspace" || p.Severity != "warning" {
		t.Fatalf("scope/sev = %s/%s", p.Scope, p.Severity)
	}
	if p.Count != 3 || p.Total != 3 || p.Truncated {
		t.Fatalf("counts = %+v", p)
	}
	if res.Title != "3 diagnostics" {
		t.Fatalf("title = %q", res.Title)
	}
	// Deterministic: a.go before b.go; within a.go line 1 error before line 3 warning.
	if p.Diagnostics[0].File != "a.go" || p.Diagnostics[0].Range.Start.Line != 1 {
		t.Fatalf("first = %+v", p.Diagnostics[0])
	}
	if p.Diagnostics[0].Severity != "error" || p.Diagnostics[0].Message != "a error" {
		t.Fatalf("first fields = %+v", p.Diagnostics[0])
	}
	if p.Diagnostics[1].File != "a.go" || p.Diagnostics[1].Severity != "warning" {
		t.Fatalf("second = %+v", p.Diagnostics[1])
	}
	if p.Diagnostics[1].Source != "linter" || p.Diagnostics[1].Code != "W2" {
		t.Fatalf("second source/code = %+v", p.Diagnostics[1])
	}
	if p.Diagnostics[1].Range.Start.Line != 3 || p.Diagnostics[1].Range.Start.Character != 5 {
		t.Fatalf("1-based range = %+v", p.Diagnostics[1].Range)
	}
	if p.Diagnostics[2].File != "b.go" {
		t.Fatalf("third = %+v", p.Diagnostics[2])
	}
	if len(p.Servers) != 1 || p.Servers[0].State != "up" {
		t.Fatalf("servers = %+v", p.Servers)
	}
}

func TestDiagnosticsSeverityFilter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.go")
	src := &fakeDiagSrc{
		statuses: []lsp.Status{{Name: "go", State: "up"}},
		byPath: map[string][]lsp.Diagnostic{
			path: {
				{Severity: lsp.SeverityError, Message: "err", Range: lsp.Range{Start: lsp.Position{Line: 0}}},
				{Severity: lsp.SeverityWarning, Message: "warn", Range: lsp.Range{Start: lsp.Position{Line: 1}}},
				{Severity: lsp.SeverityInformation, Message: "info", Range: lsp.Range{Start: lsp.Position{Line: 2}}},
				{Severity: lsp.SeverityHint, Message: "hint", Range: lsp.Range{Start: lsp.Position{Line: 3}}},
				{Severity: 0, Message: "omitted-sev-is-error", Range: lsp.Range{Start: lsp.Position{Line: 4}}},
			},
		},
	}
	tc := allowAll(dir)

	cases := []struct {
		sev  string
		want int
	}{
		{"", 2}, // default error (+ omitted severity)
		{"error", 2},
		{"warning", 3},
		{"info", 4},
		{"hint", 5},
	}
	for _, tcases := range cases {
		args := map[string]any{}
		if tcases.sev != "" {
			args["severity"] = tcases.sev
		}
		res, err := NewDiagnostics(src).Execute(context.Background(), mustJSON(t, args), tc)
		if err != nil {
			t.Fatalf("sev %q: %v", tcases.sev, err)
		}
		p := parseDiagPayload(t, res)
		if p.Count != tcases.want {
			t.Fatalf("sev %q: count=%d want %d (%+v)", tcases.sev, p.Count, tcases.want, p.Diagnostics)
		}
	}

	_, err := NewDiagnostics(src).Execute(context.Background(), mustJSON(t, map[string]any{
		"severity": "critical",
	}), tc)
	if err == nil || !strings.Contains(err.Error(), "severity") {
		t.Fatalf("want severity error, got %v", err)
	}
}

func TestDiagnosticsPathScopes(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "pkg")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	rootFile := filepath.Join(dir, "main.go")
	subFile := filepath.Join(sub, "lib.go")
	other := filepath.Join(dir, "other.go")
	src := &fakeDiagSrc{
		statuses: []lsp.Status{{Name: "go", State: "up"}},
		byPath: map[string][]lsp.Diagnostic{
			rootFile: {{Message: "root", Severity: lsp.SeverityError, Range: lsp.Range{Start: lsp.Position{Line: 0}}}},
			subFile:  {{Message: "sub", Severity: lsp.SeverityError, Range: lsp.Range{Start: lsp.Position{Line: 0}}}},
			other:    {{Message: "other", Severity: lsp.SeverityError, Range: lsp.Range{Start: lsp.Position{Line: 0}}}},
		},
	}
	tc := allowAll(dir)

	// File scope.
	res, err := NewDiagnostics(src).Execute(context.Background(), mustJSON(t, map[string]any{
		"path": "main.go",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	p := parseDiagPayload(t, res)
	if p.Scope != "file" || p.Path != "main.go" || p.Count != 1 || p.Diagnostics[0].Message != "root" {
		t.Fatalf("file scope = %+v", p)
	}

	// Directory scope.
	res, err = NewDiagnostics(src).Execute(context.Background(), mustJSON(t, map[string]any{
		"path": "pkg",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	p = parseDiagPayload(t, res)
	if p.Scope != "directory" || p.Path != "pkg" || p.Count != 1 || p.Diagnostics[0].Message != "sub" {
		t.Fatalf("dir scope = %+v", p)
	}

	// Workspace excludes paths outside workdir.
	outside := filepath.Join(t.TempDir(), "out.go")
	src.byPath[outside] = []lsp.Diagnostic{{Message: "out", Severity: lsp.SeverityError}}
	res, err = NewDiagnostics(src).Execute(context.Background(), mustJSON(t, map[string]any{}), tc)
	if err != nil {
		t.Fatal(err)
	}
	p = parseDiagPayload(t, res)
	if p.Count != 3 {
		t.Fatalf("workspace should exclude outside path: count=%d %+v", p.Count, p.Diagnostics)
	}
	for _, d := range p.Diagnostics {
		if strings.Contains(d.File, "out.go") || d.Message == "out" {
			t.Fatalf("leaked outside diagnostic: %+v", d)
		}
	}
}

func TestDiagnosticsBounds(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.go")
	diags := make([]lsp.Diagnostic, 0, 10)
	for i := 0; i < 10; i++ {
		diags = append(diags, lsp.Diagnostic{
			Severity: lsp.SeverityError,
			Message:  "m",
			Range:    lsp.Range{Start: lsp.Position{Line: i}},
		})
	}
	src := &fakeDiagSrc{
		statuses: []lsp.Status{{Name: "go", State: "up"}},
		byPath:   map[string][]lsp.Diagnostic{path: diags},
	}

	res, err := NewDiagnostics(src).Execute(context.Background(), mustJSON(t, map[string]any{
		"maxResults": 3,
	}), allowAll(dir))
	if err != nil {
		t.Fatal(err)
	}
	p := parseDiagPayload(t, res)
	if !p.Truncated || p.Count != 3 || p.Total != 10 {
		t.Fatalf("bounds = %+v", p)
	}
	if res.Title != "3 diagnostics (truncated)" {
		t.Fatalf("title = %q", res.Title)
	}

	// Cap at MaxDiagnosticsMaxResults.
	res, err = NewDiagnostics(src).Execute(context.Background(), mustJSON(t, map[string]any{
		"maxResults": 99999,
	}), allowAll(dir))
	if err != nil {
		t.Fatal(err)
	}
	p = parseDiagPayload(t, res)
	if p.Count != 10 || p.Truncated {
		t.Fatalf("high max should return all: %+v", p)
	}
}

func TestDiagnosticsNoServerAndCrashIsolation(t *testing.T) {
	dir := t.TempDir()
	tc := allowAll(dir)

	// Nil source.
	res, err := NewDiagnostics(nil).Execute(context.Background(), mustJSON(t, map[string]any{}), tc)
	if err != nil {
		t.Fatal(err)
	}
	p := parseDiagPayload(t, res)
	if !p.OK || p.Count != 0 || !strings.Contains(p.Note, "not configured") {
		t.Fatalf("nil src = %+v", p)
	}
	if res.Title != "diagnostics unavailable" {
		t.Fatalf("title = %q", res.Title)
	}

	// No servers configured.
	res, err = NewDiagnostics(&fakeDiagSrc{}).Execute(context.Background(), mustJSON(t, map[string]any{}), tc)
	if err != nil {
		t.Fatal(err)
	}
	p = parseDiagPayload(t, res)
	if !strings.Contains(p.Note, "no language servers configured") {
		t.Fatalf("empty statuses note = %q", p.Note)
	}

	// Crashed / down servers.
	res, err = NewDiagnostics(&fakeDiagSrc{
		statuses: []lsp.Status{{Name: "go", State: "down", Error: "server exited"}},
	}).Execute(context.Background(), mustJSON(t, map[string]any{}), tc)
	if err != nil {
		t.Fatal(err)
	}
	p = parseDiagPayload(t, res)
	if !strings.Contains(p.Note, "no live") {
		t.Fatalf("down note = %q", p.Note)
	}
	if len(p.Servers) != 1 || p.Servers[0].State != "down" {
		t.Fatalf("servers = %+v", p.Servers)
	}
	if res.Title != "diagnostics unavailable" {
		t.Fatalf("title = %q", res.Title)
	}

	// Panic isolation.
	res, err = NewDiagnostics(&fakeDiagSrc{panicAll: true, statuses: []lsp.Status{{Name: "go", State: "up"}}}).
		Execute(context.Background(), mustJSON(t, map[string]any{}), tc)
	if err != nil {
		t.Fatal(err)
	}
	p = parseDiagPayload(t, res)
	if !p.OK || !strings.Contains(p.Note, "failed") {
		t.Fatalf("panic payload = %+v", p)
	}
}

func TestDiagnosticsPathValidation(t *testing.T) {
	dir := t.TempDir()
	tc := allowAll(dir)
	src := &fakeDiagSrc{statuses: []lsp.Status{{Name: "go", State: "up"}}}

	_, err := NewDiagnostics(src).Execute(context.Background(), mustJSON(t, map[string]any{
		"path": "../outside.go",
	}), tc)
	if err == nil {
		t.Fatal("expected workspace escape")
	}
	var esc *WorkspaceEscapeError
	if !errors.As(err, &esc) {
		t.Fatalf("want WorkspaceEscapeError, got %T %v", err, err)
	}

	_, err = NewDiagnostics(src).Execute(context.Background(), json.RawMessage(`{`), tc)
	if err == nil {
		t.Fatal("want invalid json error")
	}
}

func TestDiagnosticsPermissionDenied(t *testing.T) {
	dir := t.TempDir()
	tc := &Context{
		WorkDir: dir,
		Ask: func(context.Context, AskRequest) error {
			return errors.New("denied")
		},
	}
	_, err := NewDiagnostics(&fakeDiagSrc{}).Execute(context.Background(), mustJSON(t, map[string]any{}), tc)
	if err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("got %v", err)
	}
}

func TestDiagnosticsContractAndDeferred(t *testing.T) {
	tl := NewDiagnostics(nil)
	c := LookupContract(tl)
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if c.SideEffect != SideEffectRead || c.Idempotency != IdempotencySafeRetry {
		t.Fatalf("contract = %+v", c)
	}
	if !IsDeferredTool("diagnostics") || IsCoreTool("diagnostics") {
		t.Fatal("diagnostics should be deferred non-core")
	}

	reg := NewRegistry(NewRead(), NewDiagnostics(nil), NewDefinition(nil))
	reg.Register(NewToolSearch(reg))
	reg.SetDeferLoading(true)
	names := schemaNameSet(reg.SchemasForProvider())
	if names["diagnostics"] {
		t.Fatal("diagnostics should be deferred from provider schemas")
	}

	res, err := NewToolSearch(reg).Execute(context.Background(), mustJSON(t, map[string]any{
		"query": "diagnostics",
	}), allowAll(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "- diagnostics:") {
		t.Fatalf("toolsearch output = %q", res.Output)
	}
	if !reg.Discovered("diagnostics") {
		t.Fatal("diagnostics not discovered")
	}
	if !schemaNameSet(reg.SchemasForProvider())["diagnostics"] {
		t.Fatal("diagnostics missing from provider schemas after discover")
	}
}

func TestDiagnosticsEmptyArgsAndNull(t *testing.T) {
	dir := t.TempDir()
	src := &fakeDiagSrc{statuses: []lsp.Status{{Name: "go", State: "up"}}}
	for _, raw := range []json.RawMessage{nil, json.RawMessage(`{}`), json.RawMessage(`null`)} {
		res, err := NewDiagnostics(src).Execute(context.Background(), raw, allowAll(dir))
		if err != nil {
			t.Fatalf("args %s: %v", raw, err)
		}
		p := parseDiagPayload(t, res)
		if p.Scope != "workspace" || !p.OK {
			t.Fatalf("args %s: %+v", raw, p)
		}
	}
}

func TestDiagnosticsManagerIntegrationNoServer(t *testing.T) {
	// Real manager with no servers: soft empty result, no hang.
	mgr := lsp.NewManager(t.TempDir())
	defer mgr.Close()
	res, err := NewDiagnostics(mgr).Execute(context.Background(), mustJSON(t, map[string]any{}), allowAll(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	p := parseDiagPayload(t, res)
	if !p.OK || p.Count != 0 {
		t.Fatalf("payload = %+v", p)
	}
	if !strings.Contains(p.Note, "no language servers") {
		t.Fatalf("note = %q", p.Note)
	}
}
